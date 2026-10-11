package dnfbridge

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// DNFBRIDGE_INJECT_UPPER_FILE 是**诊断开关**，默认关闭。它补上"厂商有、我们完全
// 没有"的那一半登录下行包。
//
// 为什么必须有它：`DNFBRIDGE_REPLAY_UPPER_FILE` 只能替换"我们本来也会发"的同 msg_id
// 包；厂商在登录最前段连发两次的 `op183`（2 字节体）我们一个都不发，光靠替换永远
// 补不回来。这个开关让这些包挂在某一"锚点包"前面按厂商顺序补发。
//
//	文件格式（每条一行，锚点即"发到哪个我方包时补发"）：
//	  # 注释
//	  4 183 0 00b700120000005c7c239402000000000001
//	  ^ 锚点 msg_id      ^ 厂商包 msg_id/class/完整字节
//
// 每条注入**每会话只发一次**（见 gameSession.vendorInjectEmitted）：锚点包在登录期
// 可能被多次发送（例如 op1/op2 重入），重复注入会凭空制造出厂商没有的重复包。
//
// 注入包同样走 writeUpperPacketLockedCore：审计 → DPROTO 保护 → 写连接，
// 与正常下行包完全同一条链路，绝不会出现"注入包没被保护"的假象。
const upperInjectFileEnv = "DNFBRIDGE_INJECT_UPPER_FILE"

type upperInjectEntry struct {
	line   int
	anchor uint16
	msgID  uint16
	class  byte
	packet []byte
}

type upperInjectTable struct {
	file    string
	entries []upperInjectEntry
}

var (
	upperInjectOnce   sync.Once
	upperInjectParsed upperInjectTable
)

func parseUpperInjectTable(text string) (upperInjectTable, error) {
	var table upperInjectTable
	for index, line := range strings.Split(text, "\n") {
		lineNumber := index + 1
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			return upperInjectTable{}, fmt.Errorf("注入表第 %d 行字段不足（需要 锚点 msg_id / 厂商 msg_id / class / hex）: %q", lineNumber, trimmed)
		}
		anchor, err := strconv.ParseUint(fields[0], 0, 16)
		if err != nil {
			return upperInjectTable{}, fmt.Errorf("注入表第 %d 行锚点非法: %w", lineNumber, err)
		}
		msgID, err := strconv.ParseUint(fields[1], 0, 16)
		if err != nil {
			return upperInjectTable{}, fmt.Errorf("注入表第 %d 行厂商 msg_id 非法: %w", lineNumber, err)
		}
		class, err := strconv.ParseUint(fields[2], 0, 8)
		if err != nil {
			return upperInjectTable{}, fmt.Errorf("注入表第 %d 行 classification 非法: %w", lineNumber, err)
		}
		packet, err := hex.DecodeString(fields[3])
		if err != nil {
			return upperInjectTable{}, fmt.Errorf("注入表第 %d 行 hex 非法: %w", lineNumber, err)
		}
		if len(packet) < 3 {
			return upperInjectTable{}, fmt.Errorf("注入表第 %d 行包体过短: %d", lineNumber, len(packet))
		}
		headerMsgID := int(packet[1]) | int(packet[2])<<8
		if headerMsgID != int(msgID) || packet[0] != byte(class) {
			return upperInjectTable{}, fmt.Errorf(
				"注入表第 %d 行头字段与包体不一致: 声明 msg_id=%d class=%d, 包头 msg_id=%d class=%d",
				lineNumber, msgID, class, headerMsgID, packet[0])
		}
		table.entries = append(table.entries, upperInjectEntry{
			line:   lineNumber,
			anchor: uint16(anchor),
			msgID:  uint16(msgID),
			class:  byte(class),
			packet: packet,
		})
	}
	return table, nil
}

func (t upperInjectTable) Empty() bool { return len(t.entries) == 0 }

func currentUpperInjectTable() upperInjectTable {
	upperInjectOnce.Do(func() {
		path := strings.TrimSpace(os.Getenv(upperInjectFileEnv))
		if path == "" {
			upperInjectParsed = upperInjectTable{}
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			upperInjectParsed = upperInjectTable{file: path + " (读取失败: " + err.Error() + ")"}
			return
		}
		table, err := parseUpperInjectTable(string(data))
		if err != nil {
			upperInjectParsed = upperInjectTable{file: path + " (解析失败: " + err.Error() + ")"}
			return
		}
		table.file = path
		upperInjectParsed = table
	})
	return upperInjectParsed
}

// emitUpperVendorInjectionsLocked 在锚点包之前补发厂商独有的包（每会话每条一次）。
// 必须在 session.wireMu 持有期间调用；写入走 core 通道，不会再触发注入/屏蔽钩子。
func (s *Service) emitUpperVendorInjectionsLocked(session *gameSession, anchorMsgID uint16) error {
	table := currentUpperInjectTable()
	if table.Empty() {
		return nil
	}
	for _, entry := range table.entries {
		if entry.anchor != anchorMsgID {
			continue
		}
		if session.vendorInjectEmitted != nil && session.vendorInjectEmitted[entry.line] {
			continue
		}
		if session.vendorInjectEmitted == nil {
			session.vendorInjectEmitted = make(map[int]bool)
		}
		// 先登记再写：写失败也不重发，避免半包重入时产生重复包。
		session.vendorInjectEmitted[entry.line] = true
		s.logGameEvent(session, "game-upper-vendor-injected",
			"anchor_msg_id", entry.anchor,
			"msg_id", entry.msgID,
			"classification", entry.class,
			"packet_len", len(entry.packet),
			"inject_file", table.file,
			"inject_line", entry.line,
			"reason", "diagnostic_vendor_login_phase_missing_packet")
		if err := s.writeUpperPacketLockedCore(session, append([]byte(nil), entry.packet...)); err != nil {
			return err
		}
	}
	return nil
}
