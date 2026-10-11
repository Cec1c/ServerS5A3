package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 本文件是操作者缺陷「双人组队正常通关后 翻牌奖励双方都看不到对方翻的牌」的修复：
// **把每一名成员翻开的牌投影给同一队伍副本实例内的其它成员**。
//
// ── 现状（取证）──────────────────────────────────────────────────────────────
//
//  1. 服务端只在**请求方自己的会话**上回 op71（`SelectCard` 的翻牌回复）：
//     `dungeon_card_reward_runtime.go:366` 用 `session`（请求方）发包，投影体由
//     `currentDungeonSelectedCardSlots(runtime)` 从**请求方自己的 runtime** 现算，
//     而队友的翻开状态根本不在他的 runtime 里 ⇒ 队友那一侧永远是"未翻开"。
//  2. 现场 `packet_log.txt`（2026-09-28，双开 game-000021 char6 / game-000024 char14）：
//     全场**没有任何一条**发给对手方的翻牌包（`msg_id=71/34/35/279/72` 对这两个 conn
//     零出现；两人各只有一条 `game-dungeon-card-reward-plan-frozen`）——即
//     "从来没发过"，不是"发了但对方忽略"。
//  3. 线格式本身容得下队友的牌：op70 是 8 个 u16 槽、op71 是 8 个 slot，
//     `dungeonCardSlotsPerSide = 4` 槽**每槽一个成员**（`StateA` = 免费行、
//     `StateB` = 付费行）——这正是厂商 `CardRewardNotificationSender.BuildCardInfoAck`
//     的形状（`for index 0..7`、`run.FreeCardSlots[index]`/`run.PaidCardSlots[index]`，
//     每槽 (free, paid) 一对）。厂商那份 fork 在 `index != 0` 时写死 `FF FF 00 00`
//     （自家 stub，队友槽位从不投影），所以本仓**不照抄那个 stub**，而是按同一份
//     已证线格式把每个成员的槽位都投影出来。
//
// ── 做法 ─────────────────────────────────────────────────────────────────────
//
// 翻牌那一刻（`selectCurrentDungeonCardLocked`）：
//
//	① 把"哪一行(side)、哪一槽(memberIndex)、谁翻的、翻出的牌面(reward tuples)"
//	   记进队伍副本实例的账本（`dungeonPartyInstance.cardFlipKnown/cardFlipSlots`）；
//	② 请求方自己的 op71 投影在既有本地槽位之上叠加**账本里的其它成员槽位**
//	   （`currentDungeonSelectedCardSlots` 的叠加，见本文件 `currentDungeonPartyCardFlipSlots`）；
//	③ 把同一份账本投影发给实例内的其它成员会话（与死亡通知广播同一条既有通路：
//	   直接对队友会话发包，**不去锁队友的 session.dungeon.mu**，避免锁序倒置）。
//
// 于是任意时刻每个成员看到的槽位 = 全体成员已经翻开的行 ⇒ 双方都能看到对方翻的牌。
// 单人/无队伍（未进实例）时账本不存在，投影与发包路径逐字节等于改动前。
//
// ── 开关 ─────────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_PARTY_CARD_FLIP_PROJECTION
//
//	vendor（默认，= 修复）：按上面三步投影队友的翻牌。
//	legacy（一键回退）    ：**一条包都不发、一个字节都不记**，逐字节等于改动前。
//
// 默认 = 修复档（本仓纪律：默认即修复行为，回退档必须显式写 legacy）。
const dungeonPartyCardFlipProjectionEnv = "DNFBRIDGE_PARTY_CARD_FLIP_PROJECTION"

// dungeonPartyCardFlipProjectionLegacyValue 是唯一被承认的回退取值。
const dungeonPartyCardFlipProjectionLegacyValue = "legacy"

type dungeonPartyCardFlipProjectionMode int

const (
	// dungeonPartyCardFlipProjectionVendor 是默认档，也是修复档（零值 = 修复）。
	dungeonPartyCardFlipProjectionVendor dungeonPartyCardFlipProjectionMode = iota
	// dungeonPartyCardFlipProjectionLegacy 是改动前的"只回请求方"档。
	dungeonPartyCardFlipProjectionLegacy
)

func readDungeonPartyCardFlipProjectionMode(value string) dungeonPartyCardFlipProjectionMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonPartyCardFlipProjectionLegacyValue) {
		return dungeonPartyCardFlipProjectionLegacy
	}
	return dungeonPartyCardFlipProjectionVendor
}

func (mode dungeonPartyCardFlipProjectionMode) String() string {
	if mode == dungeonPartyCardFlipProjectionLegacy {
		return dungeonPartyCardFlipProjectionLegacyValue
	}
	return "vendor"
}

// projects 报告该档位是否把队友的翻牌投影出去。
func (mode dungeonPartyCardFlipProjectionMode) projects() bool {
	return mode == dungeonPartyCardFlipProjectionVendor
}

func dungeonPartyCardFlipProjectionModeFromEnv() dungeonPartyCardFlipProjectionMode {
	return readDungeonPartyCardFlipProjectionMode(os.Getenv(dungeonPartyCardFlipProjectionEnv))
}

// recordDungeonPartyCardFlip 把一次翻牌记进实例账本。调用方持有实例锁。
func (instance *dungeonPartyInstance) recordDungeonPartyCardFlip(
	side dungeonCardSide,
	memberIndex byte,
	characterID uint16,
	stateA byte,
	stateB byte,
	rewards []currentDungeonOp71RewardTuple,
) {
	if instance == nil || side >= dungeonCardSideCount || memberIndex >= dungeonCardSlotsPerSide {
		return
	}
	slot := instance.cardFlipSlots[memberIndex]
	if side == dungeonCardSideFree {
		slot.StateA = stateA
		// 每个牌位只有一个奖励块 ⇒ 免费行的内容只在"该牌位的付费行还不知道"时写入；
		// 付费行随后翻开会覆盖它（与本会话自己的投影同一条"付费行优先"规则）。
		// 回退档下这一句不生效（免费行内容永远为空，与改动前逐字节相同）。
		// 见 dungeon_party_card_free_row_reward.go。
		if dungeonPartyCardFreeRowRewardEnabled() &&
			!instance.cardFlipKnown[dungeonCardSidePaid][memberIndex] {
			slot.Rewards = append([]currentDungeonOp71RewardTuple(nil), rewards...)
		}
	} else {
		slot.StateB = stateB
		slot.Rewards = append([]currentDungeonOp71RewardTuple(nil), rewards...)
	}
	instance.cardFlipSlots[memberIndex] = slot
	instance.cardFlipKnown[side][memberIndex] = true
	instance.cardFlipOwner[side][memberIndex] = characterID
}

// currentDungeonPartyCardFlipSlots 把实例账本投影成一份 op71 槽位表（未翻开的槽位保持
// `FF FF`）。账本为空（单人/无实例/回退档）时返回零值表，因此调用方的叠加是空操作。
func currentDungeonPartyCardFlipSlots(instance *dungeonPartyInstance) [dungeonCardWireSlotCount]currentDungeonOp71Slot {
	var slots [dungeonCardWireSlotCount]currentDungeonOp71Slot
	for index := range slots {
		slots[index] = currentDungeonOp71Slot{StateA: 0xff, StateB: 0xff}
	}
	if instance == nil {
		return slots
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	for memberIndex := 0; memberIndex < dungeonCardSlotsPerSide; memberIndex++ {
		if !instance.cardFlipKnown[dungeonCardSideFree][memberIndex] &&
			!instance.cardFlipKnown[dungeonCardSidePaid][memberIndex] {
			continue
		}
		slot := slots[memberIndex]
		if instance.cardFlipKnown[dungeonCardSideFree][memberIndex] {
			slot.StateA = instance.cardFlipSlots[memberIndex].StateA
		}
		// 奖励块：付费行优先；只有免费行被翻开时，账本里存的就是免费行自己的内容
		// （写入侧已经保证"付费行一旦已知就覆盖它"）—— 这里必须一起带出去，
		// 否则队友那一侧看到的仍然是一张空牌面（蓝色 X）。见
		// dungeon_party_card_free_row_reward.go。
		paidKnown := instance.cardFlipKnown[dungeonCardSidePaid][memberIndex]
		if paidKnown {
			slot.StateB = instance.cardFlipSlots[memberIndex].StateB
		}
		if paidKnown || (dungeonPartyCardFreeRowRewardEnabled() &&
			instance.cardFlipKnown[dungeonCardSideFree][memberIndex]) {
			slot.Rewards = append([]currentDungeonOp71RewardTuple(nil), instance.cardFlipSlots[memberIndex].Rewards...)
		}
		slots[memberIndex] = slot
	}
	return slots
}

// overlayCurrentDungeonPartyCardFlipSlots 把队友已经翻开的槽位并进本地投影：
// 本地已翻开的行（`state != 0xff`）优先，其余按账本补齐。账本为空时**逐字段不变**。
func overlayCurrentDungeonPartyCardFlipSlots(
	local [dungeonCardWireSlotCount]currentDungeonOp71Slot,
	instance *dungeonPartyInstance,
) [dungeonCardWireSlotCount]currentDungeonOp71Slot {
	if instance == nil {
		return local
	}
	peer := currentDungeonPartyCardFlipSlots(instance)
	merged := local
	for index := range merged {
		if merged[index].StateA == 0xff && peer[index].StateA != 0xff {
			merged[index].StateA = peer[index].StateA
			if merged[index].StateB == 0xff && peer[index].StateB == 0xff {
				merged[index].Rewards = peer[index].Rewards
			}
		}
		if merged[index].StateB == 0xff && peer[index].StateB != 0xff {
			merged[index].StateB = peer[index].StateB
			merged[index].Rewards = peer[index].Rewards
		}
	}
	return merged
}

// publishCurrentDungeonCardFlipToPartyLocked 记下本次翻牌并把账本投影发给同一队伍副本
// 实例内的其它成员会话。返回送达的队友数。
//
// 只发 op71（`CmdPacketSelectCard`，与请求方收到的是**同一份已证形状**），不发明新包；
// 不锁队友的 session.dungeon.mu（沿用死亡通知广播的既有并发口径）。
// 调用方持有请求方的 session.dungeon.mu。
func (s *Service) publishCurrentDungeonCardFlipToPartyLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	side dungeonCardSide,
	memberIndex byte,
	stateA byte,
	stateB byte,
	rewards []currentDungeonOp71RewardTuple,
	source string,
) int {
	if s == nil || session == nil || runtime == nil || runtime.partyInstance == nil {
		return 0
	}
	mode := dungeonPartyCardFlipProjectionModeFromEnv()
	if !mode.projects() {
		return 0
	}
	instance := runtime.partyInstance
	selfCharacterID := session.selectedCharacterID
	func() {
		instance.mu.Lock()
		defer instance.mu.Unlock()
		instance.recordDungeonPartyCardFlip(side, memberIndex, selfCharacterID, stateA, stateB, rewards)
	}()
	slots71 := currentDungeonPartyCardFlipSlots(instance)
	body, err := buildCurrentDungeonOp71SuccessBody(slots71)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-card-flip-party-broadcast-failed",
			"char_id", selfCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"instance_id", instance.id,
			"side", side,
			"member_index", memberIndex,
			"source", source,
			"reason", "op71_projection_body_invalid",
			"error", err)
		return 0
	}
	peers := s.partyDungeonInstancePeers(runtime, selfCharacterID)
	delivered := 0
	for _, peer := range peers {
		if peer.Session == nil {
			continue
		}
		if err := s.sendGameUpperRawClass(
			peer.Session,
			uint16(dnfenum.CmdPacketSelectCard),
			append([]byte(nil), body...),
			currentDungeonCardResponseClass,
		); err != nil {
			s.logGameEvent(session, "game-dungeon-card-flip-party-broadcast-failed",
				"char_id", selfCharacterID,
				"peer_char_id", peer.CharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"instance_id", instance.id,
				"side", side,
				"member_index", memberIndex,
				"source", source,
				"error", err)
			continue
		}
		delivered++
	}
	// 这一行是"下一次实测一眼可判"的那一条：点名队伍实例、成员、翻牌人、翻的行/槽位、
	// 牌面与广播判定。
	s.logGameEvent(session, "game-dungeon-card-flip-party-broadcast",
		"char_id", selfCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"instance_id", instance.id,
		"side", side,
		"member_index", memberIndex,
		"card_index", memberIndex,
		"state_a", stateA,
		"state_b", stateB,
		"flip_party_slot", currentDungeonCardFlipRowStateValue(session),
		"row_model", dungeonPartyCardFlipRowModelFromEnv().String(),
		"row_model_env", dungeonPartyCardFlipRowModelEnv,
		"reward_tuple_count", len(rewards),
		"party_member_count", len(instance.members),
		"peer_count", len(peers),
		"delivered", delivered,
		"source", source,
		"switch", mode.String(),
		"response_opcode", uint16(dnfenum.CmdPacketSelectCard),
		"response_body_len", len(body),
		"response", "current_exe_op71_projection_of_every_members_flipped_row")
	return delivered
}
