package dnfbridge

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"sync"
)

// DNFBRIDGE_SUPPRESS_UPPER_SENDS 是登录相位差分定位的**诊断开关**，不属于
// 生产玩法路径，默认关闭（环境变量为空 = 一个包都不过滤）。
//
// 背景：厂商 9.16 服务端与我们在同一登录相位存在机械差分——我们多发了
// 441/646/63/143/455/102/14 等上层包，厂商多发了 183 等。要定位“客户端卡死+
// 弹窗”到底由哪个包触发，必须能按任意子集做二分；如果每验证一组都要改代码重新
// 编译，一轮就要数分钟且容易引入新变量。这里把“发不发”外化成环境变量：
//
//	DNFBRIDGE_SUPPRESS_UPPER_SENDS=441,646,63      # 只按 msg_id 屏蔽
//	DNFBRIDGE_SUPPRESS_UPPER_SENDS=0:183,7:441     # 按 classification:msg_id 精确屏蔽
//	支持 0x 前缀（0x1b7 等价 439）与空白/分号分隔。
//
// 命中时会写一条 game-upper-suppressed 事件（含 msg_id/classification/长度与开关
// 原文），在真正写连接之前返回。game_wire.go 的 SEND 行写在本过滤器之前，
// 因此 SEND 行与上层发送函数返回 nil 都不能单独证明包已写连接或被客户端消费；
// 取证还需检查同次 game-upper-suppressed / game-upper-vendor-absent-blocked 事件。
const upperSendFilterEnv = "DNFBRIDGE_SUPPRESS_UPPER_SENDS"

type upperSendFilterKey struct {
	classification byte
	msgID          uint16
}

type upperSendFilter struct {
	raw        string
	anyClass   map[uint16]struct{}
	byClassMsg map[upperSendFilterKey]struct{}
}

var (
	upperSendFilterOnce   sync.Once
	upperSendFilterParsed upperSendFilter
)

// upperPacketClassMsgID 读取上层包头的 classification 与 msg_id。
// 12 字节与 16 字节两种 upper 头的 [0] 与 [1:3] 布局一致（见
// protocol.ChannelHeader / GameServerUpperHeaderSize16），因此同一个读取器对两种
// 头都成立；长度不足时返回 false，调用方据此放行（绝不因为解析失败吞包）。
func upperPacketClassMsgID(packet []byte) (byte, uint16, bool) {
	if len(packet) < 3 {
		return 0, 0, false
	}
	return packet[0], binary.LittleEndian.Uint16(packet[1:3]), true
}

// parseUpperSendFilter 解析开关文本。无法识别的片段被忽略（宁可少屏蔽也不误屏蔽），
// 并在 raw 里保留原文供日志使用。
func parseUpperSendFilter(raw string) upperSendFilter {
	filter := upperSendFilter{raw: strings.TrimSpace(raw)}
	fields := strings.FieldsFunc(filter.raw, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r', '|':
			return true
		}
		return false
	})
	for _, field := range fields {
		classText := ""
		msgText := field
		if index := strings.IndexByte(field, ':'); index >= 0 {
			classText, msgText = field[:index], field[index+1:]
		}
		msgID, err := parseUpperFilterNumber(msgText, 16)
		if err != nil {
			continue
		}
		if classText == "" {
			if filter.anyClass == nil {
				filter.anyClass = make(map[uint16]struct{})
			}
			filter.anyClass[uint16(msgID)] = struct{}{}
			continue
		}
		class, err := parseUpperFilterNumber(classText, 8)
		if err != nil || class > 0xFF {
			continue
		}
		if filter.byClassMsg == nil {
			filter.byClassMsg = make(map[upperSendFilterKey]struct{})
		}
		filter.byClassMsg[upperSendFilterKey{classification: byte(class), msgID: uint16(msgID)}] = struct{}{}
	}
	return filter
}

func parseUpperFilterNumber(text string, bitSize int) (uint64, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseUint(trimmed, 0, bitSize)
}

func (f upperSendFilter) Empty() bool {
	return len(f.anyClass) == 0 && len(f.byClassMsg) == 0
}

func (f upperSendFilter) Suppresses(classification byte, msgID uint16) bool {
	if len(f.anyClass) > 0 {
		if _, ok := f.anyClass[msgID]; ok {
			return true
		}
	}
	if len(f.byClassMsg) > 0 {
		if _, ok := f.byClassMsg[upperSendFilterKey{classification: classification, msgID: msgID}]; ok {
			return true
		}
	}
	return false
}

func currentUpperSendFilter() upperSendFilter {
	upperSendFilterOnce.Do(func() {
		upperSendFilterParsed = parseUpperSendFilter(os.Getenv(upperSendFilterEnv))
	})
	return upperSendFilterParsed
}

// suppressUpperSend 在真正编码/落日志之前判断该上层包是否被诊断开关屏蔽。
// 返回 true 表示调用方必须直接返回，不写日志、不写连接。
func (s *Service) suppressUpperSend(session *gameSession, classification byte, msgID uint16, packetLen int) bool {
	filter := currentUpperSendFilter()
	if filter.Empty() || !filter.Suppresses(classification, msgID) {
		return false
	}
	s.logGameEvent(session, "game-upper-suppressed",
		"msg_id", msgID,
		"classification", classification,
		"packet_len", packetLen,
		"switch", upperSendFilterEnv,
		"filter", filter.raw,
		"reason", "diagnostic_login_phase_bisect")
	return true
}
