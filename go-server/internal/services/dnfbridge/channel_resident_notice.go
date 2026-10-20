package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

const (
	currentChannelInfoEnum                = 1
	currentChannelInfoSuccess             = 1
	currentChannelResidentControllerIndex = 0
	currentChannelCommandPacketCount      = 1519
	currentChannelNotificationPacketCount = 1468
	official249CommandPacketCount         = 1579
	official249NotificationPacketCount    = 1543
)

var (
	errResidentChannelUnavailable = errors.New("dnf resident channel is unavailable")
	errResidentChannelRange       = errors.New("dnf resident channel field exceeds current EXE range")
)

// currentCommittedChannelOwner 给出"本会话可以提交进 u8 owner 字段"的频道号。
//
// 两条既有约定（2026-09-26 / 09-27 现场取证，语义保持不变）：
//
//	① 会话真实频道可用时优先用它 —— 客户端场景频道就是它，两边一致才不会触发
//	   `mode0-owner-compare ... branch=remote-actor`；
//	② 连上 [crack]/[deathtower] 这类**引导频道**时，允许用 catalog 选出的普通落点
//	   频道（resident）发布 op1 CHANNELINFO，客户端才会按落点提交。
//
// 2026-09-27 补充：**"真实频道 > 255 装不进 u8 owner 字段"这一情形明确归到 ②**。
// 频道目录里存在 508（[deathtower]）、261..263、501..507，u8 owner 字段表达不了它们；
// 这时唯一正确的做法是取 catalog 落点频道（恒 ≤255），而**不是**把 508 截断成 252
// （252 是另一个真实频道号，客户端会因此崩溃，见 channel_owner_range.go）。
// 连落点也装不下时返回 errResidentChannelRange —— 明确失败，不静默降级。
func currentCommittedChannelOwner(session *gameSession) (byte, error) {
	if session == nil {
		return 0, errResidentChannelUnavailable
	}
	// 2026-09-27 现场：客户端 trace 无限刷
	//   mode0-owner-compare scene_channel=252 local_owner_channel=10 branch=remote-actor
	// 场景频道（会话真实所在 252）与我们下发的 owner 频道（catalog 选出的 10）不一致时，
	// 客户端会卡在这个 owner-compare 循环里：渲染被拖死、占位面板常驻（操作者看到的
	// "弹窗 + 卡死"）。因此这里**优先用会话真实频道**；只有会话停在常驻/引导频道时才
	// 回落到 catalog 选出的落点频道（保持 2026-09-26 特殊频道引导的原有语义）。
	if owner, ok := currentOwnerChannelFieldValue(session.channel.ID); ok &&
		!channelcatalog.IsResidentChannel(session.channel) {
		return owner, nil
	}
	resident := session.residentChannel
	residentOwner, residentOK := currentOwnerChannelFieldValue(resident.ID)
	if !residentOK {
		return 0, fmt.Errorf("%w: committed channel=%d connected channel=%d",
			errResidentChannelRange, resident.ID, session.channel.ID)
	}
	if session.channel.ID != resident.ID {
		// 2026-09-26「特殊频道先留着，但不要登录就直接去」：客户端连上
		// [crack]/[deathtower] 这类**引导频道**时不会自己换线，也不会发登录包
		// （现场 508：shared-account 回落 + 角色列表为空）。这类连接必须允许用
		// catalog 选出的普通落点频道发布 op1 CHANNELINFO，客户端才会按落点提交。
		// 普通频道之间的落点不一致仍然保守拒绝（保持原有语义）。
		if channelcatalog.IsResidentChannel(session.channel) {
			return 0, fmt.Errorf(
				"%w: connected channel=%d committed channel=%d",
				errResidentChannelUnavailable,
				session.channel.ID,
				resident.ID,
			)
		}
	}
	return residentOwner, nil
}

// buildCurrentChannelResidentNotice builds the body consumed by current
// NoPack.exe sub_1D70130: u32 protobuf length followed by
// PB_ENUM_NOTIPACKET_CHANNELINFO. Field 7 is the server id and field 8 is the
// channel id. Field 9 is the current client's three-entry local-controller
// index, not the unrestricted raw channel type from channel_info.etc.
func (s *Service) buildCurrentChannelResidentNotice(channel channelcatalog.Channel, now time.Time) ([]byte, error) {
	// Official 24.9.12.0 DNF.exe (SHA256 7d634521...) checks CMD=0x62b,
	// NOTI=0x607 at VA 0x02005080, called by CHANNELINFO at 0x02023b94.
	// Keep the existing NoPack profile as the zero/default configuration.
	commandCount, notificationCount := uint64(currentChannelCommandPacketCount), uint64(currentChannelNotificationPacketCount)
	switch strings.ToLower(strings.TrimSpace(s.options.currentClientVersion)) {
	case "", "90cn":
	case "official-24.9":
		commandCount, notificationCount = official249CommandPacketCount, official249NotificationPacketCount
	default:
		return nil, fmt.Errorf("%w: unsupported current client version", errResidentChannelRange)
	}
	serverID := s.channelAdvertiseID()
	if serverID < 0 || serverID > math.MaxInt32 ||
		channel.ID <= 0 || channel.ID > math.MaxInt32 {
		return nil, fmt.Errorf("%w: server=%d channel=%d type=%d", errResidentChannelRange, serverID, channel.ID, channel.Type)
	}
	if channel.Port <= 0 || channel.Port > math.MaxInt32 ||
		s.options.initialUDPPort1 < 0 || s.options.initialUDPPort1 > math.MaxInt32 ||
		s.options.initialUDPPort2 < 0 || s.options.initialUDPPort2 > math.MaxInt32 ||
		now.Unix() <= 0 {
		return nil, fmt.Errorf("%w: port=%d udp=%d/%d time=%d",
			errResidentChannelRange,
			channel.Port,
			s.options.initialUDPPort1,
			s.options.initialUDPPort2,
			now.Unix())
	}

	protobuf := make([]byte, 0, 96)
	protobuf = appendProtoVarint(protobuf, 1, currentChannelInfoEnum)
	protobuf = appendProtoVarint(protobuf, 2, currentChannelInfoSuccess)
	protobuf = appendProtoBytes(protobuf, 4, []byte(noticeChanName(channel)))
	protobuf = appendProtoVarint(protobuf, 7, uint64(serverID))
	protobuf = appendProtoVarint(protobuf, 8, uint64(channel.ID))
	protobuf = appendProtoVarint(protobuf, 9, currentChannelResidentControllerIndex)
	protobuf = appendProtoVarint(protobuf, 11, uint64(now.Unix()))
	if s.options.serverIP != "" {
		protobuf = appendProtoBytes(protobuf, 12, []byte(s.options.serverIP))
	}
	protobuf = appendProtoVarint(protobuf, 13, uint64(s.options.initialUDPPort1))
	protobuf = appendProtoVarint(protobuf, 14, uint64(s.options.initialUDPPort2))
	protobuf = appendProtoVarint(protobuf, 30004, commandCount)
	protobuf = appendProtoVarint(protobuf, 30005, notificationCount)

	var writer packetWriter
	writer.writeUint32(uint32(len(protobuf)))
	writer.writeBytes(protobuf)
	return writer.bytes(), nil
}

func (s *Service) sendCurrentChannelResidentNotice(session *gameSession, now time.Time) error {
	if session == nil {
		return errResidentChannelUnavailable
	}
	session.endpointHandshakeMu.Lock()
	defer session.endpointHandshakeMu.Unlock()
	if session.currentChannelResidentNoticeSent {
		s.logGameEvent(session, "game-current-channel-notice-duplicate-suppressed",
			"current_channel_id", session.channel.ID,
			"reason", "one_notice_per_tcp_session")
		return nil
	}
	current := session.channel
	// 2026-09-27：常驻公告（op1 CHANNELINFO）同样必须与场景频道一致。
	// 客户端 trace 现场：mode0-owner-compare scene_channel=252 local_owner_channel=10
	// 无限刷屏（渲染被拖死、占位面板常驻）。上一处 scene-object owner 已改为真实频道，
	// 这里把公告用的频道也一并对齐；常驻/引导频道会话保持旧语义。
	//
	// 2026-09-27 第二轮（频道 508 崩溃）：会话真实频道 > 255 时 owner 字段装不下它，
	// 于是 owner 只能取 catalog 落点频道（currentCommittedChannelOwner 的既有 ② 分支）。
	// 公告必须取**同一个**落点频道：如果还回显 508，客户端场景频道（508）与下发的 owner
	// 永远不可能一致 ⇒ `branch=remote-actor` ⇒ 访问违例。这里同时写具名事件。
	ownerSource := "connected_channel"
	substitutedFrom := 0
	if currentOwnerChannelOversized(session.channel.ID) {
		landing, landingOK := currentOwnerChannelFieldValue(session.residentChannel.ID)
		if !landingOK {
			return fmt.Errorf("%w: connected=%d resident=%d",
				errResidentChannelRange,
				session.channel.ID,
				session.residentChannel.ID)
		}
		current = session.residentChannel
		ownerSource = "catalog_resident_landing"
		substitutedFrom = session.channel.ID
		s.logOwnerChannelSubstitution(session, "game_current_channel_notice", int(landing))
	}
	body, err := s.buildCurrentChannelResidentNotice(current, now)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-current-channel-notice-send",
		"source_server_id", current.ServerID,
		"advertise_server_id", s.channelAdvertiseID(),
		"current_channel_id", current.ID,
		"current_channel_type", current.Type,
		"controller_index", currentChannelResidentControllerIndex,
		"current_channel_group", current.Group,
		"owner_channel_source", ownerSource,
		"connected_channel_id", session.channel.ID,
		"substituted_from_channel", substitutedFrom,
		"msg_id", 1,
		"classification", 0,
		"transport", "game_upper",
		"header_size", s.gameUpperHeaderSize(),
		"body_len", len(body),
		"protobuf_len", len(body)-4,
		"body_source", "current_exe_sub_1D70130_pb_enum_notipacket_channelinfo")
	if err := s.sendGameUpperRawClass(session, 1, body, 0); err != nil {
		return err
	}
	session.currentChannelResidentNoticeSent = true
	return nil
}
