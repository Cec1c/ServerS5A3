package dnfbridge

import (
	"math"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// 本文件是"频道号 → u8 owner 字段"的**唯一**转换点。
//
// # 为什么必须存在
//
// 协议里 owner 频道字段是 **u8**（`u8 town actor owner field`，见
// game_bootstrap.go:24-27）：op2 mode0 的 body[4]、mode1 的 body[4]、op9 的 body[7]、
// 聊天通知里的频道字节都是这一个字节。而频道目录里**存在 id > 255 的频道** ——
// 不是例外，是一整族：`data/dnf/channel_info.etc` 的 `[deathtower]` 508（type 22）、
// 261..263（type 42）、501..507（type 24）。客户端 trace 的
// `channel runtime load server=0 channel=508 ... type=22` 逐条列出了它们。
//
// 把 508 直接 `byte(508)` 会得到 **252** —— 那不是一个"近似频道"，而是一个**别的
// 频道号**。客户端拿到 owner=252 却知道自己场景频道是 508，于是走
// `mode0-owner-compare scene_channel=508 local_owner_channel=252 branch=remote-actor`，
// 把自己的角色当成"跨频道远端 actor"再访问违例崩溃：
//
//	2026-09-27 18:32:13 / 18:35:26 / 18:41:20 / 18:44:35 / 18:46:38 / 18:52:07
//	exception code=0xC0000005 address=020092A9 eax=0x000000FC ecx=0x000000FC
//
// （六个 `DNF_DUMP*.zip`；`0xFC` = 252 = `byte(508)`。）
//
// 同一份 trace 里还有另一条同类事故，证明"场景频道 ≠ owner 频道"本身就是故障：
// 224164 行 `mode0-owner-compare scene_channel=252 local_owner_channel=10
// branch=remote-actor`（2026-09-27 03:41:53–03:42:09，16 秒刷满 22 万行，
// 客户端渲染被拖死 = 操作者报的"卡死"）。
//
// # 规则只有两条
//
//  1. 装得下（1..255）→ 原样返回，**语义与历史逐字节相同**；
//  2. 装不下 → **绝不截断**：回退到 catalog 的落点频道（resident，恒 ≤255），
//     并且调用点必须写具名事件（`game-channel-owner-substituted`）；连落点都装不下
//     → `ok=false`，调用方走显式失败/回退 context 0，不得静默降级。
const (
	// currentOwnerChannelByteLimit 是 owner 频道字段能表达的频道号上限（u8）。
	currentOwnerChannelByteLimit = math.MaxUint8

	// currentOwnerChannelSubstitutedEvent 是"会话真实频道装不进 u8 owner 字段 ⇒
	// owner/公告改用 catalog 落点频道"的具名事件。固定字段：
	// connected_channel / substituted_channel / resident_channel / reason / call_site。
	currentOwnerChannelSubstitutedEvent = "game-channel-owner-substituted"

	// currentOwnerChannelSubstitutionReason 是回退原因（会话频道 > 255）。
	currentOwnerChannelSubstitutionReason = "connected_channel_exceeds_u8_owner_field"

	// currentOwnerChannelNoLandingReason 是"连落点都装不下"的原因（显式失败路径）。
	currentOwnerChannelNoLandingReason = "no_u8_landing_channel_in_catalog"

	// channelDirectoryOwnerRangeGateEvent 是"频道目录按 owner 字段范围裁剪"的具名事件
	// （只在 DNFBRIDGE_CHANNEL_DIRECTORY_OWNER_RANGE 打开时出现）。
	channelDirectoryOwnerRangeGateEvent = "channel-advertise-owner-range-gate"

	// channelDirectoryOwnerRangeGateEnv 打开后，下发给客户端的频道目录里不再包含
	// id > 255 的频道 —— 这是服务端**唯一**能阻止客户端把 >255 频道当成落点的杠杆
	// （客户端只从这份目录 + 它自己的本地记忆里选频道，包内 CHANNELINFO 改不动它）。
	// 默认关闭：默认档下发的目录逐字节不变。
	channelDirectoryOwnerRangeGateEnv = "DNFBRIDGE_CHANNEL_DIRECTORY_OWNER_RANGE"
)

// currentOwnerChannelFieldValue 是 int 频道号 → u8 owner 字段的**唯一**转换点。
// ok=false 表示该频道号装不进 owner 字段：调用方必须回退到落点频道或显式失败，
// **不得**继续用 `byte(channelID)` 这种静默截断。
func currentOwnerChannelFieldValue(channelID int) (byte, bool) {
	if channelID <= 0 || channelID > currentOwnerChannelByteLimit {
		return currentSceneObjectContext, false
	}
	return byte(channelID), true
}

// currentOwnerChannelOversized 报告会话真实频道是否装不进 u8 owner 字段。
// 这正是"必须回退到落点频道并写具名事件"的触发条件。
func currentOwnerChannelOversized(channelID int) bool {
	_, ok := currentOwnerChannelFieldValue(channelID)
	return !ok
}

// currentSessionOwnerChannelField 返回"本会话可以直接写进 u8 owner 字段的频道字节"：
// 会话真实频道装得下就用它，否则用 catalog 落点频道（resident）。ok=false 表示
// 两者都装不下 —— 此时 owner 字段无法表达该会话，调用方必须显式失败或回退 context 0。
func currentSessionOwnerChannelField(session *gameSession) (byte, bool) {
	if session == nil {
		return currentSceneObjectContext, true
	}
	if owner, ok := currentOwnerChannelFieldValue(session.channel.ID); ok {
		return owner, true
	}
	if owner, ok := currentOwnerChannelFieldValue(session.residentChannel.ID); ok {
		return owner, true
	}
	return currentSceneObjectContext, false
}

// currentSessionOwnerChannelSubstitution 返回本会话的"owner 频道回退描述"：
// substituted=true 时 owner 是落点频道而不是会话真实频道（会话频道 > 255）。
// ok=false 表示连落点都装不下。它只做判定，不写日志（日志由持有 *Service 的调用点写）。
func currentSessionOwnerChannelSubstitution(session *gameSession) (owner byte, substituted bool, ok bool) {
	if session == nil {
		return currentSceneObjectContext, false, true
	}
	if sessionOwner, fieldOK := currentOwnerChannelFieldValue(session.channel.ID); fieldOK {
		return sessionOwner, false, true
	}
	if landing, landingOK := currentOwnerChannelFieldValue(session.residentChannel.ID); landingOK {
		return landing, true, true
	}
	return currentSceneObjectContext, true, false
}

// currentOwnerChannelSubstitutionFields 生成具名事件的固定字段集，
// 让"哪个调用点、从哪个超大频道、回退到哪个落点频道"在同一行里可判读。
func currentOwnerChannelSubstitutionFields(session *gameSession, callSite string, substitutedChannel int) []any {
	connected := 0
	resident := 0
	if session != nil {
		connected = session.channel.ID
		resident = session.residentChannel.ID
	}
	return []any{
		"connected_channel", connected,
		"substituted_channel", substitutedChannel,
		"resident_channel", resident,
		"owner_channel_limit", currentOwnerChannelByteLimit,
		"reason", currentOwnerChannelSubstitutionReason,
		"call_site", callSite,
	}
}

// logOwnerChannelSubstitution 写"会话频道超范围 ⇒ owner 改用落点频道"的具名事件。
// 只在确实发生回退（会话频道 > 255 且落点频道装得下）时调用。
func (s *Service) logOwnerChannelSubstitution(session *gameSession, callSite string, substitutedChannel int) {
	if s == nil || session == nil {
		return
	}
	if _, ok := currentOwnerChannelFieldValue(session.channel.ID); ok {
		return
	}
	s.logGameEvent(session, currentOwnerChannelSubstitutedEvent,
		currentOwnerChannelSubstitutionFields(session, callSite, substitutedChannel)...)
}

// logOwnerChannelUnrepresentable 写"连落点频道都装不下 owner 字段"的具名事件：
// 这条路径**必须**显式失败或回退 context 0，不允许静默发一个截断值。
func (s *Service) logOwnerChannelUnrepresentable(session *gameSession, callSite string, fallback string) {
	if s == nil || session == nil {
		return
	}
	if _, ok := currentOwnerChannelFieldValue(session.channel.ID); ok {
		return
	}
	if _, ok := currentOwnerChannelFieldValue(session.residentChannel.ID); ok {
		return
	}
	fields := currentOwnerChannelSubstitutionFields(session, callSite, currentSceneObjectContext)
	fields = append(fields,
		"reason", currentOwnerChannelNoLandingReason,
		"fallback", fallback)
	s.logGameEvent(session, currentOwnerChannelSubstitutedEvent, fields...)
}

// filterChannelDirectoryOwnerRange 按 u8 owner 字段范围裁剪频道目录：
// 返回保留下来的频道与被裁掉的频道号（升序）。只在显式开关打开时使用。
func filterChannelDirectoryOwnerRange(channels []channelcatalog.Channel) ([]channelcatalog.Channel, []int) {
	if len(channels) == 0 {
		return channels, nil
	}
	kept := make([]channelcatalog.Channel, 0, len(channels))
	var dropped []int
	for _, channel := range channels {
		if currentOwnerChannelOversized(channel.ID) {
			dropped = append(dropped, channel.ID)
			continue
		}
		kept = append(kept, channel)
	}
	return kept, dropped
}

// channelDirectoryOwnerRangeGateEnabled 报告是否打开"频道目录按 u8 owner 字段范围裁剪"。
//
// 默认**关闭**：默认档下发的频道目录逐字节不变。
//
// 打开（`DNFBRIDGE_CHANNEL_DIRECTORY_OWNER_RANGE=u8`）后，id > 255 的频道不再出现在
// 下发给客户端的目录里 —— 这是服务端**唯一**能阻止客户端把 508 这类频道当落点的杠杆：
// 客户端只从"服务端给的目录 + 它自己的本地记忆（DNF.cfg）"里选频道，包内 class0/op1
// CHANNELINFO **改不动已经连上来的会话**（现场证据见交付报告 ③）。
func channelDirectoryOwnerRangeGateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(channelDirectoryOwnerRangeGateEnv))) {
	case "u8", "on", "1", "true", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}
