package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ── 锦标赛（大会族）流程接线：赛制推进 / 通关门 / 包序列 / 选奖事务 ──────────────
//
// # 谁推进赛制（厂商取证，逐行）
//
// 触发源是**客户端上报怪物死亡**，不是 CMD 144、不是定时器、也不是服务端显式观测：
//
//	CmdPacketTypeA21.DIE_MONSTER = 0x0027（= 39）
//	→ Network\Protocol\GameProtocolHandler.cs:708
//	→ Network\Handlers\DungeonHandler.cs:189 Handle_ENUM_CMDPACKET_DIE_MONSTER
//	→ Network\Handlers\Dungeon\DungeonCombatHandler.cs:66 HandleDieMonster
//	  （:72 解析 Network\Parsers\Dungeon\DieMonsterRequest.cs:51 的 u16@0；:165-169 造 killEvent）
//	→ Network\Handlers\Dungeon\DungeonKillApplicationService.cs:170 ProcessAsync
//	→ 同文件 :339 sharedRoomState.InstanceRoom.TryRecordActorDeath(...)
//	→ Game\Dungeon\DungeonInstance.cs:559-593 造 DungeonActorDeathFact（:139-177）
//	→ DungeonKillApplicationService.cs:444 await _tournament.OnActorDeathAsync(session, run, worldDeath.Fact)
//	→ Network\Handlers\Dungeon\TournamentDungeonCoordinator.cs:126 _application.ApplyActorDeath(run, death)
//	→ Game\Dungeon\Tournament\TournamentDungeonApplicationService.cs:128-135 runtime.TryApplyActorDeath(death)
//	→ Game\Dungeon\Tournament\TournamentDungeonRuntime.cs:257-324
//	  （:307-315 第 4 轮全灭 ⇒ Champion；:318 _currentRound++ 进下一轮）
//
// 注意 `TournamentDungeonRuntime.cs:463-488` **不是**轮次循环，那是工厂
// `TournamentDungeonRuntimeFactory.TryCreate` 里**构造** 4 轮对阵表；运行时推进在 :257-324。
//
// # 结论：本仓不需要 CMD 144
//
// `CmdPacketTypeA21.DIE_AI_CHARACTER = 0x0090`（= 144）在厂商全树只出现一次 ——
// `Network\Core\PacketTypesA21.cs:1375` 的枚举声明本身（`Network\Core\PacketTypes.cs:1375` 同）。
// 没有派发表项、没有 parser、没有 handler，也没有任何调用点。厂商没实现 ⇒ 本仓自驱。
//
// 另：`TournamentDungeonRuntime.TryEliminate`（:326-349，玩家死亡淘汰）**有**调用链 ——
// `TournamentDungeonApplicationService.cs:138-144 ApplyElimination` ←
// `TournamentDungeonCoordinator.cs:96 ResolveDeathDeadline` ←
// `DungeonCombatHandler.cs:737 ResolveDeathRespawnDeadlineAsync`（复活计时器/HandleDeathRespawn 到期）。
// 它是"玩家死亡 ⇒ 淘汰"，与轮次推进无关；本批未接线（见交付报告"未做"）。
//
// # 本仓接线点
//
//	轮次推进：`dungeon_combat_runtime.go` 的 op39 击杀路径（`handleDungeonMonsterDeath`），
//	          在权威死亡 / 强制清怪提交之后、最终房间完成之前调用
//	          `syncCurrentTournamentActorDeathsLocked`。选它而不是 op117 那条：
//	          op117（BossDieCheck）只覆盖"终点 Boss 被打死"这一种收尾，而赛制的一轮是
//	          `partyLimit` 只怪全灭；op39 才是每一只怪死亡的唯一权威入口。
//	          （厂商同理：`DungeonCombatHandler.cs:230` 那条 op117 路径只做 WouldClearDungeon 判定。）
//
// 全部入口在默认档都是 no-op：`runtime.Tournament == nil` ⇒ 第一行就返回。

// tournamentRoundAdvanceResult 是本轮同步的结论（供调用方记日志/决定终局动作）。
type tournamentRoundAdvanceResult struct {
	// Accepted 是本次同步里是否有新的 actor 死亡被状态机接受。
	Accepted bool
	// RoundCompleted 是本次同步里是否有轮次完成。
	RoundCompleted bool
	// TournamentCompleted 是本次同步是否把赛制推到终局（冠军）。
	TournamentCompleted bool
	// CompletedRound 是最近一次完成的轮号（1..4，0 = 无）。
	CompletedRound byte
	// CurrentRound 是同步结束时的当前轮号。
	CurrentRound byte
}

// syncCurrentTournamentActorDeathsLocked 把"本房间已经权威死亡的 actor"喂给赛制状态机。
//
// 为什么不是"每来一次 op39 就喂一个 key"：
//   - 本仓 op39 的击杀路径在 Boss 型 actor 死亡时会
//     `forceCurrentDungeonRemainingHostilesForCombatEndLocked` 一次性强制清掉剩余阻挡怪
//     （`dungeon_combat_runtime.go:581-626`，86JP 的 "boss death means combat end" 规则）。
//     若只在 op39 入口喂单个 key，被强制清掉的那几只永远进不了状态机
//     ⇒ 第 4 轮永远凑不齐 ⇒ vendor 档会卡死。
//   - 因此这里改成**幂等全量同步**：每次扫一遍房间的 `defeated` 怪物，按 room 下标顺序喂。
//     `tryApplyActorDeath` 对已接受的 sequence 返回 Duplicate，重复喂没有副作用；
//     即使某轮暂时凑不齐、后续补上，下一次同步也会自愈。
//
// sequence 口径：厂商 `sequenceId` = START_MAP 里公告的 actor id
// （`DungeonInstance.cs:691` `FirstActorSequenceId + index`）。本仓等价物就是
// `runtimeDungeonMonster.ObjectKey`（`dungeon_monster_runtime.go:88-125` 从
// `firstDungeonMonsterObjectKey` 连续递增），`firstSequence` 在
// `dungeon_runtime_state.go` 建房间时绑定成同一批 key 的起点。
//
// 调用方持有 session.dungeon.mu，且 runtime 必须是会话当前 runtime。
func (s *Service) syncCurrentTournamentActorDeathsLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) tournamentRoundAdvanceResult {
	result := tournamentRoundAdvanceResult{}
	if runtime == nil || runtime.Tournament == nil || runtime.Room == nil {
		return result
	}
	definition := runtime.Tournament.Definition
	if definition == nil {
		return result
	}
	snapshot := runtime.Room.Snapshot()
	// 只有锦标赛那一张地图（`[tournament dungeon]` 的 start map）承载 PathActors；
	// 其它房间的死亡与赛制无关，直接不动。
	if snapshot.MapID != definition.MapID {
		return result
	}
	result.CurrentRound = runtime.Tournament.currentRoundNumber()
	for _, monster := range snapshot.Monsters {
		if monster.State != runtimeDungeonMonsterDefeated {
			continue
		}
		transition := runtime.Tournament.tryApplyActorDeath(int(monster.ObjectKey))
		switch {
		case transition.tournamentCompleted():
			result.Accepted = true
			result.RoundCompleted = true
			result.TournamentCompleted = true
			result.CompletedRound = transition.CompletedRound
			result.CurrentRound = transition.CurrentRound
		case transition.roundCompleted():
			result.Accepted = true
			result.RoundCompleted = true
			result.CompletedRound = transition.CompletedRound
			result.CurrentRound = transition.CurrentRound
		case transition.accepted():
			result.Accepted = true
			result.CurrentRound = transition.CurrentRound
		case transition.Disposition == tournamentDeathDuplicate:
			result.CurrentRound = transition.CurrentRound
		}
	}
	if session != nil && result.Accepted {
		s.logGameEvent(session, "game-dungeon-tournament-round-sync",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"map_id", snapshot.MapID,
			"round_completed", result.RoundCompleted,
			"completed_round", result.CompletedRound,
			"current_round", result.CurrentRound,
			"tournament_completed", result.TournamentCompleted,
			"defeated_actors", currentTournamentDefeatedActorCount(snapshot),
			"path_actors", len(runtime.Tournament.PathActors),
			"source", "vendor_die_monster_kill_path_DungeonKillApplicationService.cs:444")
	}
	return result
}

func currentTournamentDefeatedActorCount(snapshot runtimeDungeonRoomSnapshot) int {
	count := 0
	for _, monster := range snapshot.Monsters {
		if monster.State == runtimeDungeonMonsterDefeated {
			count++
		}
	}
	return count
}

// beginCurrentTournamentRewardLocked 在赛制到达终局时冻结参赛者奖励态并下发
// NOTI 389（厂商 `TournamentDungeonCoordinator.cs:159-212 EnsureParticipantRewardsAsync`
// → `:664-679 SendClearRewardAsync`）。
//
// **冠军与被淘汰都建奖励态**：厂商 `EnsureParticipantRewardsAsync` 的第一道门是
// `IsTournamentTerminated`（`:166`），而 `TryCreateParticipantRewards`
// （`TournamentDungeonApplicationService.cs:153-197`）只要求 `runtime.IsTerminated`
// （`:160`），并把 `runtime.IsChampion` 作为 `completedAllRounds` 传给
// `TournamentParticipantRewardState`（`:192`）——即**被淘汰者拿到上排（完成轮数键）
// 与下排 count=0 的失败奖励**（规范第 861 行）。本批修正了 C 批"非冠军直接返回"的
// 早退：淘汰路径同样要冻结奖励态，否则客户端在失败终止后拿不到任何 UI 依据。
//
// 奖池来源**全部来自真档 PVF 或既有 owner**，不发明任何数值：
//   - 金币基数：复用既有 clear-reward 生成器
//     （`dungeon_card_reward_runtime.go:20 freezeCurrentDungeonCardRewardStateLocked` 产出的
//     free bundle Gold，即厂商 `ClearRewardGenerator.GenerateFreeGoldCard` 的等价物），
//     再按厂商 `TournamentDungeonApplicationService.cs:415-431 ScaleTournamentGold`
//     乘 `[tournament clear reward gold rate]`（`DungeonFile.cs:583-584` `ParseFloat`）。
//   - 通关经验：`[tournament clear reward exp]` 里 CompletedRounds 对应值。
//     **P10 已接线**：同一个函数里、389 之前，走
//     `commitCurrentTournamentClearRewardExperienceLocked`
//     （厂商 `TournamentDungeonCoordinator.cs:180-181` → `:607-662`
//     `TryDeliverExperienceAsync` → `CharacterExperienceCommitService.TryCommitTournamentExperience`）。
//     本仓复用**怪物击杀经验那一份**提交实现（`commitCurrentDungeonExperience`），
//     不另写第二套升级/溢出算法；幂等凭据是本次 run 的
//     `run_token + char_id + dungeon_id + completed_rounds`。
//   - 道具：`[reward item rate]` 三行按权重抽。
//   - 空卡：厂商 `CreateEmptyReward()`（`:526-532`，ItemId = -1 / StackCount = 0）。
//
// 任何一步取不到真数据 ⇒ **不下发 389、不建奖励态**，只留具名日志（缺证据就报错，不写死）。
func (s *Service) beginCurrentTournamentRewardLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) {
	if s == nil || session == nil || runtime == nil || runtime.Tournament == nil {
		return
	}
	if runtime.tournamentReward != nil {
		return
	}
	definition := runtime.Tournament.Definition
	if definition == nil {
		return
	}
	snapshot := runtime.Tournament.snapshot()
	if snapshot.Terminal == tournamentActive {
		// 赛制还没终局：没有奖励可言，不建态、不发包。
		return
	}
	champion := snapshot.Terminal == tournamentChampion
	localPartySlot := currentTournamentLocalPartySlot(session)
	if localPartySlot < 0 {
		s.logGameEvent(session, "game-dungeon-tournament-clear-reward-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", snapshot.CompletedRounds,
			"champion", champion,
			"reason", "local_party_slot_is_unresolved")
		return
	}
	goldAmount, goldOK := s.currentTournamentGoldCardAmountLocked(session, runtime)
	cards, failure := rollCurrentTournamentRewardCards(
		definition,
		snapshot.CompletedRounds,
		champion,
		newCurrentDungeonDropLCG(runtime.Seed),
		goldAmount,
		goldOK,
	)
	if failure != "" {
		s.logGameEvent(session, "game-dungeon-tournament-clear-reward-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", snapshot.CompletedRounds,
			"champion", champion,
			"gold_resolved", goldOK,
			"reason", failure)
		return
	}
	experience := definition.RewardExperience[int(snapshot.CompletedRounds)]
	if experience < 0 || experience > 0xFFFFFFFF {
		s.logGameEvent(session, "game-dungeon-tournament-clear-reward-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", snapshot.CompletedRounds,
			"champion", champion,
			"reward_experience", experience,
			"reason", "tournament clear reward experience is out of range")
		return
	}
	state, err := newTournamentRewardState(
		cards,
		definition.PartyLimit,
		localPartySlot,
		uint32(experience),
		snapshot.CompletedRounds,
		champion,
	)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-clear-reward-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", snapshot.CompletedRounds,
			"champion", champion,
			"reason", err.Error())
		return
	}
	// ⚠️ 幂等凭据（`runtime.tournamentExperience`）由被调函数**自己**在 runtime 上
	// 读写，不经参数传递 —— 早先的版本传了一份局部拷贝，导致闸门永不生效、
	// 重复结算会给第二笔经验（被本批真档幂等用例抓出）。
	stored := state
	runtime.tournamentReward = &stored
	body := buildTournamentClearRewardBody(state)

	// ── 通关经验真的落到角色账上（P10）────────────────────────────────────────
	//
	// 厂商顺序（`TournamentDungeonCoordinator.cs:159-212 EnsureParticipantRewardsAsync`）：
	//
	//	:180-181  if (!rewards.ExperienceDelivered) await TryDeliverExperienceAsync(...)
	//	:189-211  …… 之后才做 `SendClearRewardAsync`（NOTI 389 的投影预约）
	//
	// 即**经验先提交、389 后投影**，两者在同一次"冻结奖励态"里。本仓照这个顺序：
	// 先提交经验（同一次结算流程、同一个 owner/事务边界），再发 389。
	//
	// 提交失败/幂等命中/经验为 0 都在 `commitCurrentTournamentClearRewardExperienceLocked`
	// 里留具名事件；这一层只负责把"已经落库的绝对角色状态"回给客户端
	// （厂商 `:652-661` 的 SendExpGrantNotificationAsync + InDungeonLevelUpFollowups）。
	experienceContext, cancelExperience := context.WithTimeout(context.Background(), createWriteTimeout)
	experienceCommitted, experienceResult, experienceErr := s.commitCurrentTournamentClearRewardExperienceLocked(
		experienceContext,
		session,
		runtime,
		state.RewardExperience,
		state.CompletedRounds,
	)
	cancelExperience()
	if experienceErr != nil {
		s.logGameEvent(session, "game-dungeon-tournament-clear-experience-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", state.CompletedRounds,
			"reward_experience", state.RewardExperience,
			"committed", false,
			"reason", "tournament_clear_reward_experience_commit_failed",
			"error", experienceErr)
	} else if experienceCommitted {
		s.sendCurrentTournamentExperienceCharacterStateLocked(
			session,
			runtime,
			experienceResult,
			state.CompletedRounds,
		)
	}

	// ── 冠军落定 ⇒ **先**通关、**后** 388（族内实机验收顺序，本批解环）────────────
	//
	// 族内 0.1.8 的实机验收（`90原神开源0.1.8\dnf90-source-oneclick-main\changlog.md:438-440`）：
	//
	//	"Live acceptance reached the native two-chest reward screen, then stalled
	//	 because the client emitted bodyless command 457 … " ⇒ 客户端**先看到通关**
	//	（`Session.Complete()` + clear-map Phase A + op574 快照 + op115 + 空 op31）
	//	才会进"两个宝箱"的原生奖励界面并发 bodyless CMD 457；回 9 字节选奖态后
	//	"completes settlement, returns to Shonan and advances quest 6840"。
	//
	// 对照实现（同族 `dungeon_tournament.go:613-688 completeCurrentDungeonTournamentDeathLocked`）
	// 的顺序正是：`Session.Complete()` → Phase A → 通关通知 → 再发 388。
	// 本仓此前把通关压到"选奖结束"，客户端在终点只收到 388、收不到任何通关序列，
	// op117 又被拒 ⇒ 假死（线上 `game-000006` 实证）。这里改成冠军即自驱提交，
	// 顺序与对照实现逐行一致。
	//
	// 被淘汰（`Terminal == Eliminated`）**不得**走这里：淘汰绝不产出清关事实
	// （规范第 863/865 行，`evaluateCurrentTournamentClearIntentLocked` 的
	// `tournament_result_is_eliminated` 仍然是硬门）。
	if champion {
		targetObjectKey := uint32(0)
		if pathActors := runtime.Tournament.PathActors; len(pathActors) != 0 {
			// 厂商 `TryCreateClearIntent`（:291-298）：finalSequence = FirstActorSequence + PathActors.Count - 1。
			targetObjectKey = uint32(runtime.Tournament.boundFirstSequence()) +
				uint32(len(pathActors)) - 1
		}
		if err := s.submitCurrentTournamentClearCommitLocked(
			session,
			runtime,
			targetObjectKey,
			"tournament_champion_decided_before_clear_reward",
		); err != nil {
			s.logGameEvent(session, "game-dungeon-tournament-clear-commit-deferred",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"completed_rounds", state.CompletedRounds,
				"target_object_key", targetObjectKey,
				"reason", "tournament_champion_clear_commit_failed",
				"error", err)
		}
	}

	if err := s.sendGameUpperRawClass(session, a21NotiTournamentClearReward, body, 0); err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-clear-reward-send-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"msg_id", a21NotiTournamentClearReward,
			"body_len", len(body),
			"error", err)
		return
	}
	s.logGameEvent(session, "game-dungeon-tournament-clear-reward-sent",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"msg_id", a21NotiTournamentClearReward,
		"body_len", len(body),
		"completed_rounds", state.CompletedRounds,
		"reward_experience", state.RewardExperience,
		"reward_experience_committed", runtime.tournamentExperience.Committed,
		"reward_experience_receipt", runtime.tournamentExperience.Key,
		"reward_experience_reason", runtime.tournamentExperience.Reason,
		"local_party_slot", state.LocalPartySlot,
		"champion", champion,
		"gold_resolved", goldOK,
		"source", "vendor_TournamentDungeonCoordinator.cs:664-679_SendClearReward")
}

// currentTournamentLocalPartySlot 取"本会话在队伍里的槽位"，取不到返回 -1。
// 厂商用 `TournamentDungeonCoordinator.cs:702-714 ResolvePartySlot`（无队伍 = 0）；
// 本仓的等价物是既有队伍名册里本角色所在的下标（`currentDungeonPartyCount` 用的就是长度口径）。
func currentTournamentLocalPartySlot(session *gameSession) int {
	if session == nil {
		return 0
	}
	session.party.mu.Lock()
	defer session.party.mu.Unlock()
	members := session.party.state.Members
	if len(members) <= 1 {
		return 0
	}
	for index, member := range members {
		if member.UserID == session.selectedCharacterID {
			if index >= tournamentPartySlotMax {
				return -1
			}
			return index
		}
	}
	return -1
}

// currentTournamentGoldCardAmountLocked 用既有 clear-reward 生成器取"本次 clear 的金币基数"，
// 再按厂商 `ScaleTournamentGold` 乘 `[tournament clear reward gold rate]`。
//
// 取不到（PVF 金币表缺失 / 等级不在表内 / 仓储不可用）时返回 ok=false：
// 调用方**不下发 389**，绝不拿 0 或猜的数值顶替。
func (s *Service) currentTournamentGoldCardAmountLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) (int64, bool) {
	if s == nil || session == nil || runtime == nil || runtime.Tournament == nil {
		return 0, false
	}
	definition := runtime.Tournament.Definition
	if definition == nil || definition.ClearRewardGoldRate <= 0 {
		return 0, false
	}
	if err := s.freezeCurrentDungeonCardRewardStateLocked(session, runtime); err != nil {
		return 0, false
	}
	state := runtime.settlementCardRewardState
	if state == nil {
		return 0, false
	}
	baseGold := state.plan.Sides[0].Gold
	if baseGold <= 0 {
		return 0, false
	}
	scaled := float64(baseGold) * float64(definition.ClearRewardGoldRate)
	if scaled <= 0 || scaled >= float64(1<<62) {
		return 0, false
	}
	return int64(scaled), true
}

// rollCurrentTournamentRewardCards 对应厂商
// `TournamentDungeonApplicationService.cs:433-524 TryCreateRewardCards`：
//
//	第 1 类（上排）= resultKey = CompletedRounds 的两张；
//	第 2 类（下排）= 只在"打满 4 轮"时才有，resultKey = 5 的两张；
//	每张按 `[result card]` 的 GoldWeight / ItemWeight / EmptyWeight 抽；
//	抽中金币 ⇒ 用 frozenGold；抽中道具 ⇒ 按 `[reward item rate]` 权重抽 ItemId/Count；
//	否则 ⇒ 空卡（ItemId = -1）。
//
// `goldAmountOK == false` 时**不会把金币权重消费成 0 金币**：整次抽卡失败并返回原因，
// 由调用方决定不下发（缺 PVF 数据时报错，不写死）。
func rollCurrentTournamentRewardCards(
	definition *tournamentDefinition,
	completedRounds byte,
	completedAllRounds bool,
	rng *currentDungeonDropLCG,
	goldAmount int64,
	goldAmountOK bool,
) ([]tournamentCardReward, string) {
	if definition == nil {
		return nil, "tournament definition is missing"
	}
	if rng == nil {
		return nil, "tournament reward random source is missing"
	}
	if len(definition.ResultCards) != tournamentResultCardKeyCount {
		return nil, fmt.Sprintf("tournament result cards=%d want=%d",
			len(definition.ResultCards), tournamentResultCardKeyCount)
	}
	if len(definition.RewardItemRates) == 0 {
		return nil, "tournament reward item rates are missing"
	}
	var itemWeightTotal int64
	for _, rate := range definition.RewardItemRates {
		if rate.ItemID <= 0 || rate.Weight <= 0 || rate.Count <= 0 {
			return nil, "tournament reward item rate contains an invalid row"
		}
		itemWeightTotal += rate.Weight
	}
	if itemWeightTotal <= 0 {
		return nil, "tournament reward item rate total weight is not positive"
	}

	// 需要抽的卡与它们各自的 resultKey：上排两张开 CompletedRounds，下排两张开 5。
	// 非冠军时下排按厂商 `:496-505` 直接写空卡，**不读权重表**。
	keys := make([]int64, 0, tournamentCardTypeCount*tournamentCardsPerType)
	for index := 0; index < tournamentCardsPerType; index++ {
		keys = append(keys, int64(completedRounds))
	}
	if completedAllRounds {
		for index := 0; index < tournamentCardsPerType; index++ {
			keys = append(keys, tournamentResultCardChampionKey)
		}
	}

	cards := make([]tournamentCardReward, 0, tournamentCardTypeCount*tournamentCardsPerType)
	for _, key := range keys {
		card, reason := rollCurrentTournamentResultCard(
			definition, key, rng, itemWeightTotal, goldAmount, goldAmountOK)
		if reason != "" {
			return nil, reason
		}
		cards = append(cards, card)
	}
	if !completedAllRounds {
		for index := 0; index < tournamentCardsPerType; index++ {
			cards = append(cards, tournamentCardReward{ItemID: tournamentEmptyRewardItemID})
		}
	}
	return cards, ""
}

func rollCurrentTournamentResultCard(
	definition *tournamentDefinition,
	resultKey int64,
	rng *currentDungeonDropLCG,
	itemWeightTotal int64,
	goldAmount int64,
	goldAmountOK bool,
) (tournamentCardReward, string) {
	weights, ok := definition.ResultCards[int(resultKey)]
	if !ok {
		return tournamentCardReward{}, fmt.Sprintf("tournament result card key=%d is missing", resultKey)
	}
	total := weights.TotalWeight()
	if total <= 0 || total > int64(^uint32(0)>>1) {
		return tournamentCardReward{}, fmt.Sprintf("tournament result card key=%d weights are invalid", resultKey)
	}
	roll := int64(rng.Next(int(total)))
	switch {
	case roll < weights.GoldWeight:
		if !goldAmountOK || goldAmount <= 0 {
			return tournamentCardReward{}, "tournament gold card amount is not resolvable from the PVF clear-reward owner"
		}
		return tournamentCardReward{IsGold: true, GoldAmount: goldAmount}, ""
	case roll < weights.GoldWeight+weights.ItemWeight:
		itemRoll := int64(rng.Next(int(itemWeightTotal)))
		var cumulative int64
		for _, rate := range definition.RewardItemRates {
			cumulative += rate.Weight
			if itemRoll >= cumulative {
				continue
			}
			return tournamentCardReward{ItemID: rate.ItemID, StackCount: rate.Count}, ""
		}
		return tournamentCardReward{}, "tournament reward item rate roll fell through"
	default:
		return tournamentCardReward{ItemID: tournamentEmptyRewardItemID}, ""
	}
}

// handleCurrentTournamentRewardCommand 是上行 CMD 458 / 459 的入口。
//
// 厂商口径（`Network\Parsers\Dungeon\DungeonCommandParser.cs`）：
//
//	:54-64  TOURNAMENT_REWARD_SELECT_STATE（0x01CA = 458）**body 必须为空**
//	:66-85  TOURNAMENT_REWARD_SELECT（0x01CB = 459）**body 必须恰好 2 字节**，
//	        且 body[0]（cardType）< 2、body[1]（cardIndex）< 2。
//
// 本仓 `dnfenum` 的 `0x01Cx` 区段整体 −1（本仓 457/458 = 厂商 458/459，
// 458 在本仓还叫 `CmdPacketTournamentRewardSelect`），而 **459 在本仓是
// `CmdPacketAvatarOptionChange`**。为了不因为一个待单独立项的枚举偏移而收错包，
// 这里**按 body 形状判语义**、并只在"本会话确实有等待选择的锦标赛奖励态"时才接管：
//
//	空 body            ⇒ 视为 SELECT_STATE 请求，回一条 458 选奖态
//	恰好 2 字节且两个都 <2 ⇒ 视为 SELECT 提交，落奖励
//
// 其余情况一律返回 (false, nil)：控制流回到改动前那条路径（默认档同理，
// 因为 `runtime.tournamentReward == nil` 时第一行就返回）。
func (s *Service) handleCurrentTournamentRewardCommand(
	session *gameSession,
	typ uint16,
	body []byte,
) (bool, error) {
	if !dungeonTournamentEnabled() {
		return false, nil
	}
	if typ != a21CmdTournamentRewardSelectState && typ != a21CmdTournamentRewardSelect {
		return false, nil
	}
	if session == nil {
		return false, nil
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.tournamentReward == nil {
		return false, nil
	}
	state := runtime.tournamentReward
	switch {
	case len(body) == 0:
		// 厂商 `HandleRewardSelectStateAsync`（:214-241）在这一条请求上做三件事：
		// ① 回 CMD 458 选奖态；② 记日志；③ `StartRewardSelectionTimer`（3000ms）。
		//
		// ⚠️ 本分支（**未受邀**的 457 应答）只回那 9 字节选奖态，不附带
		// `buildTournamentRewardSelectionBody`：厂商确实会在 `RecoverAsync`（:289-298）
		// 里把 state + selection 两条都发出去，但线上实证未受邀的 458 之后客户端异常退出
		// （2026-09-27 22:23–22:25 CN 线上 conn game-000003：`exception code=0xC0000005`；
		// 23:1x conn game-000034：紧接着 RST）。族内 0.1.8 的实机验收也只回 state。
		//
		// 本批的真机派发表取证推翻了其中一个**旧的**理由（"真机没有 458 下行 handler"）：
		// 90CN `DNF.exe` 的注册表把 0x1C9→sub_1CF4FD0、**0x1CA→sub_1CF5010** 都注册了，
		// 458 是双向包（见 dungeon_tournament_config.go 的取证块）。所以 458 不是"不能发"，
		// 而是**只能回在 solicited（客户端刚发来 2 字节选奖）路径上** —— 那条崩溃观察
		// 针对的是未受邀的 458，本分支因此继续一个字节都不发 458。
		// 客户端发来体长为 0 的 457 ⇒ 翻牌选奖界面已打开、在等选奖。
		// 记下这个前提：3s 兜底自动选奖**只有在显式开危险档时**才会回 458 选中投影
		// （默认档下 bodyless 457 不算受邀 —— 未受邀的 458 会崩客户端，见本文件
		// `sendCurrentTournamentRewardSelectionAckLocked` 的门槛说明）。
		session.tournamentRewardStateRequested = true
		ack := buildTournamentRewardSelectStateBody(*state)
		if err := s.sendGameUpperRawClass(session, a21CmdTournamentRewardSelectState, ack, s.currentTournamentRewardReplyClass()); err != nil {
			return true, err
		}
		if s.options.currentClientVersion == "official-24.9" {
			state.commandStateReady = true
		}
		s.armCurrentTournamentRewardAutoSelectTimerLocked(session, runtime)
		s.logGameEvent(session, "game-dungeon-tournament-reward-select-state-sent",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"msg_id", a21CmdTournamentRewardSelectState,
			"body_len", len(ack),
			"selection_msg_id", 0,
			"selection_body_len", 0,
			"auto_select_timer_ms", currentTournamentRewardAutoSelectDelay.Milliseconds(),
			"source", "current_exe_sub_1CF4FD0_nine_byte_state_only")
		return true, nil
	case len(body) == 2 && body[0] < tournamentCardTypeCount && body[1] < tournamentCardsPerType:
		// **solicited**：客户端自己发来的 2 字节选奖（真机 RV 0x1D81860 的组装形状），
		// 修复档下它必须收到 458 选中投影，否则翻牌界面永远推进不了。
		s.applyCurrentTournamentRewardSelectionLocked(session, runtime, body[0], body[1], tournamentRewardSelectionSourceClient)
		return true, nil
	default:
		s.logGameEvent(session, "game-dungeon-tournament-reward-command-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"msg_id", typ,
			"body_len", len(body),
			"reason", "unproven_tournament_reward_command_shape",
			"source", "vendor_DungeonCommandParser.cs:54-85")
		return true, nil
	}
}

// applyCurrentTournamentRewardSelectionLocked 处理一次选奖：
// 把"选哪张卡"落到**既有**的背包事务边界
// （`dungeon_card_asset.go:29 grantDungeonCardRewardBundle`，与普通翻牌奖励同一条写库路径），
// 再按来源回下行（厂商 `TournamentDungeonCoordinator.cs:302-398
// ApplyTournamentRewardSelectionAsync` 的等价物）。
//
// **solicited / unsolicited 是本函数唯一的分叉**（`tournamentRewardSelectionSource`）：
//
//	source == client（客户端自己发来的 2 字节选奖）
//	    ⇒ 修复档下**先回 458 选中投影**（7 字节），选奖未完成时再补 9 字节 457 选奖态；
//	      投影在"落奖成功 / 落奖失败回滚 / 该组已被拿走（重复）"三个出口**都发**
//	      —— 厂商 `:368-372` 就是在 reserve 失败时也照样回这条（客户端只有靠它才知道
//	      "这张卡已经被拿走"）。3s 兜底自动选奖可能已经替玩家把这一组发掉了，所以
//	      "重复"出口也必须回，否则客户端那一排永远推进不了（现场症状就是卡在翻牌）。
//	source == timer（3s 兜底自动选奖，**unsolicited**）
//	    ⇒ 一个字节的 458 都不发，只沿用改动前的 457 应答（未受邀 458 之后客户端异常
//	      退出的实证见 dungeon_tournament_config.go 的取证块）。
//
// 幂等：同一张卡只落一次奖（`tournamentRewardState.Selected` 已选即不再落库）。
// 全部选完（厂商 `:392-397 AreAllParticipantRewardsComplete` ⇒ `CompleteTournamentResultAsync`）
// ⇒ 取消兜底计时器并自驱提交终局（`completeCurrentTournamentResultLocked`）。
func (s *Service) applyCurrentTournamentRewardSelectionLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	cardType byte,
	cardIndex byte,
	source tournamentRewardSelectionSource,
) {
	if s == nil || session == nil || runtime == nil || runtime.tournamentReward == nil {
		return
	}
	state := runtime.tournamentReward
	if int(cardType) >= tournamentCardTypeCount {
		s.logTournamentRewardSelectionBlocked(session, runtime, cardType, cardIndex, 0, "card_type_out_of_range", "", source)
		return
	}
	if !state.isCardTypeEnabled(int(cardType)) || int(cardIndex) >= int(state.cardCount(int(cardType))) {
		s.logTournamentRewardSelectionBlocked(session, runtime, cardType, cardIndex, state.cardCount(int(cardType)), "card_index_out_of_range", "", source)
		return
	}
	if state.Selected[cardType] != tournamentUnselectedCard {
		// 这一组已经被拿走：**不重复落奖**，但（solicited + 修复档）仍要回 458 选中投影，
		// 客户端据此把这张卡标成已选、翻牌界面推进到下一排/收尾。
		selectionReplyBodyLen := s.sendCurrentTournamentRewardSelectionAckLocked(session, runtime, source, cardType, cardIndex)
		s.logGameEvent(session, "game-dungeon-tournament-reward-selection-duplicate",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"card_type", cardType,
			"card_index", cardIndex,
			"accepted_index", state.Selected[cardType],
			"selection_reply_msg_id", a21CmdTournamentRewardSelect,
			"selection_reply_body_len", selectionReplyBodyLen,
			"source", source.String())
		return
	}
	state.Selected[cardType] = cardIndex
	reward := state.Cards[cardType][cardIndex]
	delivered := currentTournamentRewardIsEmpty(reward)
	failureReason := ""
	// resolution 是这张卡的**现场证据**（真实 PVF 定义 / 取不到的原因 / 落点段），
	// 只进日志：成功进 `game-dungeon-tournament-reward-delivered`，失败拼进
	// `game-dungeon-tournament-reward-selection-blocked` 的 `error`。
	resolution := newTournamentRewardItemResolution(reward)
	if !delivered {
		repositories, ok := s.repositoryGroup()
		if !ok || repositories.CharacterAssets == nil {
			failureReason = errDungeonCardAssetOwnerUnavailable.Error()
		} else {
			now := s.gameplayNow().UTC()
			bundle, resolved, bundleErr := s.currentTournamentRewardGrantBundle(reward, now)
			resolution = resolved
			if bundleErr != nil {
				failureReason = fmt.Sprintf("%v (%s)", bundleErr, resolution.describe())
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				grant, grantErr := grantDungeonCardRewardBundle(
					ctx,
					repositories,
					fmt.Sprintf("%d", session.selectedCharacterID),
					0,
					bundle,
					now,
				)
				cancel()
				if grantErr != nil {
					// owner 的具名校验已经把命中的规则写进 error（`rule=...`），这里再补上
					// "这张卡解析成了什么 / 真实段是多少"，下一次线上日志可直接定位。
					failureReason = fmt.Sprintf("%v (%s)", grantErr, resolution.describe())
				} else {
					delivered = true
					if grant.GoldAfter != grant.GoldBefore {
						_ = s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentGoldStateBody(grant.GoldAfter), 0)
					}
					refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(grant.ItemSlots))
					for _, slot := range grant.ItemSlots {
						if slot < 0 {
							continue
						}
						refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{
							ListType:  dnfrepo.MainInventoryListType,
							SlotIndex: slot,
						})
					}
					if len(refreshes) != 0 {
						if refreshErr := s.sendSelectedIncrementalItemSlotRefreshes(session, "dungeon-tournament-reward", refreshes); refreshErr != nil {
							s.logGameEvent(session, "game-dungeon-tournament-reward-refresh-deferred",
								"char_id", session.selectedCharacterID,
								"dungeon_id", runtime.Dungeon.ID,
								"error", refreshErr)
						}
					}
				}
			}
		}
	}
	if !delivered {
		// 交付失败 ⇒ 回滚选中，允许客户端重选（厂商 `FailReward` 语义，:255-265），
		// 并（solicited + 修复档）回一条把这一组标成"还没人拿"的 458 投影，
		// 让客户端的翻牌界面留在可重选的状态。
		state.Selected[cardType] = tournamentUnselectedCard
		selectionReplyBodyLen := s.sendCurrentTournamentRewardSelectionAckLocked(session, runtime, source, cardType, cardIndex)
		s.logTournamentRewardSelectionBlocked(session, runtime, cardType, cardIndex, state.cardCount(int(cardType)), "reward_delivery_failed", failureReason, source)
		if selectionReplyBodyLen != 0 {
			s.logGameEvent(session, "game-dungeon-tournament-reward-selection-ack-sent",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"card_type", cardType,
				"card_index", cardIndex,
				"selection_reply_msg_id", a21CmdTournamentRewardSelect,
				"selection_reply_body_len", selectionReplyBodyLen,
				"source", source.String())
		}
		return
	}
	// ⚠️ **458 选中投影只回在 solicited 路径上**（客户端自己刚发来的 2 字节选奖）；
	// 未受邀的 458 之后真机客户端异常退出（2026-09-27 22:23–22:25 conn game-000003
	// `exception code=0xC0000005`；23:1x conn game-000034 紧接着 RST）。判据是"未受邀"，
	// 不是"458 不能发"：真机派发表确实把 0x1CA 注册到了 sub_1CF5010
	// （见 dungeon_tournament_config.go 的取证块），而这条 7 字节体正是它的 reader 形状
	// （`N1 | N1 字节 | N2 | N2 字节`）。
	//
	// 为什么这条包必须存在（本批根因）：改动前**任何**路径都不回它，真机客户端因此拿不到
	// "这张卡被哪个队伍槽位拿走"（sub_1D81E00 的 `setSlot`：`slot[0] == -1` 才写、
	// 0xFF = 空位不写、并把该排标成已选），翻牌界面永远推进不了，玩家只能自己返回城镇，
	// 而奖励因为 3s 兜底自动选奖已经在服务端落库、只出现在聊天栏 —— 与操作者报障逐条一致。
	selectionReplyBodyLen := s.sendCurrentTournamentRewardSelectionAckLocked(session, runtime, source, cardType, cardIndex)
	// 落奖后的 9 字节 457 选奖态应答：形状与触发条件与改动前**逐字节一致**
	// （选奖未完成才补；族内 0.1.8 的 `handleCurrentDungeonTournamentRewardSelect`：
	// "选奖未完成 ⇒ 重发选奖态，客户端据此继续下一排；选完 ⇒ 不再发"）。
	replyMsgID := uint16(0)
	replyBodyLen := 0
	selectionComplete := state.isSelectionComplete()
	if !selectionComplete {
		stateBody := buildTournamentRewardSelectStateBody(*state)
		if err := s.sendGameUpperRawClass(session, a21CmdTournamentRewardSelectState, stateBody, s.currentTournamentRewardReplyClass()); err != nil {
			// 奖励已经落库，重发失败不能把这次交付变回"没发出去"：只记账，等客户端下一次
			// 457 请求（`handleCurrentTournamentRewardCommand` 的空 body 分支）补发。
			s.logGameEvent(session, "game-dungeon-tournament-reward-select-state-resend-deferred",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"card_type", cardType,
				"card_index", cardIndex,
				"msg_id", a21CmdTournamentRewardSelectState,
				"body_len", len(stateBody),
				"source", source.String(),
				"error", err)
		} else {
			replyMsgID = a21CmdTournamentRewardSelectState
			replyBodyLen = len(stateBody)
		}
	}
	s.logGameEvent(session, "game-dungeon-tournament-reward-delivered",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"card_type", cardType,
		"card_index", cardIndex,
		"is_gold", reward.IsGold,
		"gold", reward.GoldAmount,
		"item_id", reward.ItemID,
		"item_count", reward.StackCount,
		"item_pvf_path", resolution.PVFPath,
		"item_slot_start", resolution.SlotStart,
		"item_slot_end", resolution.SlotEnd,
		"reply_msg_id", replyMsgID,
		"reply_body_len", replyBodyLen,
		"selection_reply_msg_id", a21CmdTournamentRewardSelect,
		"selection_reply_body_len", selectionReplyBodyLen,
		"selection_complete", selectionComplete,
		"source", source.String())
	if !selectionComplete {
		return
	}
	// 厂商 `ApplyTournamentRewardSelectionAsync`（:392-397）：
	//   if (!AreAllParticipantRewardsComplete(run)) return;
	//   run.Timers.Cancel(TournamentRewardAutoSelect);
	//   await CompleteTournamentResultAsync(session, run);
	s.cancelCurrentTournamentRewardAutoSelectTimerLocked(session, runtime, "reward_selection_complete")
	if err := s.completeCurrentTournamentResultLocked(session, runtime); err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-clear-intent-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", "tournament_clear_commit_failed",
			"source", source.String(),
			"error", err)
	}
}

// sendCurrentTournamentRewardSelectionAckLocked 是 458 选中投影的**唯一**发送点，
// 返回真正发出去的体长（没发 = 0）。
//
// 发送门槛（三条之一成立即发，都不成立则一个字节不发）：
//
//	① `source.solicited()` —— 这次选奖是**客户端自己**刚发来的 2 字节选奖，
//	   且档位不是 `state`（`answersSolicitedSelection()`）。**这是唯一在默认档下成立的路径。**
//	② 3s 兜底自动选奖（timer）+ 档位是**显式** `select+timer` + 客户端已发来 bodyless 457
//	   （`session.tournamentRewardStateRequested`）。未受邀的 458 之后真机客户端异常退出
//	   （2026-09-27 22:23–22:25 `exception code=0xC0000005`；2026-09-29 06:05Z
//	   `conn_id=game-002721` RST 于 +1.265s），所以这条兜底路径是**危险档**、必须显式开。
//	③ 3s 兜底自动选奖（timer）+ `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT=state-request`
//	   （**显式**危险档：bodyless 457 本身就算"受邀"）+ 档位不是 `state` + 同一个 latch。
//	   ③ 与 ② 的唯一差别是**档位**：② 只认显式 `select+timer`，③ 连线上现取的 `select` 也认。
//	   ⚠️ ③ 就是 2026-09-29 05:03:10 +0800 上线的那个默认档，它把"卡在翻牌"变成了
//	   "1.265 秒后客户端 RST"（`game-002721`）。**现在两个开关的零值都是安全档**，
//	   ② ③ 都只有在显式写出危险字面量时才成立。
//
// 体形状（7 字节，2×2 卡局面）：`1 | 2 | slot slot | 2 | slot slot`，`slot` = 拿走这张卡的
// 队伍槽位（本仓当前只有单人 `LocalPartySlot = 0`），没人拿 = `0xFF`。出处三方一致：
//
//	厂商 `TournamentPacketBuilder.BuildTournamentRewardSelection`（:129-144）；
//	族内 0.1.8 `buildCurrentDungeonTournamentRewardSelection`（:218-238）；
//	真机 reader `sub_1CF5010`（RVA 0x18F5010）：读 N1 → N1 字节 → `setSlot(row=0)`，
//	再读 N2 → N2 字节 → `setSlot(row=1)`；`setSlot`（sub_1D81E00）只在该卡
//	`slot[0] == -1`（还没被拿走）且入参 != 0xFF 时才写入，并刷新卡面。
//
// ⚠️ **字节形状不是崩溃的原因——状态才是**。线上那两局发出的 7 字节体
// （`01 02 00 ff 02 ff ff` / `01 02 00 ff 02 00 ff`）与厂商 builder 逐字节同形，
// 真机 reader 也**恰好**消费 1+1+N1+N2 = 7 字节（不会把包缓冲读越界）。
// 崩的是 `SetSlot` 对**卡槽指针**的解引用（`mov eax,[esi+4*idx+0xcc]`）：客户端没进
// 可点状态时那个数组还没准备好 ⇒ 0xC0000005。所以判据不能是"帧对不对"，只能是
// "客户端有没有自己开口要过"。
func (s *Service) sendCurrentTournamentRewardSelectionAckLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source tournamentRewardSelectionSource,
	cardType byte,
	cardIndex byte,
) int {
	if s == nil || session == nil || runtime == nil || runtime.tournamentReward == nil {
		return 0
	}
	answered := source.solicited() && s.dungeonTournamentRewardAck().answersSolicitedSelection()
	if source == tournamentRewardSelectionSourceTimer &&
		s.options.currentClientVersion == "official-24.9" &&
		runtime.tournamentReward.commandStateReady &&
		s.dungeonTournamentRewardAck().answersSolicitedSelection() {
		answered = true
	}
	if !answered && source == tournamentRewardSelectionSourceTimer &&
		s.options.currentClientVersion != "official-24.9" &&
		s.dungeonTournamentRewardAck().answersTimerSelection() &&
		session.tournamentRewardStateRequested {
		// ②：3s 兜底自动选奖 + **显式** `REWARD_ACK=select+timer` + 客户端已打开翻牌界面
		// （latch 已置位）⇒ 回 458 选中投影。**危险档**：这一档把"卡在翻牌"换成
		// "客户端 1.265 秒后 RST"（2026-09-29 06:05Z `conn_id=game-002721`，4/4 局实证）。
		// 默认档 `select` 下 `answersTimerSelection()` 为假 ⇒ 这条不成立。
		answered = true
	}
	if !answered && source == tournamentRewardSelectionSourceTimer &&
		s.options.currentClientVersion != "official-24.9" &&
		dungeonTournamentRewardSolicitModeFromEnv().treatsStateRequestAsSolicitation() &&
		s.dungeonTournamentRewardAck().answersSolicitedSelection() &&
		session.tournamentRewardStateRequested {
		// ③：**显式** `REWARD_SOLICIT=state-request` 时把"客户端自己发来的 bodyless 457"
		// 也算成受邀。这与 ② 是同一件事，只是绕开了"档位必须是 select+timer"。
		// 它已实证会崩客户端（见上）；默认档是 `legacy` ⇒ 这条不成立。
		// `answersSolicitedSelection()` 保证 `REWARD_ACK=state` 仍然一个字节都不发。
		answered = true
	}
	if !answered {
		return 0
	}
	body := buildTournamentRewardSelectionBody(*runtime.tournamentReward)
	if err := s.sendGameUpperRawClass(session, a21CmdTournamentRewardSelect, body, s.currentTournamentRewardReplyClass()); err != nil {
		// 奖励已经落库 / 已回滚，重发失败不能把这次选奖变回"没发生"：只记账。
		// 客户端下一次 2 字节选奖还会再拿到投影（solicited 路径幂等）。
		s.logGameEvent(session, "game-dungeon-tournament-reward-selection-ack-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"card_type", cardType,
			"card_index", cardIndex,
			"msg_id", a21CmdTournamentRewardSelect,
			"body_len", len(body),
			"source", source.String(),
			"error", err)
		return 0
	}
	return len(body)
}

// v112 c82cb803... registers 457/458 via the command registry (1fac980),
// dispatched by 2523cf6/251e1e0 after consuming the result byte. The state
// handler 1fa5420 initializes UI state 6; selection handler 1fa5460 reads the
// two slot arrays. Class0 uses a different registry and cannot run these.
// Older profile behavior and its explicit rollback switches remain intact.
func (s *Service) currentTournamentRewardReplyClass() byte {
	if s.options.currentClientVersion == "official-24.9" {
		return 1
	}
	return 0
}

func (s *Service) logTournamentRewardSelectionBlocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	cardType byte,
	cardIndex byte,
	cardCount byte,
	reason string,
	failureReason string,
	source tournamentRewardSelectionSource,
) {
	s.logGameEvent(session, "game-dungeon-tournament-reward-selection-blocked",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"card_type", cardType,
		"card_index", cardIndex,
		"card_count", cardCount,
		"reason", reason,
		"error", businessLogDiagnostic(failureReason),
		"source", source.String())
}

// currentTournamentRewardIsEmpty 对应厂商 `IsEmptyReward`
// （TournamentDungeonApplicationService.cs:595-599，ItemId == -1 且不是金币）。
func currentTournamentRewardIsEmpty(reward tournamentCardReward) bool {
	return !reward.IsGold && reward.ItemID == tournamentEmptyRewardItemID
}

// tournamentRewardItemResolution 是"这张锦标赛卡在真实 PVF 里解析成了什么"的**现场证据**
// （只进日志，不参与判定）。线上 2026-09-27 的失败日志只有一句裸
// `dungeon card reward plan is invalid: dungeon reward is invalid`，
// 看不出是"道具根本不在 PVF 道具表里"还是"段不可用"还是"数量非法"。
type tournamentRewardItemResolution struct {
	ItemID    int64
	Count     int64
	Resolved  bool
	Kind      string
	PVFPath   string
	Stackable bool
	SlotStart int16
	SlotEnd   int16
	// ResolveError 是 PVF 解析失败的原因（成功时为空）。
	ResolveError string
}

func newTournamentRewardItemResolution(reward tournamentCardReward) tournamentRewardItemResolution {
	return tournamentRewardItemResolution{ItemID: reward.ItemID, Count: reward.StackCount}
}

func (r tournamentRewardItemResolution) describe() string {
	return fmt.Sprintf(
		"reward_evidence item=%d count=%d pvf_resolved=%t pvf_kind=%s pvf_path=%s slot_start=%d slot_end=%d pvf_error=%q",
		r.ItemID, r.Count, r.Resolved, r.Kind, r.PVFPath, r.SlotStart, r.SlotEnd, r.ResolveError)
}

// currentTournamentRewardGrantBundle 把一张锦标赛卡翻成既有背包 owner 的奖励 bundle：
//
//  1. 金币卡：直接用卡里的金币额（既有行为，逐字节不变）。
//  2. 道具卡：用**既有**的运行时 PVF 道具目录解析真实定义
//     （`Service.currentPVFItemCatalog()` → `pvfDungeonDropCatalog.ResolveItem`，
//     与普通翻牌 `dungeon_card_reward_runtime.go:currentDungeonRandomCardItem`、
//     掉落、任务奖励同一条真源；本仓不新开第二个 PVF reader），把 PVF 家族给出的落点段
//     写进 bundle 的 `SlotStart/SlotEnd`，并按真实 `Kind` 决定 `Stackable`，
//     再按同一套发放期规则（`currentPVFItemDefinitionForGrantAt`）解析到期时间。
//     绝不硬编码 3..N，也绝不凭卡面猜段。
//  3. PVF 解析不到 / 段不可用：**不猜段、也不挪用别的字段当格数**，返回具名错误；
//     调用方保留 `game-dungeon-tournament-reward-selection-blocked`
//     （`reason=reward_delivery_failed`）并把 resolution 证据拼进 `error`。
//
// 为什么**不**回退成"角色的主背包容量"（任务书里的备选方案，已按证据否掉）：
// 本仓 `dnfrepo.CharacterContainerState.MainSlotCount` 是 settings 里的
// `main_list_param16`，即**主背包扩展档位**——合法值只有 0/8/16/24
// （`repository/container_state.go:currentMainInventoryExpansion`，新角色默认 0，
// 商城扩展券每级 +8，见 `cera_shop_grant.go` 的 `targetExpansion = stage * 8`），
// 不是格数。把它当 `CardRewardCommand.MainSlots`（`addCardItem` 的扫描上界 0..N-1）
// 等于把奖励塞进 0..N：0 号格是金币钱包的预留格（`current_gold_wallet_repair.go`），
// 而未扩展角色的 0 又会让校验照旧失败。那是"发明一个落点"，比具名拒绝更糟，
// 所以本批只走 PVF 真源这一条路。
func (s *Service) currentTournamentRewardGrantBundle(
	reward tournamentCardReward,
	now time.Time,
) (dungeonCardRewardBundle, tournamentRewardItemResolution, error) {
	resolution := newTournamentRewardItemResolution(reward)
	if reward.IsGold || reward.ItemID <= 0 || reward.ItemID > math.MaxUint32 || reward.StackCount <= 0 {
		bundle, err := currentTournamentRewardBundle(reward, nil)
		return bundle, resolution, err
	}
	definition, resolveErr := s.currentTournamentRewardItemDefinition(uint32(reward.ItemID))
	if resolveErr != nil {
		resolution.ResolveError = resolveErr.Error()
		return dungeonCardRewardBundle{}, resolution, resolveErr
	}
	resolution.Resolved = true
	resolution.Kind = string(definition.Kind)
	resolution.PVFPath = definition.PVFPath
	resolution.Stackable = definition.Kind == dungeonDropItemStackable
	resolution.SlotStart = definition.SlotStart
	resolution.SlotEnd = definition.SlotEnd
	if !dnfdungeon.CardRewardSlotRangeUsable(definition.SlotStart, definition.SlotEnd) {
		return dungeonCardRewardBundle{}, resolution, fmt.Errorf(
			"tournament item card has no usable PVF slot range: item=%d kind=%s pvf_path=%s slot_start=%d slot_end=%d",
			reward.ItemID, definition.Kind, definition.PVFPath, definition.SlotStart, definition.SlotEnd)
	}
	grantDefinition, err := currentPVFItemDefinitionForGrantAt(definition, now)
	if err != nil {
		return dungeonCardRewardBundle{}, resolution, fmt.Errorf(
			"tournament item card=%d grant-time PVF definition is invalid: %w", reward.ItemID, err)
	}
	bundle, err := currentTournamentRewardBundle(reward, &grantDefinition)
	return bundle, resolution, err
}

// currentTournamentRewardItemDefinition 用**既有**的运行时 PVF 道具目录解析这张卡的道具
// 定义（`Service.currentPVFItemCatalog`，启动期 `preloadPVFItemCatalog` 就已就绪；
// 普通翻牌、掉落、任务奖励、商城走的都是它）。
func (s *Service) currentTournamentRewardItemDefinition(itemID uint32) (dungeonDropItemDefinition, error) {
	if s == nil {
		return dungeonDropItemDefinition{}, errDungeonDropSourceRequired
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return dungeonDropItemDefinition{}, fmt.Errorf("tournament card item=%d dungeon drop PVF catalog unavailable: %w", itemID, err)
	}
	if catalog == nil {
		return dungeonDropItemDefinition{}, fmt.Errorf("tournament card item=%d: %w", itemID, errDungeonDropSourceRequired)
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		return dungeonDropItemDefinition{}, fmt.Errorf("tournament card item=%d PVF definition unresolved: %w", itemID, err)
	}
	return definition, nil
}

// currentTournamentRewardItemExtra 复刻**普通翻牌**道具行的身份键
// （`dungeon_card_reward_runtime.go:currentDungeonRandomCardItem` 的同一组 key）：
// 落库行要能过家族闸门（`item_slot_family_guard.go` 读 `item_kind` / `pvf_path` /
// `stackable_type`），也要能和普通翻牌发出的同一个道具并堆
// （`dnfrepo.CanMergeItemStack`）。只写 PVF 里真实存在的字段，不合成任何随机投影
// （`quality_seed` 那类每张卡现算的值不在这里硬造）。
func currentTournamentRewardItemExtra(definition dungeonDropItemDefinition) map[string]string {
	extra := map[string]string{
		"source":    "dungeon_tournament_reward",
		"item_kind": string(definition.Kind),
		"pvf_path":  definition.PVFPath,
	}
	if definition.StackableType != "" {
		extra["stackable_type"] = definition.StackableType
	}
	if definition.StackLimit > 0 {
		extra["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
	}
	if definition.EquipmentType != "" {
		extra["equipment_type"] = definition.EquipmentType
	}
	if definition.Durability > 0 {
		extra["durability"] = strconv.FormatUint(uint64(definition.Durability), 10)
		extra["max_durability"] = strconv.FormatUint(uint64(definition.Durability), 10)
	}
	if currentNewPVFEquipmentSealFlag(definition) != 0 {
		extra["seal_flag"] = "1"
	}
	return extra
}

// currentTournamentRewardBundle 把一张锦标赛卡翻成既有背包 owner 的奖励 bundle。
// 道具行不带 ItemCore 实例内容（RawEntry 为空），因此走的是"按模板新建"这条
// 既有路径 —— 与普通翻牌奖励完全同一个 owner，不新开第二套写库逻辑。
//
// definition 是该道具**真实**的 PVF 定义（由 `currentTournamentRewardGrantBundle`
// 解析）；金币卡传 nil。没有真实定义时**不构造道具 bundle**：宁可具名失败，
// 也不发一条 `SlotStart=0/SlotEnd=0` 的行 —— 旧代码正是那样，被 owner 的
// `rule=card_item_has_no_pvf_slot_range_and_no_main_slots` 拦下，
// 于是锦标赛里**任何**道具卡都发不出去（现场：dungeon 121/123-124 第 4 轮翻牌卡死）。
func currentTournamentRewardBundle(
	reward tournamentCardReward,
	definition *dungeonDropItemDefinition,
) (dungeonCardRewardBundle, error) {
	if reward.IsGold {
		if reward.GoldAmount <= 0 {
			return dungeonCardRewardBundle{}, errors.New("tournament gold reward amount is not positive")
		}
		return dungeonCardRewardBundle{Gold: reward.GoldAmount}, nil
	}
	if reward.ItemID <= 0 || reward.StackCount <= 0 {
		return dungeonCardRewardBundle{}, fmt.Errorf(
			"tournament item reward is invalid: item=%d count=%d", reward.ItemID, reward.StackCount)
	}
	if definition == nil {
		return dungeonCardRewardBundle{}, fmt.Errorf(
			"tournament item reward has no resolved PVF definition: item=%d count=%d", reward.ItemID, reward.StackCount)
	}
	if definition.ItemID != 0 && uint32(reward.ItemID) != definition.ItemID {
		return dungeonCardRewardBundle{}, fmt.Errorf(
			"tournament item reward PVF definition mismatch: card_item=%d resolved_item=%d",
			reward.ItemID, definition.ItemID)
	}
	policy := currentPVFItemPlacementPolicy(*definition)
	return dungeonCardRewardBundle{
		Items: []dungeonCardItemReward{{
			ItemID:           reward.ItemID,
			Count:            reward.StackCount,
			Stackable:        definition.Kind == dungeonDropItemStackable,
			SlotStart:        definition.SlotStart,
			SlotEnd:          definition.SlotEnd,
			ExpireAt:         definition.ExpirationDate,
			Extra:            currentTournamentRewardItemExtra(*definition),
			AllowQuickSlots:  policy.AllowQuickSlots,
			PreferQuickSlots: policy.PreferQuickSlots,
			StackLimit:       definition.StackLimit,
		}},
	}, nil
}

// shouldDeferTournamentDefaultClear 是厂商
// `DungeonCombatHandler.ShouldClearDungeon`（DungeonCombatHandler.cs:260-267）
// 在"终点 Boss 已验证"这一条路径上的接线：
//
//	return clearConditionMatched || (reachedBossEndpoint && !ignoreDefaultDungeonClear);
//
// 锦标赛在 `TournamentDungeonApplicationService.cs:70-72` 把
// `run.IgnoreDefaultDungeonClear` 置 true（本仓 `dungeon_runtime_state.go:499`）。
// 到 `bossRoomClearReady` 这个点意味着 `reachedBossEndpoint == true`，
// 而这里对应的 `clearConditionMatched == false`（房间是普通阻挡怪全灭，不是脚本 clear 条件）。
//
// 返回 true = 本次"到终点即通关"必须被压住：
//   - 默认档：`IgnoreDefaultDungeonClear` 恒为 false ⇒ 恒返回 false，控制流逐字节不变。
//   - vendor 档：只有赛制还没到终局（非冠军）时才压住；冠军一落定就放行 ⇒ 不会卡死。
func shouldDeferTournamentDefaultClear(runtime *runtimeDungeonState) bool {
	if runtime == nil {
		return false
	}
	clearConditionMatched := false
	reachedBossEndpoint := true
	if shouldClearDungeon(clearConditionMatched, reachedBossEndpoint, runtime.IgnoreDefaultDungeonClear) {
		return false
	}
	// `ignoreDefaultDungeonClear` 为真但赛制已经终局（冠军）⇒ 终局门已放行，不再压。
	if runtime.Tournament != nil && runtime.Tournament.isChampion() {
		return false
	}
	return true
}

// ── 「副本级通关」的锦标赛门（本批接线，任务 #1）───────────────────────────────
//
// 厂商把"锦标赛的什么算通关"完全交给状态机：
//
//	`TournamentDungeonApplicationService.cs:70-72`
//	    run.IgnoreDefaultDungeonClear = true;
//	`TournamentDungeonCoordinator.cs:589-605 CompleteTournamentResultAsync`
//	    if (!IsTournamentChampion(run)) { 只记日志; return; }
//	    return SubmitClearIntentAsync(session, run);
//	`TournamentDungeonApplicationService.cs:275-317 TryCreateClearIntent`
//	    要求 sourcePlayerId > 0 && runtime.IsChampion && rewards.IsSelectionComplete；
//	    sourceActorId = FirstActorSequence + PathActors.Count - 1（最后一个路径 actor）。
//	`DungeonClearPresentationPolicy.CompletesAtClearCommit`
//	    Tournament 为 true ⇒ `DungeonSettlementHandler.cs:2693-2706` 只把 run 推到
//	    ResultShown + Completed，**不发普通结算投影**（`UsesStandardResultProjection`
//	    对 Tournament 为 false）。
//
// 本仓的等价物：`completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked`
// （`dungeon_boss_die_ordinary.go:14-87`）→ `completeCurrentDungeonBossDieCheckLocked`
// （`dungeon_boss_die_settlement.go:15-173`）→ 清图 quest Phase A / op115 / op31。
//
// 两处门：
//   - `shouldDeferTournamentClearCommit`：赛制还没终局（Active）⇒ 压住一切副本级通关提交，
//     直到冠军 + 选奖结束。**被淘汰（Eliminated）不是 Active ⇒ 本门不压**，改由
//     `trySubmitCurrentTournamentClearIntentLocked` 以具名事件拒掉 —— 淘汰绝不产出
//     DungeonClearedFact（规范第 863/865 行："Eliminated 只完成 Tournament 失败终止，
//     禁止生成 DungeonClearedFact、任务清图进度、难度权限或普通卡牌结算"）。
//   - `trySubmitCurrentTournamentClearIntentLocked`：唯一允许提交通关 intent 的入口，
//     复刻 `TryCreateClearIntent` 的三条硬条件。
//
// 默认档：`runtime.Tournament == nil` ⇒ 两处都恒为放行/不可达，控制流逐字节等于改动前。
func shouldDeferTournamentClearCommit(runtime *runtimeDungeonState) bool {
	if runtime == nil || runtime.Tournament == nil {
		return false
	}
	if !runtime.IgnoreDefaultDungeonClear {
		return false
	}
	return runtime.Tournament.isActive()
}

// tournamentClearIntentDecision 是"这一次能不能提交通关 intent"的判定结果。
type tournamentClearIntentDecision struct {
	// Allowed 为真表示本次可以走既有副本级通关提交链。
	Allowed bool
	// Reason 是具名 reason（只用于日志；Allowed 为真时为空）。
	Reason string
}

// evaluateCurrentTournamentClearIntentLocked 复刻厂商
// `TournamentDungeonApplicationService.TryCreateClearIntent`（:275-317）的硬条件，
// 但**第三条 `rewards.IsSelectionComplete` 已按族内实机证据去掉**（见函数体内注释）：
//
//	sourcePlayerId > 0 && runtime.IsChampion           ← 保留
//	&& rewards.IsSelectionComplete                     ← **去掉**（真机客户端会死环）
//
// 外加厂商在 `CompleteTournamentResultAsync`（:593-602）显式分出去的那条：
// **非冠军（被淘汰）只记日志，绝不创建 DungeonClearIntent**。
//
// 默认档（Tournament == nil）⇒ Allowed=true，调用方控制流不变。
func evaluateCurrentTournamentClearIntentLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) tournamentClearIntentDecision {
	if runtime == nil || runtime.Tournament == nil {
		// 默认档：不是锦标赛，调用方走原有控制流。
		return tournamentClearIntentDecision{Allowed: true}
	}
	if session == nil || session.selectedCharacterID == 0 {
		return tournamentClearIntentDecision{Reason: "selected_character_is_missing"}
	}
	snapshot := runtime.Tournament.snapshot()
	switch {
	case snapshot.Terminal == tournamentEliminated:
		return tournamentClearIntentDecision{Reason: "tournament_result_is_eliminated"}
	case snapshot.Terminal != tournamentChampion:
		return tournamentClearIntentDecision{Reason: "tournament_is_not_decided_yet"}
	}
	if runtime.tournamentReward == nil {
		return tournamentClearIntentDecision{Reason: "participant_reward_state_is_not_frozen"}
	}
	// ⚠️ **不再以"选奖是否选完"为通关前置**（本批按族内实机证据解环）。
	//
	// 厂商 `TryCreateClearIntent` 的第三条 `rewards.IsSelectionComplete` 只对**厂商
	// A21 客户端**成立：那个客户端的选奖握手由服务端先发 389 触发。真机 90CN
	// 客户端不是这样 —— 族内 0.1.8 的实机验收（
	// `90原神开源0.1.8\dnf90-source-oneclick-main\changlog.md:438-440`）证明：
	//
	//	"Live acceptance reached the native two-chest reward screen, then stalled
	//	 because the client emitted bodyless command 457 … The terminal flow must
	//	 therefore answer the verified nine-byte 01C9 state-6 projection before
	//	 permitting exit" ⇒ 客户端要先看到**通关**才进宝箱界面、才发 457。
	//
	// 本仓此前的接线把通关压到"选奖结束"，形成死环：
	//   服务端等客户端选奖（457/458）⇄ 客户端等通关序列（op115/空 op31）才进奖励界面。
	// 线上实证（2026-09-27，conn `game-000006`）：第 4 轮 op39 后服务端发 388，
	// 客户端回 op117 被 `reward_selection_is_not_complete` 拒掉，此后除物品拾取外
	// **再无任何上行**，客户端假死。
	//
	// 解环口径：冠军落定即可通关（`beginCurrentTournamentRewardLocked` 里自驱提交，
	// 见那里的顺序注释）；选奖退化为**纯投影 + 发奖**握手（457 回选奖态、458 落奖、
	// 3s 兜底自动选），不再影响 run 的终局。
	if runtime.bossDieCheckAccepted || runtime.ordinaryFinalRoomClearAccepted {
		// 已经提交过（幂等重放）。
		return tournamentClearIntentDecision{Reason: "dungeon_clear_was_already_committed"}
	}
	if len(runtime.Tournament.PathActors) == 0 {
		return tournamentClearIntentDecision{Reason: "tournament_path_actors_are_empty"}
	}
	return tournamentClearIntentDecision{Allowed: true}
}

// submitTournamentClearIntentLocked 是**唯一**允许"从零开始提交通关 intent"的入口
// （厂商 `TournamentDungeonCoordinator.cs:574-587 SubmitClearIntentAsync`）：
// 自己判定（`evaluateCurrentTournamentClearIntentLocked`）并在允许时驱动既有提交链。
//
// 当前的调用点只有 `completeCurrentTournamentResultLocked`（选奖结束那一刻，厂商
// `:397 await CompleteTournamentResultAsync`）。op39 / op117 两条路径**不能**用它 ——
// 那两条路自己随后就会走既有提交序列，重复调用会提交两次；它们用
// `evaluateCurrentTournamentClearIntentLocked` 只问不提交。
func (s *Service) submitTournamentClearIntentLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	targetObjectKey uint32,
	source string,
) (tournamentClearIntentDecision, error) {
	decision := evaluateCurrentTournamentClearIntentLocked(session, runtime)
	if !decision.Allowed || runtime == nil || runtime.Tournament == nil {
		return decision, nil
	}
	// 厂商 `TryCreateClearIntent`（:291-298）：
	//   finalActor = PathActors[PathActors.Count - 1]
	//   finalSequence = FirstActorSequence + PathActors.Count - 1
	finalActor := runtime.Tournament.PathActors[len(runtime.Tournament.PathActors)-1]
	finalSequence := uint32(runtime.Tournament.boundFirstSequence()) +
		uint32(len(runtime.Tournament.PathActors)) - 1
	s.logGameEvent(session, "game-dungeon-tournament-clear-intent-accepted",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"completed_rounds", runtime.Tournament.snapshot().CompletedRounds,
		"final_actor_code", finalActor.Code,
		"final_actor_type", finalActor.Type,
		"final_actor_sequence", finalSequence,
		"source_actor_object_key", targetObjectKey,
		"source", source,
		"vendor_source", "vendor_TournamentDungeonCoordinator.cs:574-587_SubmitClearIntentAsync")
	err := s.submitCurrentTournamentClearCommitLocked(session, runtime, targetObjectKey, source)
	return tournamentClearIntentDecision{Allowed: true}, err
}

// submitCurrentTournamentClearCommitLocked 以**与 op39 权威死亡路径完全相同的**
// 提交链落地通关（`dungeon_boss_die_ordinary.go:59-86` 那一组赋值 +
// `completeCurrentDungeonBossDieCheckLocked`）。
//
// 为什么复用而不是另写一条：清关 receipt 的冻结逻辑（`ensureCurrentDungeonCompletionReceiptKey`
// 的 `clearMapCompletionKey`、`bossDieCheckTargetObjectKey`）必须与 op117 路径逐字段一致，
// 否则同一局会出现两个不同的 completion key。这里只**改写"谁来触发"**，
// 不新增任何写库路径。
//
// 与 op117 路径的唯一差异：`Session.Complete()` vs `Session.CompleteCurrentRoom()`。
// 锦标赛是单房间 maze（真档 5 张图 `[maze info][size] 1 1`），两者在单房间下等价；
// 用 `Complete()` 与 `completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked:59` 同源。
func (s *Service) submitCurrentTournamentClearCommitLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	targetObjectKey uint32,
	source string,
) error {
	if session == nil || runtime == nil || runtime.Session == nil || session.dungeon.runtime != runtime {
		return nil
	}
	if runtime.ordinaryFinalRoomClearAccepted || runtime.bossDieCheckAccepted {
		// 已经提交过：幂等 no-op（厂商 `GetOrCreateClearedFact` 的 `factCreated == false`
		// 语义 —— 清关事实只建一次，重放不再走一次 clear commit）。
		s.logGameEvent(session, "game-dungeon-tournament-clear-commit-replay",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_status", runtime.Session.Snapshot().Run.Status,
			"settlement_entry_sent", runtime.settlementEntrySent,
			"reason", "dungeon_clear_was_already_committed",
			"source", source)
		return nil
	}
	if runtime.townReturnPending || runtime.townReturnOp24Sent {
		s.logGameEvent(session, "game-dungeon-tournament-clear-intent-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", "town_return_is_already_pending",
			"source", source)
		return nil
	}
	if err := runtime.Session.Complete(); err != nil {
		return err
	}
	runtime.ordinaryFinalRoomClearAccepted = true
	runtime.bossDieCheckAccepted = true
	runtime.bossDieCheckRelatedActorObjectKey = currentSceneActorObjectKey(session.selectedCharacterID)
	runtime.bossDieCheckTargetObjectKey = uint16(targetObjectKey)
	runtime.bossDieCheckPending = false
	runtime.bossDieCheckPendingRequest = dungeoncmd.BossDieCheckRequest{}
	completed := runtime.Session.Snapshot()
	s.logGameEvent(session, "game-dungeon-tournament-clear-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", completed.Run.Current.String(),
		"map_id", completed.Scene.Map.Map.ID,
		"target_object_key", targetObjectKey,
		"run_status", completed.Run.Status,
		"completion_source", "tournament_reward_selection_complete",
		"op117_required", false,
		"next_stage", "persist_completion_then_reply_op115_then_bodyless_settlement_op31")
	return s.completeCurrentDungeonBossDieCheckLocked(
		session,
		runtime,
		"tournament_champion_reward_selection_complete",
	)
}

// completeCurrentTournamentResultLocked 是厂商
// `CompleteTournamentResultAsync`（`TournamentDungeonCoordinator.cs:589-605`）的等价物：
// 冠军 + 全部启用奖励已交付 ⇒ 提交通关 intent；被淘汰只记具名事件。
//
// `scene` 取当前会话场景（清怪已经在 op39 路径提交过，`Session.Complete()` 只改 run 状态）。
// 客户端在奖励界面上**不会**再发一次 op39/op117，所以这里必须由服务端自驱。
func (s *Service) completeCurrentTournamentResultLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) error {
	if session == nil || runtime == nil || runtime.Tournament == nil || runtime.Session == nil {
		return nil
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		s.logGameEvent(session, "game-dungeon-tournament-clear-intent-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", "current_dungeon_scene_is_unavailable")
		return nil
	}
	decision := evaluateCurrentTournamentClearIntentLocked(session, runtime)
	if !decision.Allowed {
		s.logGameEvent(session, "game-dungeon-tournament-clear-intent-rejected",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"reason", decision.Reason,
			"source", "tournament_reward_selection_complete",
			"vendor_source", "vendor_TournamentDungeonApplicationService.cs:275-317_TryCreateClearIntent")
		return nil
	}
	targetObjectKey := uint32(0)
	if pathActors := runtime.Tournament.PathActors; len(pathActors) != 0 {
		// 厂商 `:291-298` 的 finalSequence；op115 的 target object key 用的就是这个 actor 序号。
		targetObjectKey = uint32(runtime.Tournament.boundFirstSequence()) + uint32(len(pathActors)) - 1
	}
	s.logGameEvent(session, "game-dungeon-tournament-clear-intent-accepted",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"completed_rounds", runtime.Tournament.snapshot().CompletedRounds,
		"final_actor_sequence", targetObjectKey,
		"source", "tournament_reward_selection_complete",
		"vendor_source", "vendor_TournamentDungeonCoordinator.cs:589-605_CompleteTournamentResultAsync_SubmitClearIntentAsync")
	return s.submitCurrentTournamentClearCommitLocked(
		session,
		runtime,
		targetObjectKey,
		"tournament_reward_selection_complete",
	)
}

// ── 玩家死亡 ⇒ 淘汰（本批接线，任务 #2）────────────────────────────────────────
//
// 厂商链（逐行）：
//
//	`DungeonCombatHandler.cs:708-777 ResolveDeathRespawnDeadlineAsync`
//	  :737  var tournamentDecision = _tournament.ResolveDeathDeadline(session, run, runIdentity);
//	  :741  if (Action == Ignore) { CancelDeathRespawn; return; }          ← 失效 run/ticket
//	  :750  if (Action == PresentTournamentRewards) {
//	  :753        DungeonRunLifecycle.CancelDeathRespawn(session);
//	  :761        await _tournament.EnsureParticipantRewardsAsync(session, run, forceProjection: false);
//	  :765        return; }                                                ← **不回城**
//	  :768  CancelDeathRespawn; ReturnAsync(..., DeathRespawn, 0x007B)      ← 普通副本回城
//	`TournamentDungeonCoordinator.cs:66-110 ResolveDeathDeadline`
//	  :81-87  !IsTournamentRun ⇒ ReturnToTown
//	  :88-94  run.RunState != Active ⇒ Ignore
//	  :96     var transition = _application.ApplyElimination(run);
//	  :97-104 !transition.Handled && !IsTournamentTerminated ⇒ Ignore
//	  :106-109 ⇒ PresentTournamentRewards
//	`TournamentDungeonApplicationService.cs:138-145 ApplyElimination` → `runtime.TryEliminate()`
//	`TournamentDungeonRuntime.cs:326-349 TryEliminate`：CompletedRounds = max(0, CurrentRound-1)，
//	  终局 = Eliminated，终止态不可回退、重复调用是 no-op。
//
// 本仓对应路径是 `dungeon_death_return.go` 的复活倒计时：
//   - 布防点：`handleDungeonDieCharacter`（op40）→ `armCurrentDungeonDeathReturnLocked`（10s）
//   - 到期点：`fireCurrentDungeonDeathReturn`（timequeue 到期）
//   - 提前取消：`handleDungeonUseCoin`（复活币，op41）→ `completeCurrentDungeonReviveLocked`
//
// **默认档逐字节不变**：该函数第一行就是"非锦标赛 ⇒ ReturnToTown"，
// 调用方 `fireCurrentDungeonDeathReturn` 只在返回值不是 ReturnToTown 时才改写控制流。
type tournamentDeathDeadlineAction byte

const (
	// tournamentDeathDeadlineReturnToTown 是普通副本的原有语义（回城）。
	tournamentDeathDeadlineReturnToTown tournamentDeathDeadlineAction = iota
	// tournamentDeathDeadlinePresentTournamentRewards 是锦标赛终止态：取消回城，留在本局看奖励。
	tournamentDeathDeadlinePresentTournamentRewards
	// tournamentDeathDeadlineIgnore 是失效 run / 非 Active：什么都不做。
	tournamentDeathDeadlineIgnore
)

// resolveCurrentTournamentDeathDeadline 是厂商 `ResolveDeathDeadline` 的等价物。
// 调用方持有 `session.dungeon.mu`。
func (s *Service) resolveCurrentTournamentDeathDeadline(
	session *gameSession,
	runtime *runtimeDungeonState,
) tournamentDeathDeadlineAction {
	if session == nil || runtime == nil || runtime.Session == nil {
		return tournamentDeathDeadlineIgnore
	}
	if runtime.Tournament == nil {
		// 默认档 / 非锦标赛：厂商 `:81-87` 的同一条 —— ReturnToTown。
		return tournamentDeathDeadlineReturnToTown
	}
	if !dungeonTournamentEnabled() || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return tournamentDeathDeadlineIgnore
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		return tournamentDeathDeadlineIgnore
	}
	completedRounds, accepted := runtime.Tournament.tryEliminate()
	snapshot := runtime.Tournament.snapshot()
	if snapshot.Terminal == tournamentEliminated {
		if accepted {
			s.logGameEvent(session, "game-dungeon-tournament-eliminated",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"completed_rounds", completedRounds,
				"current_round", snapshot.CurrentRound,
				"path_actors", len(runtime.Tournament.PathActors),
				"clear_intent_submitted", false,
				"source", "vendor_DungeonCombatHandler.cs:737_ResolveDeathDeadline_ApplyElimination")
		} else {
			// 已经是终止态（重复 deadline）：厂商 `TryEliminate` 的 no-op 分支
			// （`TournamentDungeonRuntime.cs:486-488`）。仍然要点名，否则现场只能看到一个
			// "回城被吞掉"的黑盒。
			s.logGameEvent(session, "game-dungeon-tournament-eliminated",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"completed_rounds", completedRounds,
				"current_round", snapshot.CurrentRound,
				"already_terminated", true,
				"clear_intent_submitted", false,
				"reason", "tournament_terminal_result_is_immutable")
		}
		return tournamentDeathDeadlinePresentTournamentRewards
	}
	if runtime.Tournament.isChampion() {
		// 冠军已落定、玩家在颁奖/选奖期间死亡：厂商同一条路（`:97` 的
		// `IsTournamentTerminated` 为真 ⇒ 不 Ignore）⇒ 回奖励界面、不回城。
		// 冠军仍然可以提交通关 intent（奖励选择完成时），这条分支不会把它挡掉。
		return tournamentDeathDeadlinePresentTournamentRewards
	}
	// 赛制还 Active 但 `TryEliminate` 没有接受（理论上不可达：Active 一定接受）：
	// 按厂商 `:97-104` 的 !Handled && !IsTerminated ⇒ Ignore。
	return tournamentDeathDeadlineIgnore
}

// presentCurrentTournamentRewardsOnDeathLocked 处理"死亡倒计时到期 ⇒ 淘汰 ⇒ 回奖励界面"。
// 调用方持有 `session.dungeon.mu`，且已经判定 action == PresentTournamentRewards。
//
// **不需要**再补一条 389：奖励态在赛制终局那一刻就已经冻结下发
// （`beginCurrentTournamentRewardLocked`，厂商 `EnsureParticipantRewardsAsync` 的
// `SendClearRewardAsync` 也在 `OnActorDeathAsync` 里发过一次）。这里只负责：
//
//	① 取消死亡回城 timer（厂商 `:753` 的 `CancelDeathRespawn`）；
//	② 保证赛制已冻结在终止态（淘汰时由 `resolveCurrentTournamentDeathDeadline` 写入）；
//	③ 记一条具名事件，说明"这一次死亡没有回城，而是进了锦标赛失败终止"。
func (s *Service) presentCurrentTournamentRewardsOnDeathLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	generation uint64,
) {
	if s == nil || session == nil || runtime == nil || runtime.Tournament == nil {
		return
	}
	s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "tournament_death_deadline_present_rewards")
	snapshot := runtime.Tournament.snapshot()
	s.logGameEvent(session, "game-dungeon-tournament-death-deadline-enter-settlement",
		"char_id", runtime.Character.CharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"run_token", runtime.lifecycleToken,
		"timer_generation", generation,
		"completed_rounds", snapshot.CompletedRounds,
		"current_round", snapshot.CurrentRound,
		"terminal", int(snapshot.Terminal),
		"champion", snapshot.Terminal == tournamentChampion,
		"town_return_performed", false,
		"clear_intent_submitted", false,
		"reward_state_frozen", runtime.tournamentReward != nil,
		"source", "vendor_DungeonCombatHandler.cs:750-766_present_tournament_rewards")
}

// shouldDeferTournamentRewardAutoSelect 判定"这一次自动选奖能不能开跑"。与
// `applyCurrentTournamentRewardSelectionLocked` 的前置条件保持一致，避免定时器
// 在赛制未终局 / 奖励态未冻结时误触发。
func shouldDeferTournamentRewardAutoSelect(
	session *gameSession,
	runtime *runtimeDungeonState,
	state *tournamentRewardState,
) (bool, string) {
	if session == nil || runtime == nil || runtime.Session == nil || state == nil {
		return true, "tournament_reward_state_is_unavailable"
	}
	if runtime.Tournament == nil {
		return true, "active_run_is_not_a_tournament"
	}
	if runtime.tournamentReward != state {
		return true, "tournament_reward_state_was_replaced"
	}
	if runtime.Tournament.isActive() {
		return true, "tournament_is_not_decided_yet"
	}
	if state.isSelectionComplete() {
		return true, "reward_selection_is_already_complete"
	}
	// ⚠️ **不再要求 run 还是 Active**（本批）：冠军落定即通关（`beginCurrentTournamentRewardLocked`
	// 自驱提交），所以 457 到达时 run 通常已经 `Completed` —— 旧口径会以
	// `dungeon_run_is_not_active` 把兜底自动选奖也一起掐掉，客户端万一不点宝箱
	// 就永远拿不到锦标赛卡牌奖励。卡牌交付走的是既有的
	// `grantDungeonCardRewardBundle`（背包事务边界），不依赖 run 状态。
	// 现场：2026-09-27 22:24 CN 线上 `reward-auto-select-skipped reason=dungeon_run_is_not_active`。
	if !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return true, "tournament_run_owner_mismatch"
	}
	return false, ""
}

// cancelCurrentTournamentRewardAutoSelectTimerLocked 取消 3000ms 自动选奖计时器。
// 与 `cancelCurrentDungeonCardAutoFlipLocked`（`dungeon_card_flow.go:513-536`）
// 同一形状：先作废代际，再删队列项，保证"已出队但还在等锁"的回调也会失效。
func (s *Service) cancelCurrentTournamentRewardAutoSelectTimerLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source string,
) {
	if runtime == nil {
		return
	}
	timerName := runtime.tournamentRewardAutoSelectTimerName
	if timerName == "" {
		// 没有在途计时器 ⇒ 完全 no-op：**不**推进代际、不记日志。
		// 这条保证默认档（以及任何没布防过的 vendor 局面）逐字节零变化；
		// 真实流程里只有 `armCurrentTournamentRewardAutoSelectTimerLocked` 成功之后
		// 才会有 timerName，因此"有票就作废代际"的语义没有丢。
		return
	}
	runtime.tournamentRewardAutoSelectTimerName = ""
	runtime.tournamentRewardAutoSelectGeneration++
	hadState := runtime.tournamentReward != nil
	if s == nil {
		return
	}
	cancelled := false
	if queue := s.ensureGameplayTimeQueue(); queue != nil {
		cancelled = queue.Cancel(timerName)
	}
	s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-cancelled",
		"char_id", runtime.Character.CharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"generation", runtime.tournamentRewardAutoSelectGeneration,
		"timer_name", timerName,
		"queue_item_cancelled", cancelled,
		"reward_state_present", hadState,
		"source", source)
}

// autoSelectCurrentTournamentRewardsLocked 是厂商 `OnRewardSelectionTimerElapsedAsync`
// （`TournamentDungeonCoordinator.cs:493-549`）的等价物：对**每个启用且尚未选择**的
// 卡类选 `cardIndex: 0`，走与手动 0x01CB 完全相同的 reserve/deliver/commit 入口
// （本仓即 `applyCurrentTournamentRewardSelectionLocked`，来源固定
// `tournamentRewardSelectionSourceTimer` = **unsolicited**：这条路径**不发 458**，
// 只沿用改动前的 9 字节 457 选奖态应答）。
//
// 它与真机客户端并存时的作用被刻意收窄成"**兜底落库**"：玩家不点也能拿到奖励；
// 而玩家自己点（solicited）时由 `sendCurrentTournamentRewardSelectionAckLocked`
// 回 458 选中投影，客户端据此把卡标成已选并推进/收尾 —— 包括"这一组已经被兜底发掉"
// 的重复出口（见 `applyCurrentTournamentRewardSelectionLocked` 的重复分支）。
//
// 选完最后一张后，`applyCurrentTournamentRewardSelectionLocked` 自己会走
// `completeCurrentTournamentResultLocked`，与手动选择同一条终局路径。
func (s *Service) autoSelectCurrentTournamentRewardsLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source tournamentRewardSelectionSource,
) {
	if s == nil || session == nil || runtime == nil {
		return
	}
	state := runtime.tournamentReward
	if state == nil {
		return
	}
	if deferred, reason := shouldDeferTournamentRewardAutoSelect(session, runtime, state); deferred {
		s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-skipped",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", reason,
			"source", source.String())
		return
	}
	for cardType := byte(0); cardType < tournamentCardTypeCount; cardType++ {
		if !state.isCardTypeEnabled(int(cardType)) || state.Selected[cardType] != tournamentUnselectedCard {
			continue
		}
		s.applyCurrentTournamentRewardSelectionLocked(session, runtime, cardType, 0, source)
	}
}

// armCurrentTournamentRewardAutoSelectTimerLocked 在**成功回出选奖态之后**布防
// 一次性 3000ms 自动选奖计时器。
//
// 厂商对拍：
//
//	`TournamentDungeonCoordinator.cs:40`   RewardSelectionTimeoutMilliseconds = 3000
//	`:214-241 HandleRewardSelectStateAsync` … 回 0x01CA 之后 `StartRewardSelectionTimer`
//	`:400-441 StartRewardSelectionTimer`  `run.Timers.TryGetCurrentTicket(...) == true ⇒ return`
//	                                      （已有票 ⇒ 不重开，绝对 deadline 不变）
//	`:471-491 ScheduleRewardSelectionTimer`  ClockService 一次性 timer + ticket 绑定
//	`:493-549 OnRewardSelectionTimerElapsedAsync` 逐卡类选 index 0
//
// **本仓与厂商的一处刻意差异**：本仓没有跨会话的 run timer 票据/绝对 deadline 持久化，
// 自动选奖计时器是**会话内一次性**的（与 `armCurrentDungeonCardAutoFlipLocked` 同构）。
// 因此"重连后以原 deadline 恢复"在本仓退化为"重连后重新布防 3000ms"
// （见 `handleCurrentTournamentRewardCommand` 的空 body 分支 = 厂商 `RecoverAsync` 的补发形态）。
func (s *Service) armCurrentTournamentRewardAutoSelectTimerLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) {
	if s == nil || session == nil || runtime == nil ||
		session.dungeon.runtime != runtime || runtime.tournamentReward == nil {
		return
	}
	if runtime.tournamentRewardAutoSelectTimerName != "" {
		// 已有票：厂商 `:408-413` 的 `TryGetCurrentTicket ⇒ return`（绝对 deadline 不变）。
		return
	}
	if deferred, reason := shouldDeferTournamentRewardAutoSelect(session, runtime, runtime.tournamentReward); deferred {
		s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-skipped",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", reason,
			"source", "arm_reward_auto_select_timer")
		return
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-schedule-failed",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", "gameplay_time_queue_is_unavailable")
		return
	}
	runtime.tournamentRewardAutoSelectGeneration++
	generation := runtime.tournamentRewardAutoSelectGeneration
	timerName := fmt.Sprintf(
		"dnf-dungeon-tournament-reward-auto-select:%s:run:%d",
		session.connID,
		runtime.lifecycleToken,
	)
	characterID, characterGeneration, err := gameSessionCharacterEventIdentity(
		session,
		session.selectedCharacterID,
	)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-schedule-failed",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", "character_event_identity_is_unavailable",
			"error", err)
		return
	}
	runtime.tournamentRewardAutoSelectTimerName = timerName
	runtime.tournamentRewardAutoSelectDueAt = queue.Now().Add(currentTournamentRewardAutoSelectDelay)
	dueAt := runtime.tournamentRewardAutoSelectDueAt
	dungeonID := runtime.Dungeon.ID
	runToken := runtime.lifecycleToken
	if err := queue.ScheduleAfter(timerName, currentTournamentRewardAutoSelectDelay, func(time.Time) {
		postErr := s.postGameSessionCharacterEvent(
			session,
			"dungeon-tournament-reward-auto-select",
			characterID,
			characterGeneration,
			func() error {
				session.dungeon.mu.Lock()
				defer session.dungeon.mu.Unlock()
				if session.dungeon.runtime != runtime ||
					runtime.tournamentRewardAutoSelectGeneration != generation ||
					runtime.lifecycleToken != runToken {
					return nil
				}
				runtime.tournamentRewardAutoSelectTimerName = ""
				s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-fired",
					"char_id", characterID,
					"dungeon_id", dungeonID,
					"run_token", runToken,
					"timer_generation", generation,
					"source", "timequeue_one_shot_reward_selection_timeout")
				s.autoSelectCurrentTournamentRewardsLocked(
					session,
					runtime,
					tournamentRewardSelectionSourceTimer,
				)
				return nil
			},
		)
		if postErr != nil && !isClosedGameSessionEventError(postErr) {
			s.logPacketEvent("game-dungeon-tournament-reward-auto-select-submit-failed",
				"conn_id", session.connID,
				"char_id", characterID,
				"dungeon_id", dungeonID,
				"error", postErr)
		}
	}); err != nil {
		runtime.tournamentRewardAutoSelectTimerName = ""
		s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-schedule-failed",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", "timequeue_schedule_failed",
			"error", err)
		return
	}
	s.logGameEvent(session, "game-dungeon-tournament-reward-auto-select-scheduled",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"run_token", runtime.lifecycleToken,
		"timer_generation", generation,
		"timer_name", timerName,
		"due_at_utc", dueAt.UTC().Format(time.RFC3339Nano),
		"delay_ms", currentTournamentRewardAutoSelectDelay.Milliseconds(),
		"source", "vendor_TournamentDungeonCoordinator.cs:400-441_StartRewardSelectionTimer")
}

// 厂商发送点：`Network\Handlers\Dungeon\DungeonMapHandler.cs:1033-1077`，由
// `:932-934` 的 `sendTournamentProjection = tournament != null && sentMapId == tournament.Definition.MapId`
// 决定；它**替代**了普通 START_MAP —— 厂商在锦标赛地图上根本不组装也不发送 START_MAP
// （`:778-779 startMapBody = Array.Empty<byte>(); if (!isBloodAltarMap && !isTournamentMap)`、
// `:943 if (!sendTournamentProjection) { … 发 START_MAP … }`）。
//
// 本仓从 P8 起与厂商**同形**：锦标赛地图上普通 op29 由
// `currentTournamentMapSuppressesOrdinaryStartMap` 抑制
// （见 `dungeon_tournament_entry.go` 的证据块），本函数发出的 386/387 就是
// "房间 + actor"的**唯一**公告者。op39 的 owner 校验不受影响：它读
// `runtime.Room`（`AnnounceAllActors` 的绑定，无论发不发 op29 都会执行）与
// `runtime.Tournament.firstSequence`（`dungeon_runtime_state.go:470-484` 绑到
// `room.monsters[0].ObjectKey`），而 386 的路径 actor 段写的正是
// `firstSequence + i` ⇒ `sequence` 空间与 `ObjectKey` 空间逐值相同。
//
// 返回值语义（**不许静默降级**）：
//   - 默认档 / 非锦标赛地图 / 非本定义地图 ⇒ `(nil)`：一个字节都不发，控制流不变；
//   - 本定义地图但投影发不出去 ⇒ 返回**具名错误**（同时已经写了
//     `game-dungeon-tournament-start-map-projection-blocked`）。调用方在"op29 被抑制"
//     的那条路径上**必须**把它升级成硬失败：那时 386/387 是唯一公告者，
//     "没发替代包却继续进场"正是任务书禁止的静默降级。
func (s *Service) sendCurrentTournamentStartMapProjection(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	revisit bool,
) error {
	if !dungeonTournamentEnabled() || session == nil || runtime == nil || runtime.Tournament == nil {
		return nil
	}
	definition := runtime.Tournament.Definition
	if definition == nil || scene.Map.Map.ID != definition.MapID {
		return nil
	}
	firstSequence := runtime.Tournament.boundFirstSequence()
	if firstSequence == 0 {
		s.logGameEvent(session, "game-dungeon-tournament-start-map-projection-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"map_id", scene.Map.Map.ID,
			"reason", "tournament_actor_sequence_is_not_bound")
		return fmt.Errorf("tournament actor sequence is not bound: dungeon=%d map_id=%d",
			runtime.Dungeon.ID, scene.Map.Map.ID)
	}
	infoBody, err := buildTournamentInfoBody(runtime.Tournament, runtime.Request.Difficulty, firstSequence)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-start-map-projection-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"map_id", scene.Map.Map.ID,
			"msg_id", a21NotiTournamentInfo,
			"error", err)
		return fmt.Errorf("build tournament info body: %w", err)
	}
	if err := s.sendGameUpperRawClass(session, a21NotiTournamentInfo, infoBody, 0); err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-start-map-projection-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"msg_id", a21NotiTournamentInfo,
			"error", err)
		return fmt.Errorf("send tournament info: %w", err)
	}
	mapBody := buildTournamentMapInfoBody(
		byte(scene.Coordinate.X),
		byte(scene.Coordinate.Y),
		runtime.Seed,
		definition.MapID,
		revisit,
	)
	if err := s.sendGameUpperRawClass(session, a21NotiTournamentMapInfo, mapBody, 0); err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-start-map-projection-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"msg_id", a21NotiTournamentMapInfo,
			"error", err)
		return fmt.Errorf("send tournament map info: %w", err)
	}
	s.logGameEvent(session, "game-dungeon-tournament-start-map-projection-sent",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"map_id", scene.Map.Map.ID,
		"info_msg_id", a21NotiTournamentInfo,
		"info_body_len", len(infoBody),
		"map_info_msg_id", a21NotiTournamentMapInfo,
		"map_info_body_len", len(mapBody),
		"first_sequence", firstSequence,
		"path_actors", len(runtime.Tournament.PathActors),
		"revisit", revisit,
		"inserted_between", "op28_dungeon_info_and_op29_start_map",
		"replaces", "ordinary_start_map_op29_suppressed_on_tournament_map",
		"source", "vendor_DungeonMapHandler.cs:1033-1077_TOURNAMENT_INFO_TOURNAMENT_MAP_INFO")
	return nil
}
