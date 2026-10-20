package dnfbridge

import (
	"context"
	"errors"
	"fmt"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ── 锦标赛通关经验的落库（P10）───────────────────────────────────────────────
//
// # 缺口
//
// `beginCurrentTournamentRewardLocked`（`dungeon_tournament_flow.go:236-251`）把
// `[tournament clear reward exp]` 里 `CompletedRounds` 对应的值写进 NOTI 389 的
// body（`u32 RewardExperience`），`:282` 记 `reward_experience` —— **之后没有任何
// 一笔把角色累计经验改掉的事务**。也就是说 389 里的经验在过去只是 UI 数字。
//
// # 厂商对拍（逐行）
//
//	Network\Handlers\Dungeon\TournamentDungeonCoordinator.cs:159-212 EnsureParticipantRewardsAsync
//	  :180-181  if (!rewards.ExperienceDelivered) await TryDeliverExperienceAsync(session, run, rewards);
//	  :191-211  …… 之后才做 `SendClearRewardAsync`（NOTI 389 的投影预约）
//	同文件 :607-662 TryDeliverExperienceAsync
//	  :612      if (!_application.TryReserveExperience(run, out var reservedState)) return;   ← 一次性预定
//	  :619-638  reservedState.RewardExperience > 0 ⇒
//	            CharacterExperienceCommitService.TryCommitTournamentExperience(...)
//	            （失败 ⇒ throw；catch 里 :645 reservedState.TryRollbackExperience()）
//	  :639-641  TryMarkExperienceDelivered() —— 交付检查点
//	  :652-656  SendExpGrantNotificationAsync(..., "TOURNAMENT_CLEAR_REWARD")
//	  :657-661  grant.LeveledUp == true ⇒ SendInDungeonLevelUpFollowups
//	Game\Progression\CharacterExperienceCommitService.cs:9-76 TryCommitTournamentExperience
//	  :20-25    硬条件：lease/player/characterId/experienceService 都在，且 **rawGain != 0**
//	  :31-52    OnlineInventoryMutationCommitCoordinator.TryCommit(lease, "tournament-experience", …)
//	            ⇒ CharacterExperienceService.GrantInTransaction(connection, transaction,
//	              characterId, accountId, player.Level, player.Exp, rawGain, normalizeMaxLevelExp: true)
//	  :60-61    player.Level / player.Exp 用事务结果回写（内存态与库态同步）
//	Game\Dungeon\Tournament\TournamentParticipantRewardState.cs:13-18 / :217-254
//	  TournamentExperienceDeliveryState { NotReserved, Reserved, Delivered } 三态 ——
//	  这就是厂商的幂等键：**同一份 run 级奖励态对象**只能 reserve 一次。
//
// **厂商没有给通关经验乘任何加成**（`rawGain` 原样进 `GrantInTransaction`），
// 所以本仓的 `GrowthContractBonus` 在这一条路径上恒为 0；成长契约只作用于
// 怪物击杀经验（`commitCurrentDungeonMonsterExperience`）。
//
// # 本仓的等价物（复用而非另写）
//
//   - 提交实现：`commitCurrentDungeonExperience`（`dungeon_monster_experience.go`）
//     —— 与怪物击杀经验**同一份**升级阈值 / 经验上限 / 名誉 Expert 溢出 /
//     SP·TP 账本 / `progression.Commit` 的角色 progression 单一事务。
//   - 资源装载：`currentDungeonExperienceCommitResources`（同一份运行时 PVF 表 + 同一
//     个 `sync.Map` 缓存）。
//   - 幂等键：本仓没有厂商那种"durable 的 run 级奖励态对象"，所以用**本次 run 的
//     既有标识**组合成一次性凭据 `tournamentExperienceReceipt.Key`
//     （`run_token` + 角色 + 副本 + 完成轮数）。凭据取不到（`run_token == 0`）
//     时**明确拒绝提交并记错误**，绝不"大概齐"再加一次。
//   - 客户端可见：一次提交成功后，用既有 `currentDungeonCharacterStateMsgID`
//     （class0/op37，`dnfenum.CmdPacketFinishLoading`）绝对角色状态投影回写等级 /
//     累计经验 / 剩余 SP·TP —— 厂商对应 `SendExpGrantNotificationAsync` +
//     `SendInDungeonLevelUpFollowups`（`:652-661`）。

// tournamentExperienceReceipt 是"这一局锦标赛的通关经验已经提交过"的**一次性凭据**。
//
// 它故意与 `runtimeDungeonState.tournamentReward` 分开：奖励态可能在
// "客户端重发选奖态请求 / 重连补发 389"这些路径上被重建，而经验提交在一整局里
// 只能发生一次。凭据只在真正拿到可用的 run 标识时才被写入。
type tournamentExperienceReceipt struct {
	// Attempted 报告本局是否已经尝试过提交（无论成功、失败还是幂等命中）。
	// 它保证"数据库失败"不会被下一次结算重放变成第二笔经验：
	// 提交失败时我们**无法**知道事务是否落了一半，所以本局不再重试。
	Attempted bool
	// Committed 报告本局的经验是否真的落库（`progression.Commit` 返回 Applied 或
	// Idempotent）。失败与幂等命中的区别由 `Reason` 承载。
	Committed bool
	// Key 是本次 run 的结算凭据（`tournamentExperienceReceiptKey`）。
	Key string
	// Reason 是具名结论：failed / duplicate 时为具体原因，成功时为空。
	Reason string
	// Gain 是本次实际提交的经验值（真档 `[tournament clear reward exp]` 的原值）。
	Gain uint32
}

// tournamentExperienceReceiptKey 组合本次 run 的通关经验凭据。
//
// 用到的四个数都是**既有标识**，没有新造：
//   - `runToken`：`runtime.lifecycleToken`，来自 `session.dungeon.runToken`
//     （`dungeon_runtime_state.go:584` 在 `commitDungeonRuntime` 里冻结，
//     与死亡回城 / 自动选奖计时器用的是同一个 token）；
//   - 角色 / 副本：`char_id` 与 `dungeon_id`；
//   - `completedRounds`：真档 `[tournament clear reward exp]` 的索引键（0..4）。
//
// 纯函数，便于用例直接钉住"同一次 run ⇒ 同一个键、换一局 ⇒ 换键"。
func tournamentExperienceReceiptKey(
	characterID uint16,
	dungeonID int64,
	runToken uint64,
	completedRounds byte,
) string {
	return fmt.Sprintf("tournament-clear-experience:char:%d:dungeon:%d:run_token:%d:completed_rounds:%d",
		characterID, dungeonID, runToken, completedRounds)
}

// commitCurrentTournamentClearRewardExperienceLocked 把"389 里报给客户端的经验"
// 真正提交到角色账上。
//
// **调用方必须持有 `session.dungeon.mu`**，且必须位于
// `beginCurrentTournamentRewardLocked` 的 389 发送点上（厂商的顺序是
// `TryDeliverExperienceAsync` → `SendClearRewardAsync`，`:180-181` 在 `:189` 之前）。
//
// 幂等凭据**只**存在 `runtime.tournamentExperience` 上（本函数不接受外部凭据参数）：
// 奖励态与凭据必须同生命周期，否则"重复结算 ⇒ 第二笔经验"。
//
// 返回值语义：
//   - `(true, result, nil)`：本局第一次提交成功（Applied 或 Idempotent）。
//     调用方据此把绝对角色状态（class0/op37）回给客户端。
//   - `(false, _, nil)`：未提交。`reason` 是具名原因，调用方必须已经由本函数
//     记录过具名事件。包括：开关关闭 / 非锦标赛 / 会话与 runtime 不匹配 /
//     `run_token` 缺失 / 奖励态为 0 / 已尝试过（幂等命中）。
//   - `(false, _, err)`：本次**真的尝试了**提交但失败（事务错误、状态不一致、
//     PVF 表装载失败…）。调用方只记日志，不再重试。
//
// 本函数自己不做任何算术：真档给多少就是多少，升级/溢出/SP·TP 全部由
// `commitCurrentDungeonExperience` 决定。
func (s *Service) commitCurrentTournamentClearRewardExperienceLocked(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
	rewardExperience uint32,
	completedRounds byte,
) (bool, currentDungeonMonsterExperienceCommitResult, error) {
	if s == nil || session == nil || runtime == nil {
		return false, currentDungeonMonsterExperienceCommitResult{}, nil
	}
	// 凭据**只**从 runtime 上取：它必须与 `runtime.tournamentReward` 同生命周期。
	// 曾经把凭据当参数传进来（调用方还传了一份局部拷贝），结果是幂等闸门永远
	// 不生效、重复结算给第二笔经验 —— 那条缺陷是被本批"同一 run 只提交一次"
	// 的真档用例抓出来的，所以这里刻意不给调用方留第二个入口。
	receipt := &runtime.tournamentExperience
	reject := func(reason string) (bool, currentDungeonMonsterExperienceCommitResult, error) {
		receipt.Reason = reason
		s.logGameEvent(session, "game-dungeon-tournament-clear-experience-rejected",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", completedRounds,
			"reward_experience", rewardExperience,
			"committed", false,
			"reason", reason,
			"source", "vendor_TournamentDungeonCoordinator.cs:607-662_TryDeliverExperienceAsync")
		return false, currentDungeonMonsterExperienceCommitResult{}, nil
	}

	if !dungeonTournamentEnabled() {
		// 默认档：本函数在默认档根本不可达（`beginCurrentTournamentRewardLocked`
		// 首行 `runtime.Tournament == nil` 就返回），这里只是纵深防御。
		return reject("tournament_mode_is_not_vendor")
	}
	if runtime.Tournament == nil {
		return reject("active_run_is_not_a_tournament")
	}
	if receipt.Attempted {
		// 厂商 `TournamentParticipantRewardState.TryReserveExperience`（`:217-228`）：
		// 已经是 Reserved/Delivered ⇒ 返回 false，`TryDeliverExperienceAsync` 直接
		// `return` 什么也不做。本仓的等价物就是这一条 early-return。
		receipt.Reason = "tournament_experience_was_already_attempted_for_this_run"
		s.logGameEvent(session, "game-dungeon-tournament-clear-experience-duplicate",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", completedRounds,
			"reward_experience", rewardExperience,
			"committed", receipt.Committed,
			"receipt_key", receipt.Key,
			"reason", receipt.Reason,
			"source", "vendor_TournamentParticipantRewardState.cs:217-228_TryReserveExperience")
		return false, currentDungeonMonsterExperienceCommitResult{}, nil
	}

	// ① run 标识：取不到就**明确拒绝**，绝不用"大概齐"的键再加一笔。
	runToken := runtime.lifecycleToken
	if runToken == 0 {
		return reject("tournament_run_token_is_missing")
	}
	if session.dungeon.runtime != runtime {
		return reject("dungeon_runtime_is_not_the_session_owner")
	}
	if !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return reject("dungeon_runtime_does_not_own_the_selected_character")
	}
	if !s.currentDungeonRuntimeCharacterIsCommitReady(session, runtime) {
		return reject("runtime_character_state_is_not_commit_ready")
	}

	// ② 本局的凭据（既有 run/settlement 标识组合）。
	key := tournamentExperienceReceiptKey(
		session.selectedCharacterID, runtime.Dungeon.ID, runToken, completedRounds)
	receipt.Key = key

	// ③ 真档给 0 ⇒ 没有可提交的经验。厂商同一口径：`TournamentDungeonCoordinator.cs:619`
	//    只在 `reservedState.RewardExperience > 0` 时才提交；`CharacterExperienceCommitService.cs:24`
	//    也把 `rawGain == 0` 当作"什么都不做"。这里仍然点名，不静默。
	if rewardExperience == 0 {
		receipt.Attempted = true
		receipt.Committed = true
		receipt.Reason = "tournament_clear_reward_experience_is_zero"
		receipt.Gain = 0
		s.logGameEvent(session, "game-dungeon-tournament-clear-experience-zero",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", completedRounds,
			"reward_experience", rewardExperience,
			"committed", false,
			"receipt_key", key,
			"reason", receipt.Reason,
			"vendor_behavior", "reward_experience_0_commits_nothing_and_marks_delivered",
			"source", "vendor_TournamentDungeonCoordinator.cs:619-621")
		return false, currentDungeonMonsterExperienceCommitResult{}, nil
	}

	// ④ 一次性预定：从这里开始"本局已经尝试过"。即使下面失败，也不再重试
	//    （厂商 `TryRollbackExperience` 会允许重试，因为它能在同一进程里查询
	//    reservation 是否丢失；本仓拿不到这个信息，重试可能变成第二笔经验 ——
	//    宁可本局少给，也不重复给）。
	receipt.Attempted = true
	receipt.Gain = rewardExperience

	resources, err := s.currentDungeonExperienceCommitResources()
	if err != nil {
		return failCurrentTournamentExperienceCommit(s, session, runtime, receipt, completedRounds, err)
	}
	// 与怪物击杀经验**同一份**提交实现：等级阈值、经验上限、名誉 Expert 溢出、
	// SP·TP 账本、`progression.Commit` 的乐观 version precondition 全部复用。
	// `GrowthContractBonus` 恒为 0（厂商 `rawGain` 原样提交）。
	result, err := s.commitCurrentDungeonExperience(ctx, session, runtime, resources.Progression,
		currentDungeonMonsterExperienceCommitResult{
			Award: currentDungeonMonsterExperienceAward{Gain: rewardExperience},
		})
	if err != nil {
		return failCurrentTournamentExperienceCommit(s, session, runtime, receipt, completedRounds, err)
	}
	receipt.Committed = true
	// 内存态与库态同步：与怪物击杀路径同一处赋值（`dungeon_combat_runtime.go:430`）。
	runtime.Character = dnfrepo.CloneCharacter(result.Character)
	s.logGameEvent(session, "game-dungeon-tournament-clear-experience-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"completed_rounds", completedRounds,
		"reward_experience", rewardExperience,
		"committed", true,
		"receipt_key", key,
		"run_token", runToken,
		"applied", result.Applied,
		"idempotent", result.Idempotent,
		"growth_contract_bonus", result.GrowthContractBonus,
		"honor_expert_gain", result.HonorExpertGain,
		"sp_gain", result.SPGain,
		"tp_gain", result.TPGain,
		"new_level", result.Character.Level,
		"new_total_exp", statU32(result.Character, "exp", 0),
		"remaining_sp", result.Skill.Points.RemainingSP,
		"remaining_tp", result.Skill.Points.RemainingTP,
		"vendor_behavior", "raw_gain_committed_without_growth_contract_bonus",
		"source", "vendor_CharacterExperienceCommitService.cs:9-76_TryCommitTournamentExperience")
	return true, result, nil
}

// failCurrentTournamentExperienceCommit 统一记录失败（具名事件 + 凭据回写），
// 供两个失败分支（资源装载 / 提交事务）共用。
func failCurrentTournamentExperienceCommit(
	s *Service,
	session *gameSession,
	runtime *runtimeDungeonState,
	receipt *tournamentExperienceReceipt,
	completedRounds byte,
	err error,
) (bool, currentDungeonMonsterExperienceCommitResult, error) {
	receipt.Committed = false
	receipt.Reason = "tournament_experience_commit_failed"
	s.logGameEvent(session, "game-dungeon-tournament-clear-experience-failed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"completed_rounds", completedRounds,
		"reward_experience", receipt.Gain,
		"committed", false,
		"receipt_key", receipt.Key,
		"reason", receipt.Reason,
		"retry_suppressed", true,
		"error", err,
		"source", "vendor_TournamentDungeonCoordinator.cs:643-647_catch_rollback_experience")
	return false, currentDungeonMonsterExperienceCommitResult{}, err
}

// errCurrentTournamentExperienceBodyShapeInvalid 只用于日志字段与本用例断言：
// 提交成功但 class0/op37 的体长不是既有常量时，本仓**不发**这个包（与怪物击杀
// 经验同一条判据，`dungeon_combat_runtime.go:438`）。它不代表提交失败。
var errCurrentTournamentExperienceBodyShapeInvalid = errors.New("current EXE tournament experience character-state body shape is invalid")

// sendCurrentTournamentExperienceCharacterStateLocked 在通关经验提交成功后把
// **绝对角色状态**（class0/op37）回给客户端：等级 / 累计经验 / 剩余 SP·TP。
//
// 厂商对拍：
//
//	`TournamentDungeonCoordinator.cs:652-656` SendExpGrantNotificationAsync(session, grant, "TOURNAMENT_CLEAR_REWARD")
//	同文件 :657-661 grant.LeveledUp == true ⇒ SendInDungeonLevelUpFollowups(session)
//
// 本仓没有单独的"经验获得通知"包，用的是与怪物击杀经验**完全相同**的
// class0/op37 投影（`currentDungeonCharacterStateMsgID` =
// `dnfenum.CmdPacketFinishLoading`，即 `buildCurrentFinishLoadingCharacterStateBody...`）。
//
// 传输失败**不改变已提交的事实**（厂商注释 `:649-651`："A transport failure must not
// make EXP grantable again"）：这里只记具名事件，不返回错误、不回滚经验。
func (s *Service) sendCurrentTournamentExperienceCharacterStateLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	result currentDungeonMonsterExperienceCommitResult,
	completedRounds byte,
) {
	if s == nil || session == nil || runtime == nil || !result.Applied && !result.Idempotent {
		return
	}
	body := buildCurrentFinishLoadingCharacterStateBodyWithPresentation(
		result.Character,
		result.Skill.Points,
		&currentFinishLoadingExperiencePresentation{
			GrowthContractBonus: result.GrowthContractBonus,
		},
	)
	if len(body) != currentFinishLoadingCharacterStateBodySize {
		s.logGameEvent(session, "game-dungeon-tournament-clear-experience-state-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", completedRounds,
			"committed", true,
			"reason", "current_exe_op37_body_shape_invalid_after_atomic_commit",
			"body_len", len(body),
			"error", errCurrentTournamentExperienceBodyShapeInvalid)
		return
	}
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, body, 0); err != nil {
		s.logGameEvent(session, "game-dungeon-tournament-clear-experience-state-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completed_rounds", completedRounds,
			"committed", true,
			"reason", "experience_state_transport_failed_but_experience_was_committed",
			"msg_id", currentDungeonCharacterStateMsgID,
			"body_len", len(body),
			"error", err)
		return
	}
	s.logGameEvent(session, "game-dungeon-tournament-clear-experience-state-sent",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"completed_rounds", completedRounds,
		"committed", true,
		"msg_id", currentDungeonCharacterStateMsgID,
		"body_len", len(body),
		"new_level", result.Character.Level,
		"new_total_exp", statU32(result.Character, "exp", 0),
		"remaining_sp", result.Skill.Points.RemainingSP,
		"remaining_tp", result.Skill.Points.RemainingTP,
		"sequence", "op39_tail_after_tournament_experience_commit",
		"source", "vendor_TournamentDungeonCoordinator.cs:652-661_exp_grant_and_level_up_followups")
}
