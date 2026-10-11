package dnfbridge

import "strings"

// 复活通知：class-0 / msg 32（`ENUM_NOTIPACKET_DIE_STATE`，线号 0x0020）的 **state=1** 形态。
//
// ── 为什么必须有这一条 ────────────────────────────────────────────────────
//
// 改动前 `handleDungeonUseCoin` 成功之后只做两件事：扣币 + `completeCurrentDungeonReviveLocked`
// （那支函数体只有一句"取消死亡计时器"，它自己的注释就写着
// "the cancellation boundary for a **real future revive transaction**"）。也就是说：
// **币扣掉了、计时器也取消了，但没有任何一条包告诉客户端"角色已经活了"** ——
// 客户端会一直停在死亡态，现场表现就是"复活币用不了"。
//
// 厂商 `Network\Handlers\Dungeon\DungeonCombatHandler.cs:593-599`（op41 成功之后、
// 发 ACK 之前）：
//
//	// 1. NOTI 0x0020 DIE_STATE: set_charac_live(user, 1=revive)
//	//    df_game_r body = u16 actorId + u8 state; 86JP has extra u8 flag
//	var noti = new GamePacketWriter();
//	noti.WriteUInt16(targetId);
//	noti.WriteByte(0x01);  // state=1 revive
//	noti.WriteByte(0x00);  // 86JP flag
//	await session.SendPacketAsync(GamePacketEnvelopeBuilder.Build(0x00, 0x0020, noti.ToArray()));
//
// 这与**死亡**那条是同一个 opcode/同一种布局（本仓 `buildCurrentDungeonDeathStateBody`
// 已经是 `u16 + u8 + u8`），只有中间那个字节从 0（death）变成 1（revive）。
// 本仓死亡路径发的是 `buildCurrentDungeonDeathStateBody(key, 0, 0)`，复活路径此前**根本没发**。
//
// 厂商发的顺序是 **先 NOTI、后 ACK**，本文件按同一顺序接在 `completeCurrentDungeonReviveLocked`
// 之后、`sendGameUpperSuccess` 之前。
//
// ── 开关 ──────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_REVIVE_STATE_NOTIFY
//
//	vendor（默认，= 修复档）：op41 成功后先发 class-0/32 的 state=1 通知。
//	legacy（一键回退）      ：逐字节回到改动前 —— **完全不发**这条通知。
//
// 与 `DNFBRIDGE_HELL_ROOM` 同一约定：零值是"修复"，只有显式 `legacy` 才回退。
const reviveStateNotifyEnv = "DNFBRIDGE_REVIVE_STATE_NOTIFY"

const reviveStateNotifyValueLegacy = "legacy"

type reviveStateNotifyMode int

const (
	// reviveStateNotifyVendor 是默认档，也是修复档（零值 = 修复）。
	reviveStateNotifyVendor reviveStateNotifyMode = iota
	// reviveStateNotifyLegacy 是改动前行为（不发复活通知）。
	reviveStateNotifyLegacy
)

// readReviveStateNotifyMode 解析 DNFBRIDGE_REVIVE_STATE_NOTIFY：
// **只有显式 `legacy` 才回退**，其余任何值都取修复档。
func readReviveStateNotifyMode(value string) reviveStateNotifyMode {
	if strings.EqualFold(strings.TrimSpace(value), reviveStateNotifyValueLegacy) {
		return reviveStateNotifyLegacy
	}
	return reviveStateNotifyVendor
}

func (mode reviveStateNotifyMode) String() string {
	if mode == reviveStateNotifyLegacy {
		return reviveStateNotifyValueLegacy
	}
	return "vendor"
}

func (mode reviveStateNotifyMode) sends() bool { return mode == reviveStateNotifyVendor }

// reviveStateNotifyMode 是 DNFBRIDGE_REVIVE_STATE_NOTIFY 的归一化取值。
func (s *Service) reviveStateNotifyMode() reviveStateNotifyMode {
	return s.options.reviveStateNotify
}

// sendCurrentDungeonReviveState 在 op41 成功之后发"角色已复活"通知。
// legacy 档下**一个字节都不发**（返回 nil，调用方控制流不变）。
func (s *Service) sendCurrentDungeonReviveState(session *gameSession, actorObjectKey uint16) error {
	if !s.reviveStateNotifyMode().sends() {
		return nil
	}
	body := buildCurrentDungeonDeathStateBody(actorObjectKey, currentDungeonReviveStateType, 0)
	return s.sendGameUpperRawClass(session, currentDungeonDeathStateMsgID, body, 0)
}
