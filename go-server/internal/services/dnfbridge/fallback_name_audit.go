package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

// fallbackNameAuditEnv 只是"合成角色名审计"的开关，**不是**行为开关：
// 打开它不会改动任何一个下行字节，只会在 packet_log.txt 里多出一行
// `game-fallback-name-in-outbound-packet`。
//
// 背景（2026-09-26 现场）：CN 客户端在每次登录后弹出 `pvp02` 新 PvP 竞技场
// 匹配房窗口，房内参与者徽标渲染的是 **`dnf_46688`**；而 `dnf_<角色ID>` 是本
// 服务端**唯一**的合成角色名（`csharpSelectCharacterName`，
// game_select_resolve.go:119-124），即该徽标来自服务端下发的某一份名单里
// **角色记录缺失或名字为空**的那一行。该行的**发包点**无法只靠代码读出来：
//
//   - 服务端 packet_log 的 `hex=` 只记录包体**前 512 字节**
//     （packet_log.go:13 packetLogHexLimit），名字若在长包尾部就 grep 不到；
//   - 客户端 90CN_protocol.log 里受 DPROTO 保护的包只留外层信封
//     （game_dproto.go:182-215，外层 opcode = DprotoServerEnvelopeOpcode），
//     内层明文根本不在客户端日志里，所以"客户端日志里搜不到 dnf_"**不能**证明
//     服务端没发。
//
// 打开 `DNFBRIDGE_FALLBACK_NAME_AUDIT=on` 后，在**DPROTO 编码之前**的明文内层包
// 上扫描该前缀，一次登录即可把 `msg_id` + 名字 + 偏移钉死，然后再决定
// "不下发该行 / 跳过名字为空的参与者 / 修数据源"。
//
// 默认值 = off，零值 = off：所有不经过 optionsFromConfig 的构造路径（测试与探针）
// 以及线上默认都保持"一个字节、一行日志都不变"。
const fallbackNameAuditEnv = "DNFBRIDGE_FALLBACK_NAME_AUDIT"

// fallbackCharacterNameMarker 是服务端合成角色名的前缀，与
// game_select_resolve.go:119-124 的 `"dnf_" + strconv.Itoa(int(charID))` 成对。
// 全仓只有这一个产出点（已核对：不存在第二处 `dnf_` 角色名拼装）。
const fallbackCharacterNameMarker = "dnf_"

// fallbackNameAuditContextBytes 是记进日志的上下文窗口半径：足够看清该名字
// 所在行的相邻字段（槽位/等级/职业），又不会把整包倒进日志。
const fallbackNameAuditContextBytes = 24

func (s *Service) fallbackNameAuditEnabled() bool {
	return s != nil && s.options.fallbackNameAudit
}

// auditFallbackNameInOutboundPacket 在包体落盘/加压之前扫描合成角色名。
// layer 只用于日志判读：`game_upper_inner_pre_dproto` 的 header[1:3] 是
// upper 消息号；`game_raw_frame` 是 legacy/固定 15 字节帧，[1:3] 不是消息号。
func (s *Service) auditFallbackNameInOutboundPacket(session *gameSession, packet []byte, layer string) {
	if !s.fallbackNameAuditEnabled() || len(packet) == 0 {
		return
	}
	offset := bytes.Index(packet, []byte(fallbackCharacterNameMarker))
	if offset < 0 {
		return
	}
	charID, ok := fallbackCharacterIDAt(packet, offset)
	if !ok {
		// 前缀在但后面不是合法角色 ID（例如玩家真名 "dnf_x"）：不记。
		return
	}
	msgID := uint16(0)
	msgIDSource := "unreadable_packet_shorter_than_3_bytes"
	if len(packet) >= 3 {
		msgID = binary.LittleEndian.Uint16(packet[1:3])
		msgIDSource = "bytes_1_3_little_endian"
	}
	windowStart := offset - fallbackNameAuditContextBytes
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := offset + len(fallbackCharacterNameMarker) + 5 + fallbackNameAuditContextBytes
	if windowEnd > len(packet) {
		windowEnd = len(packet)
	}
	connID := ""
	channelID := 0
	selectedCharID := uint16(0)
	if session != nil {
		connID = session.connID
		channelID = session.channel.ID
		selectedCharID = session.selectedCharacterID
	}
	s.logGameEvent(session, "game-fallback-name-in-outbound-packet",
		"layer", layer,
		"packet_len", len(packet),
		// 字段名刻意**不叫** msg_id：suppressPacketLogEvent 见到 msg_id ∈
		// {1276,1516,1518} 会把整条事件丢掉（service.go:713-734）。审计行必须
		// 在任何消息号下都能落盘，否则又是一次"被日志过滤器骗了"。
		"outbound_msg_id", msgID,
		"outbound_msg_id_source", msgIDSource,
		"marker_offset", offset,
		"name", fallbackCharacterNameMarker+strconv.Itoa(int(charID)),
		"name_char_id", charID,
		"selected_char_id", selectedCharID,
		"target_channel_id", channelID,
		"conn_id", connID,
		"context_hex", hex.EncodeToString(packet[windowStart:windowEnd]),
		"context_offset", windowStart,
		"switch", fallbackNameAuditEnv,
		"reason", "synthetic_character_name_would_be_sent_to_client")
}

// fallbackCharacterIDAt 解析 marker 后面的十进制角色 ID。要求至少 1 位数字、
// 至多 5 位（uint16），且后续字节不是数字——与 strconv.Itoa(uint16) 的产出
// 形状完全一致，避免把 "dnf_abc" 这类玩家真实名字误判成合成名。
func fallbackCharacterIDAt(packet []byte, offset int) (uint16, bool) {
	start := offset + len(fallbackCharacterNameMarker)
	end := start
	for end < len(packet) && end-start < 5 && packet[end] >= '0' && packet[end] <= '9' {
		end++
	}
	if end == start {
		return 0, false
	}
	if end < len(packet) && packet[end] >= '0' && packet[end] <= '9' {
		return 0, false
	}
	value, err := strconv.ParseUint(string(packet[start:end]), 10, 16)
	if err != nil || value == 0 {
		// charID==0 时 csharpSelectCharacterName 返回空串，不可能产出该前缀。
		return 0, false
	}
	return uint16(value), true
}
