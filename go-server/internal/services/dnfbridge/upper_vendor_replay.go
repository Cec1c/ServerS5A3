package dnfbridge

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// DNFBRIDGE_REPLAY_UPPER_FILE / DNFBRIDGE_REPLAY_UPPER_IDS 是**诊断开关**，默认关闭。
//
// 目的：把厂商 9.16 服务端在登录相位实际发出的上层包，按 msg_id 原样回放到我们的
// 会话里，用自动探针判定弹窗/卡死到底由哪个包的内容触发。厂商日志
// （packet_log.txt 的 SEND kind=game-upper 行）记录的是 **dproto 编码前的内层明文**，
// 因此回放必须替换内层字节后继续走我们正常的 EncodeServer 保护层——直接往连接上写
// 明文会让客户端拿到未保护的帧，结论立刻变成假。
//
//	DNFBRIDGE_REPLAY_UPPER_FILE=/opt/dnf90/data/vendor-login-replay.txt
//	DNFBRIDGE_REPLAY_UPPER_IDS=5,19,21        # 可选：只回放这些 msg_id（不设=全表）
//
// 文件格式（每行一个厂商包，顺序即厂商发送顺序）：
//
//	# 注释
//	183 0 00b700120000005c7c239402000000000001
//	5 7 0705...
//
// 同一个 msg_id 出现多次时按轮转取用（厂商 op183 就是连发两次），这样"我们发第 n 个
// 该 msg_id 的包"会拿到厂商的第 n 个，而不是永远拿第一个。
const (
	upperReplayFileEnv = "DNFBRIDGE_REPLAY_UPPER_FILE"
	upperReplayIDsEnv  = "DNFBRIDGE_REPLAY_UPPER_IDS"
)

type upperReplayTable struct {
	file    string
	allow   map[uint16]struct{}
	byMsgID map[uint16][][]byte
	cursor  map[uint16]*uint64
	total   int
}

var (
	upperReplayOnce   sync.Once
	upperReplayParsed upperReplayTable
)

func parseUpperReplayTable(text string, allowText string) (upperReplayTable, error) {
	table := upperReplayTable{
		allow:   make(map[uint16]struct{}),
		byMsgID: make(map[uint16][][]byte),
		cursor:  make(map[uint16]*uint64),
	}
	for _, field := range strings.FieldsFunc(allowText, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r', '|':
			return true
		}
		return false
	}) {
		value, err := strconv.ParseUint(field, 0, 16)
		if err != nil {
			return upperReplayTable{}, fmt.Errorf("回放 msg_id 白名单 %q 不是合法数字: %w", field, err)
		}
		table.allow[uint16(value)] = struct{}{}
	}
	for lineNumber, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			return upperReplayTable{}, fmt.Errorf("回放表第 %d 行字段不足: %q", lineNumber+1, trimmed)
		}
		msgID, err := strconv.ParseUint(fields[0], 0, 16)
		if err != nil {
			return upperReplayTable{}, fmt.Errorf("回放表第 %d 行 msg_id 非法: %w", lineNumber+1, err)
		}
		classification, err := strconv.ParseUint(fields[1], 0, 8)
		if err != nil {
			return upperReplayTable{}, fmt.Errorf("回放表第 %d 行 classification 非法: %w", lineNumber+1, err)
		}
		packet, err := hex.DecodeString(fields[2])
		if err != nil {
			return upperReplayTable{}, fmt.Errorf("回放表第 %d 行 hex 非法: %w", lineNumber+1, err)
		}
		if len(packet) < 3 {
			return upperReplayTable{}, fmt.Errorf("回放表第 %d 行包体过短: %d", lineNumber+1, len(packet))
		}
		if packet[0] != byte(classification) || int(packet[1])|int(packet[2])<<8 != int(msgID) {
			return upperReplayTable{}, fmt.Errorf(
				"回放表第 %d 行头字段与包体不一致: 声明 msg_id=%d classification=%d, 包头 msg_id=%d classification=%d",
				lineNumber+1, msgID, classification, int(packet[1])|int(packet[2])<<8, packet[0])
		}
		key := uint16(msgID)
		table.byMsgID[key] = append(table.byMsgID[key], packet)
		if _, ok := table.cursor[key]; !ok {
			var counter uint64
			table.cursor[key] = &counter
		}
		table.total++
	}
	return table, nil
}

func (t upperReplayTable) Empty() bool {
	return t.total == 0
}

func (t upperReplayTable) Allowed(msgID uint16) bool {
	if len(t.allow) == 0 {
		return true
	}
	_, ok := t.allow[msgID]
	return ok
}

// Next 返回该 msg_id 下一个待回放的厂商包（轮转）。
func (t upperReplayTable) Next(msgID uint16) ([]byte, bool) {
	packets := t.byMsgID[msgID]
	if len(packets) == 0 || !t.Allowed(msgID) {
		return nil, false
	}
	counter := t.cursor[msgID]
	if counter == nil {
		return packets[0], true
	}
	index := atomic.AddUint64(counter, 1) - 1
	return packets[index%uint64(len(packets))], true
}

func currentUpperReplayTable() upperReplayTable {
	upperReplayOnce.Do(func() {
		path := strings.TrimSpace(os.Getenv(upperReplayFileEnv))
		if path == "" {
			upperReplayParsed = upperReplayTable{}
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			upperReplayParsed = upperReplayTable{file: path + " (读取失败: " + err.Error() + ")"}
			return
		}
		table, err := parseUpperReplayTable(string(data), os.Getenv(upperReplayIDsEnv))
		if err != nil {
			upperReplayParsed = upperReplayTable{file: path + " (解析失败: " + err.Error() + ")"}
			return
		}
		table.file = path
		upperReplayParsed = table
	})
	return upperReplayParsed
}

// replayUpperSend 判断当前上层包是否应改为回放厂商字节。返回的字节必须继续走
// writeGameUpperPacketLocked 的审计/保护层，调用方不得直接写连接。
func (s *Service) replayUpperSend(session *gameSession, inner []byte) ([]byte, bool) {
	table := currentUpperReplayTable()
	if table.Empty() {
		return nil, false
	}
	_, msgID, ok := upperPacketClassMsgID(inner)
	if !ok {
		return nil, false
	}
	vendor, ok := table.Next(msgID)
	if !ok {
		return nil, false
	}
	s.logGameEvent(session, "game-upper-replayed-vendor",
		"msg_id", msgID,
		"ours_len", len(inner),
		"vendor_len", len(vendor),
		"replay_file", table.file,
		"reason", "diagnostic_vendor_login_phase_byte_equivalence")
	return vendor, true
}
