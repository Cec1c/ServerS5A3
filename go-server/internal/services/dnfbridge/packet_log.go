package dnfbridge

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// packetLogHexLimit 是 packet_log 每行 `hex=` 记录的**包体最大字节数**。
//
// 2026-09-29：原为常量 512（限制本身在 `fallback_name_audit.go:20` 有记录），
// 但"把一条 1028 字节的 actor body 与一条 1631 字节的逐字段对照"这类取证需要完整包体，
// 512 只能看到前半段。因此改为可由 env 覆盖 —— **默认仍然是 512**：
// 不设 env 时这一行日志与改动前**逐字节相同**（同一个 `data` 切片、同一个前缀、
// 同一个 `truncated=true` 标记位）。
//
// 边界（这一点是硬约束）：本改动**只影响 `hex=` 字段的长度**，
// **不改任何下行字节、不改长度字段、不改时序** —— 截断发生在日志格式化处，
// 取的是调用方传进来的同一个 `data`。
//
//   - 默认 / 空 / 非法值 / <=0 = 512（今天的行）。
//   - `DNFBRIDGE_PACKET_LOG_HEX_LIMIT=<n>` = 最多记录 n 字节，上限 packetLogHexLimitMax。
//   - 一行回退：删掉这个 env（或置 0）。
const (
	packetLogHexLimitDefault = 512
	packetLogHexLimitMax     = 16384
	packetLogHexLimitEnv     = "DNFBRIDGE_PACKET_LOG_HEX_LIMIT"
)

// packetLogHexLimit 在包初始化时读一次，避免每行都查环境变量。
var packetLogHexLimit = packetLogHexLimitDefault

func init() {
	raw := strings.TrimSpace(os.Getenv(packetLogHexLimitEnv))
	if raw == "" {
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return
	}
	if n > packetLogHexLimitMax {
		n = packetLogHexLimitMax
	}
	packetLogHexLimit = n
}

// packetLogTimestampsEnv 控制包日志每行**行尾**是否追加 `t=<RFC3339Nano>`。
//
// 线上 `packet_log.txt` 原本没有时间戳（`_dist/quest-finish-latency-diagnosis.md`
// §1.1），所以"同一次完成任务里 ACK→356→574→21 各差多少毫秒"根本无法测量。
//   - **默认 on**（未设置 / 空 / 未知值）。
//   - `off` / `legacy` / `0` / `false` / `no` / `disabled` / `none` = 逐字节今天的行，
//     用于一键回退（时间戳是追加字段，回退后整行与改动前完全相同）。
//
// 现有字段顺序一律不变、只追加在行尾 ⇒ 既有 grep（`len=`/`hex=`/`msg_id=`…）继续可用。
const packetLogTimestampsEnv = "DNFBRIDGE_PACKET_LOG_TIMESTAMPS"

// packetLogTimestampsEnabled 在 logger 构造时读一次，避免每行都查环境变量。
func packetLogTimestampsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(packetLogTimestampsEnv))) {
	case "off", "legacy", "0", "false", "no", "disabled", "disable", "none":
		return false
	default:
		return true
	}
}

// packetLogFlushBytes 控制缓冲写回阈值。包日志在服务端热路径上（每个包一行），
// 原来是一行一次无缓冲 write + 全局锁；开盒子/穿时装这种一次几十个包的操作会被
// 系统调用和锁串行化拖慢。改成 64 KiB 缓冲后，绝大多数情况只是内存追加。
const packetLogFlushBytes = 64 << 10

// packetLogFlushInterval 定期把缓冲写回磁盘。只有阈值触发时，一个小会话
// （几百行）可能几分钟都不落盘，现场排查时看到的就是空文件；2 秒一次刷新的
// 代价可以忽略，却让日志始终接近实时。
const packetLogFlushInterval = 2 * time.Second

type packetLogger struct {
	mu     sync.Mutex
	file   *os.File
	writer *bufio.Writer
	// timestamps 在 open 时按 DNFBRIDGE_PACKET_LOG_TIMESTAMPS 解析一次：默认 true，
	// 行尾追加 `t=<RFC3339Nano>`；false 时整行与改动前逐字节相同。
	timestamps bool
	// stopFlush 关闭时通知定期刷新协程退出。
	stopFlush chan struct{}
	// flushOnce 保证 Close 不会重复关闭 stopFlush。
	flushOnce sync.Once
}

func openPacketLogger(path string) (*packetLogger, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640) //nolint:gosec // G304：路径来自部署环境变量，只用于本地包日志。
	if err != nil {
		return nil, err
	}
	logger := &packetLogger{
		file:       file,
		writer:     bufio.NewWriterSize(file, packetLogFlushBytes),
		timestamps: packetLogTimestampsEnabled(),
		stopFlush:  make(chan struct{}),
	}
	// 文件头分隔行**不带**时间戳：它本身就写着启动时间，且行首行尾的 `===` 是既有
	// grep/工具的锚点。DNFBRIDGE_PACKET_LOG_TIMESTAMPS 只作用于数据行。
	logger.writeRawLine("=== LongHeng Go dnfbridge packet log started " + time.Now().UTC().Format(time.RFC3339) + " ===")
	go logger.flushPeriodically()
	return logger, nil
}

// flushPeriodically 把缓冲内容定期写回，避免小会话的日志长时间停在内存里。
func (l *packetLogger) flushPeriodically() {
	if l == nil {
		return
	}
	ticker := time.NewTicker(packetLogFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopFlush:
			return
		case <-ticker.C:
			l.mu.Lock()
			if l.writer != nil && l.writer.Buffered() > 0 {
				_ = l.writer.Flush()
			}
			l.mu.Unlock()
		}
	}
}

func (l *packetLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.flushOnce.Do(func() {
		if l.stopFlush != nil {
			close(l.stopFlush)
		}
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	var err error
	if l.writer != nil {
		err = l.writer.Flush()
		l.writer = nil
	}
	err = errorsJoin(err, l.file.Close())
	l.file = nil
	return err
}

func (l *packetLogger) Log(direction string, kind string, data []byte, fields ...any) {
	if l == nil || l.file == nil {
		return
	}
	fields = redactPacketDiagnosticErrors(fields)
	if metadata, sensitive := loginPacketLogMetadata(kind, data, fields); sensitive {
		l.writeLine(redactedLoginPacketLogLine(direction, kind, data, metadata))
		return
	}
	// 截断只作用于即将写进日志的十六进制**副本**：hexData 是 data 的重新切片，
	// 调用方的包体、长度与发送时序都不受影响。
	hexData := data
	truncated := ""
	if len(hexData) > packetLogHexLimit {
		hexData = hexData[:packetLogHexLimit]
		truncated = " truncated=true"
	}
	line := fmt.Sprintf("%s kind=%s", direction, kind)
	for i := 0; i+1 < len(fields); i += 2 {
		line += fmt.Sprintf(" %v=%v", fields[i], fields[i+1])
	}
	line += fmt.Sprintf(" len=%d%s hex=%s", len(data), truncated, hex.EncodeToString(hexData))
	l.writeLine(line)
}

func (l *packetLogger) Event(kind string, fields ...any) {
	if l == nil || l.file == nil {
		return
	}
	fields = redactPacketDiagnosticErrors(fields)
	if metadata, sensitive := loginPacketLogMetadata(kind, nil, fields); sensitive {
		fields = append(metadata, "payload_redacted", true)
	}
	line := "EVENT kind=" + kind
	for i := 0; i+1 < len(fields); i += 2 {
		line += fmt.Sprintf(" %v=%v", fields[i], fields[i+1])
	}
	l.writeLine(line)
}

// writeLine 写一条**数据行**（RECV/SEND/EVENT）。时间戳在**取锁之前**采样（排队等锁
// 的时间不属于这条日志对应的事件），且只追加在行尾：现有字段顺序不变，既有 grep
// （`len=`/`hex=`/`msg_id=`…）全部继续可用。
func (l *packetLogger) writeLine(line string) {
	if l == nil || l.file == nil {
		return
	}
	if l.timestamps {
		line += " t=" + time.Now().UTC().Format(time.RFC3339Nano)
	}
	l.writeRawLine(line)
}

// writeRawLine 原样写一行（不加时间戳）。只有文件头的 `=== … ===` 分隔行走这里。
func (l *packetLogger) writeRawLine(line string) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil {
		l.writer = bufio.NewWriterSize(l.file, packetLogFlushBytes)
	}
	if _, err := l.writer.WriteString(line + "\n"); err != nil {
		return
	}
	if l.writer.Buffered() >= packetLogFlushBytes {
		_ = l.writer.Flush()
	}
}

func errorsJoin(left error, right error) error {
	switch {
	case left == nil:
		return right
	case right == nil:
		return left
	default:
		return fmt.Errorf("%v; %w", left, right)
	}
}
