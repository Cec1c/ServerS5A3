package dnfbridge

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

const nativeResultDeliveryLimit = 256 << 10

type nativeResultDeliveryKey struct {
	opcode  uint16
	context string
}

// 这是传输层连续写回，不是整批资产事务。每个请求及其原生ACK完整保留。
// 仅排空已到达的同入口请求；不等待静默时间，不猜选中件数，也不合并奖励。
type nativeResultDelivery struct {
	key      nativeResultDeliveryKey
	wire     []byte
	upper    [][]byte
	requests int
	frames   int
	started  time.Time
	// 最近一次接续请求的开始时间；只保护单件停滞，不限制整批总耗时。
	lastProgress time.Time
}

// 跨场景资料等发送者要求返回前真正写连接；不能因分解缓冲延后这些消息。
// 本入口ACK、局部物品行、本角色经验mode0、副职业资料，以及两种固定
// 格式的活动状态可以随结果写回。活动计时、结算及领取仍走原路径。
func (s *Service) nativeResultPacketCanWait(p *gameSession, inner []byte) bool {
	if len(inner) < 3 {
		return false
	}
	opcode := binary.LittleEndian.Uint16(inner[1:3])
	if inner[0] == 1 {
		return opcode == p.nativeResultDelivery.key.opcode
	}
	if inner[0] != 0 {
		return false
	}
	switch opcode {
	case 14, 205:
		return true
	case 1386, currentPCRoomTimePointStateID:
		// 签到9字节和在线奖励17字节状态不会改变分解上下文。保留编码
		// 顺序一并写回，避免定时刷新拆开结果；未知格式及超限仍立即释放。
		bodySize := 9
		if opcode == currentPCRoomTimePointStateID {
			bodySize = 17
		}
		return len(inner) == s.gameUpperHeaderSize()+bodySize && time.Since(p.nativeResultDelivery.lastProgress) < 4*time.Second
	case 2:
		header := s.gameUpperHeaderSize()
		const keyOffset = 5 + 0x47 // 原生mode0的单行前缀及已配套角色状态。
		if len(inner) < header+keyOffset+2 {
			return false
		}
		body := inner[header:]
		return body[0] == 0 && binary.LittleEndian.Uint16(body[1:3]) == 1 && binary.LittleEndian.Uint16(body[keyOffset:keyOffset+2]) == p.selectedCharacterID
	}
	return false
}

func nativeResultDeliveryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY"))) {
	case "off", "0", "false", "legacy":
		return false
	}
	return true
}

func nativeResultRequestKey(packet dnfproto.LatestGameStreamPacket) (nativeResultDeliveryKey, bool) {
	if packet.Kind != dnfproto.LatestGameStreamLegacy {
		return nativeResultDeliveryKey{}, false
	}
	p, err := dnfproto.ParseLegacyGamePacket(packet.Data)
	if err != nil || p.Header.Cmd != 1 {
		return nativeResultDeliveryKey{}, false
	}
	b := p.Body
	key := nativeResultDeliveryKey{opcode: p.Header.Type}
	switch p.Header.Type {
	case 26:
		if len(b) != 5 && len(b) != 9 {
			return key, false
		}
		if _, err := parseCurrentDisjointItemRequest(b[:5]); err != nil {
			return key, false
		}
		key.context = string(b[2:])
	case 416:
		b = stripLegacyTransportTrailer(b, currentExpertJobExtractionRequestSize)
		if _, err := parseCurrentExpertJobExtractionRequest(b); err != nil {
			return key, false
		}
		key.context = string(b[:4])
	case 235:
		b = stripLegacyTransportTrailer(b, 5)
		if _, err := parseCurrentExpertJobStoreDisjointRequest(b); err != nil {
			return key, false
		}
		key.context = string(b[:2]) + string(b[4:5])
	default:
		return key, false
	}
	return key, true
}

// 由原会话事件循环在请求处理前调用。人物切换、其他指令、工具或店主变化
// 都先写出已准备的结果；不把不同入口当成同一个选择集合。
func (s *Service) prepareNativeResultDelivery(p *gameSession, packet dnfproto.LatestGameStreamPacket) error {
	key, eligible := nativeResultRequestKey(packet)
	p.wireMu.Lock()
	defer p.wireMu.Unlock()
	if b := p.nativeResultDelivery; b != nil {
		if !eligible || key != b.key || b.requests >= 127 || time.Since(b.lastProgress) >= 4*time.Second {
			reason := "连续请求件数上限"
			if !eligible {
				reason = "收到其他指令"
			} else if key != b.key {
				reason = "分解入口变化"
			} else if b.requests < 127 {
				reason = "单件处理停滞上限"
			}
			if err := s.flushNativeResultDeliveryLocked(p, reason); err != nil {
				return err
			}
		}
	}
	if !eligible || p.selectedCharacterID == 0 || p.channelSceneBatch != nil || s.options.currentClientVersion != "official-24.9" || !nativeResultDeliveryEnabled() {
		return nil
	}
	if p.nativeResultDelivery == nil {
		p.nativeResultDelivery = &nativeResultDelivery{key: key, started: time.Now()}
	}
	p.nativeResultDelivery.requests++
	p.nativeResultDelivery.lastProgress = time.Now()
	return nil
}

func (s *Service) finishNativeResultDelivery(p *gameSession, reason string) error {
	p.wireMu.Lock()
	defer p.wireMu.Unlock()
	return s.flushNativeResultDeliveryLocked(p, reason)
}

// 调用方持有wireMu；顺序、序号和保护层编码已由原路径完成。
func (s *Service) flushNativeResultDeliveryLocked(p *gameSession, reason string) error {
	b := p.nativeResultDelivery
	p.nativeResultDelivery = nil
	if b == nil {
		return nil
	}
	if len(b.wire) > 0 {
		n, err := s.writeGameConnectionObserved(p.conn, b.wire, perfWireNative)
		if err == nil && n != len(b.wire) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_ = p.conn.Close()
			return err
		}
	}
	for _, inner := range b.upper {
		s.logCurrentSharedInventoryPacketWritten(p, inner)
		s.logGoldDiagnosticPacketResult(p, inner, false)
	}
	s.logGameEvent(p, "原生分解连续回包已写出", "协议", b.key.opcode, "请求数", b.requests, "报文数", b.frames, "字节数", len(b.wire), "准备毫秒", time.Since(b.started).Milliseconds(), "释放原因", reason)
	return nil
}

func (s *Service) writeNativeResultDeliveryLocked(p *gameSession, wire []byte) (int, error) {
	b := p.nativeResultDelivery
	if len(wire) > nativeResultDeliveryLimit-len(b.wire) {
		if err := s.flushNativeResultDeliveryLocked(p, "缓冲容量上限"); err != nil {
			return 0, err
		}
		return s.writeGameConnectionObserved(p.conn, wire, perfWireOverflow)
	}
	b.wire = append(b.wire, wire...)
	b.frames++
	return len(wire), nil
}
