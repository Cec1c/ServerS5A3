package dnfbridge

import (
	"os"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	defaultCharacterSlots        = dnfrepo.DefaultCharacterSlotLimit
	noPackRosterWireSlotLimit    = defaultCharacterSlots
	latestRosterRouteNormal      = 0
	latestRosterContextNormal    = 0
	currentSceneObjectRoute      = 0
	currentSceneObjectContext    = 0
	upperMaxCharacter            = 0xffff
	latestCharacterStateRoute    = 1
	latestCharacterCreateEnabled = 1
	latestCharacterStateActive   = 1
	latestCharacterStateNormal   = 0
	rosterNameMaxBytes           = 30
	rosterWideNameUnits          = 31
	noPackRosterPreJobBytes      = 2
	noPackRosterPostGrowBytes    = 2
	rosterLinkedIDBlockSize      = 8
	// Current sub_20026C0 consumes a mandatory type-44 raw block after every
	// equipment item ID. Even an empty block has its u32 length prefix.
	noPackRosterEquipRowBytes  = 19
	noPackRosterPostEquipBytes = 38
	noPackRosterEntryTailBytes = noPackRosterPreJobBytes + 1 + 1 + noPackRosterPostGrowBytes + 1 + 4 + 4 + 1 + noPackRosterPostEquipBytes
	rosterDefaultPageCount     = 1
	createCodeDuplicated       = 0x00
	createCodeSlotFull         = 0x04
	createCodeServerError      = 0x14
)

func currentConnectionTownActorOwnerContext(session *gameSession) byte {
	if session == nil {
		return currentSceneObjectContext
	}
	if currentOwnerContextUsesClientChannel() {
		// 2026-09-27 线上（`DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL=on`）：
		// 客户端落在频道 508 时，旧写法 `byte(session.channel.ID)` = **252**，
		// 客户端场景频道是 508 ⇒ `mode0-owner-compare ... branch=remote-actor`
		// ⇒ 0xC0000005 崩溃（eax=ecx=0xFC）。这里改成"只有 u8 装得下才允许对齐"：
		// 装不下就沿用会话已提交的 owner（bootstrap 已按 catalog 落点频道提交并写了
		// `game-channel-owner-substituted` 事件），**绝不截断**。
		// 见 channel_owner_range.go。
		if channelID, ok := currentOwnerChannelFieldValue(session.channel.ID); ok && channelID != 0 {
			return channelID
		}
	}
	return session.connectionTownActorOwnerChannel
}

func currentTownActorOwnerContext(session *gameSession) byte {
	if session == nil {
		return currentSceneObjectContext
	}
	if currentOwnerContextUsesClientChannel() {
		// 同 currentConnectionTownActorOwnerContext：会话频道 > 255 时不再截断，
		// 沿用会话已提交的 owner（= catalog 落点频道，恒 ≤255）。
		if channelID, ok := currentOwnerChannelFieldValue(session.channel.ID); ok && channelID != 0 {
			return channelID
		}
	}
	return session.townActorOwnerChannel
}

// currentClientSelectedChannel 返回"客户端当前所在频道"的数值。
//
// class0/op9 的**读端**是拿记录第 7 字节（owner context）与客户端当前频道比较来决定
// 每个成员行后面有没有内联身份块（`u8 job / u8 level / u8 grow / u8 flag /
// u32 nameLen + raw name`）的：两边不同 → 按"跨频道成员"读身份块（IDA：
// `sub_1D64CA0` 的 kind0 记录 + `sub_269EE50`）。所以**服务端组包时必须按同一个参照值**
// 决定是否写身份块。
//
// 不能用 `townActorOwnerChannel` 当参照值：进副本时本地 town 上下文被置 0，而客户端
// 频道仍是登录频道（现场 2026-09-24 22:25：记录 owner=0、客户端频道 10/121）。
// 也不能拿 owner 和自己比（那样这个分支永远不命中）——2026-09-24 双开进图 50% 闪退
// 就是这么来的：客户端按跨频道读身份块，nameLength 读成 0x00000301 后越界。
//
// 2026-09-27：**返回值是 u8 投影，只用于"owner 字节"侧**。会话频道 > 255 时不存在任何
// 能等于客户端场景频道的字节（客户端场景频道 = 508 这种大数），所以这里返回 context 0
// = 不可表达，让调用方走显式失败/回退（旧写法 `byte(508)` = 252 会冒充成"频道 252"，
// 客户端直接崩溃）。要判"记录 owner 与客户端频道是否一致"请用
// currentClientSelectedChannelID（int，不截断）。
func currentClientSelectedChannel(session *gameSession) byte {
	if session == nil {
		return currentSceneObjectContext
	}
	if channelID, ok := currentOwnerChannelFieldValue(session.channel.ID); ok {
		return channelID
	}
	if currentOwnerChannelOversized(session.channel.ID) {
		return currentSceneObjectContext
	}
	// 频道还没绑定时退回连接期记录的频道值（选择角色页等 0 槽位记录不受影响）。
	return currentConnectionTownActorOwnerContext(session)
}

// currentClientSelectedChannelID 返回**未截断**的"客户端当前频道号"，供
// "记录 owner 字节 vs 客户端频道是否一致"这类判定使用（op9 kind0 成员行的内联身份块）。
//
// 为什么必须是 int：客户端自己比较时用的是它的场景频道（可能是 508 这种 >255 的值）。
// 只要它 > 255，任何 u8 记录字节都不可能相等 ⇒ 那些成员行必须一律带身份块。
// 截断成 252 会让服务端在这个判据上"蒙对或蒙错"，而蒙错就是 2026-09-24 那条
// "按跨频道读身份块、nameLength 越界"的闪退。
func currentClientSelectedChannelID(session *gameSession) int {
	if session == nil {
		return currentSceneObjectContext
	}
	if session.channel.ID > 0 {
		return session.channel.ID
	}
	return int(currentConnectionTownActorOwnerContext(session))
}

// currentOwnerContextUsesClientChannel 控制"记录第 7 字节（owner context）"是否
// 强制等于**客户端当前频道**。
//
// 2026-09-27 现场（客户端 trace 无限刷）：
//
//	mode0-owner-compare scene_channel=252 local_owner_channel=10 branch=remote-actor
//
// 读端（class0/op9）拿 owner context 与客户端当前频道比较；不一致就一直重试，
// 渲染被拖死、占位面板常驻 —— 即操作者看到的"弹窗 + 卡死"。
//
// 默认**关闭**（保持既有契约与用例不变）；线上用
// DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL=on 打开对齐。
const ownerContextUseClientChannelEnv = "DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL"

func currentOwnerContextUsesClientChannel() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ownerContextUseClientChannelEnv))) {
	case "on", "1", "true", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}
