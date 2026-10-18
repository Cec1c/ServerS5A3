package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

var errCurrentFinishQuestAckShape = errors.New("current EXE finish-quest ACK shape is invalid")

// currentFinishQuestReplayKey scopes one terminal receipt to the selected
// character in one TCP session. Character selection can reuse its connection,
// so a bare quest ID would incorrectly suppress another character's receipt.
type currentFinishQuestReplayKey struct {
	characterID uint16
	questID     uint16
}

func newCurrentFinishQuestReplayKey(characterID uint16, request dnfquest.FinishQuestRequest) currentFinishQuestReplayKey {
	return currentFinishQuestReplayKey{characterID: characterID, questID: request.QuestID}
}

// currentFinishQuestReplay helpers keep the durable replay receipt scoped to
// reconnect recovery. A live same-session duplicate finish request (double
// click or client retry) must not receive the receipt a second time: the
// current EXE finish handler (sub_1D32120) drops the quest UI entry after the
// first receipt and NULL-derefs on the duplicate, killing the client
// (2026-07-25 quest 3157 crash at 0x1D3340F).
func (session *gameSession) currentFinishQuestAnswered(key currentFinishQuestReplayKey) bool {
	if session == nil {
		return false
	}
	session.questReplay.finishMu.Lock()
	defer session.questReplay.finishMu.Unlock()
	_, ok := session.questReplay.finishAnswered[key]
	return ok
}

func (session *gameSession) markCurrentFinishQuestAnswered(key currentFinishQuestReplayKey) {
	if session == nil {
		return
	}
	session.questReplay.finishMu.Lock()
	defer session.questReplay.finishMu.Unlock()
	if session.questReplay.finishAnswered == nil {
		session.questReplay.finishAnswered = make(map[currentFinishQuestReplayKey]struct{}, 1)
	}
	session.questReplay.finishAnswered[key] = struct{}{}
}

func (s *Service) handleCurrentFinishQuest(session *gameSession, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	// A2-3 分段计时（load / 结算事务 / ACK）：默认 off ⇒ timing == nil，所有打点
	// 都是空操作。defer 放在解码之前，因此每个 op34 请求恰好写一行，**包括**加载
	// 阶段就失败的请求（那种情况 load_ms 就是全部耗时）。见 quest_finish_timing.go。
	timing := startCurrentQuestFinishTiming()
	defer timing.log(s, session)
	request, err := dnfquest.DecodeFinishQuestRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-upper-finish-quest-blocked",
			"body_len", len(body),
			"reason", "current_exe_op34_requires_exact_plain_10_byte_body",
			"error", err)
		return nil
	}
	timing.markQuest(request.QuestID)
	// The current live ten-byte request ends in FF FF. The field's domain
	// meaning remains unproved, so validate only the observed wire marker.
	if request.QuestID == 0 || request.Multiplier != 1 || request.Reserved != dnfquest.CurrentFinishQuestObservedTailMarker {
		s.logGameEvent(session, "game-upper-finish-quest-blocked",
			"quest_id", request.QuestID,
			"multiplier", request.Multiplier,
			"reserved", request.Reserved,
			"reason", "current_exe_op34_semantic_fields_invalid")
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketFinishQuest), 22)
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil {
		s.logGameEvent(session, "game-upper-finish-quest-blocked",
			"quest_id", request.QuestID,
			"reason", "character_settlement_transaction_unavailable")
		return nil
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		s.logGameEvent(session, "game-upper-finish-quest-blocked",
			"quest_id", request.QuestID,
			"reason", "quest_catalog_unavailable",
			"error", err)
		// 加载阶段的 ctx 失败同样不许静默（与结算分支同一个开关、同一个失败包）。
		if currentFinishQuestContextFailureReason(err) != "" && currentQuestFinishFailureReplyEnabled() {
			return s.replyCurrentFinishQuestContextFailure(session, request.QuestID, "quest_catalog_load", timing, err)
		}
		return nil
	}
	if handled, linkedErr := s.handleCurrentLinkedObjectiveFinish(ctx, session, request, repositories, catalog); handled {
		if linkedErr != nil {
			s.logGameEvent(session, "game-upper-linked-objective-finish-blocked",
				"quest_id", request.QuestID,
				"reason", "atomic_linked_progress_failed",
				"error", linkedErr)
		}
		return linkedErr
	}
	tables, itemCatalog, err := s.currentQuestFinishResources(ctx)
	if err != nil {
		s.logGameEvent(session, "game-upper-finish-quest-blocked",
			"quest_id", request.QuestID,
			"reason", "progression_or_item_pvf_unavailable",
			"error", err)
		// 同上：`progression.Load`（每次完成都重做，无缓存）吃满 3 秒时也必须回包。
		if currentFinishQuestContextFailureReason(err) != "" && currentQuestFinishFailureReplyEnabled() {
			return s.replyCurrentFinishQuestContextFailure(session, request.QuestID, "progression_or_item_pvf_load", timing, err)
		}
		return nil
	}
	var professionResources []currentQuestProfessionResources
	if definition, ok := catalog.Find(int64(request.QuestID)); ok && currentQuestRewardIsProfession(definition.RewardType) {
		profiles, skillCatalog, err := s.currentProfessionResources(ctx)
		if err != nil {
			s.logGameEvent(session, "game-upper-finish-quest-blocked",
				"quest_id", request.QuestID,
				"reason", "profession_pvf_resources_unavailable",
				"error", err)
			// 同上：专家职业资源的加载失败（PVF 冷缓存）也不许静默。
			if currentFinishQuestContextFailureReason(err) != "" && currentQuestFinishFailureReplyEnabled() {
				return s.replyCurrentFinishQuestContextFailure(session, request.QuestID, "profession_pvf_load", timing, err)
			}
			return nil
		}
		professionResources = append(professionResources, currentQuestProfessionResources{Profiles: profiles, SkillCatalog: skillCatalog})
	}
	timing.markLoadDone()
	return s.handleCurrentFinishQuestTimed(
		session,
		request,
		repositories,
		catalog,
		tables,
		currentQuestFinishItemAllocator(itemCatalog),
		timing,
		professionResources...,
	)
}

type currentQuestProfessionResources struct {
	Profiles     *dnfprofession.Profiles
	SkillCatalog *dnfskill.Table
}

// handleCurrentFinishQuestWithResources 是 handleCurrentFinishQuestTimed 的无计时
// 入口：既有的仓库测试与其它调用点签名保持不变（timing == nil ⇒ 零行为差异）。
func (s *Service) handleCurrentFinishQuestWithResources(
	session *gameSession,
	request dnfquest.FinishQuestRequest,
	repositories dnfrepo.Group,
	catalog *dnfquest.Catalog,
	tables *progression.Tables,
	allocator dnfquest.FinishItemAllocator,
	professionResources ...currentQuestProfessionResources,
) error {
	return s.handleCurrentFinishQuestTimed(session, request, repositories, catalog, tables, allocator, nil, professionResources...)
}

// handleCurrentFinishQuestTimed 是完成任务结算的真正实现。timing 非 nil 时在
// 结算事务与 ACK 两个边界打点（load 段的起止由 handleCurrentFinishQuest 维护）；
// timing == nil（默认开关关闭，或旧调用点）时所有打点都是空操作，行为逐字节不变。
func (s *Service) handleCurrentFinishQuestTimed(
	session *gameSession,
	request dnfquest.FinishQuestRequest,
	repositories dnfrepo.Group,
	catalog *dnfquest.Catalog,
	tables *progression.Tables,
	allocator dnfquest.FinishItemAllocator,
	timing *currentQuestFinishTiming,
	professionResources ...currentQuestProfessionResources,
) error {
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	var professionProfiles *dnfprofession.Profiles
	var skillCatalog *dnfskill.Table
	if len(professionResources) > 0 {
		professionProfiles = professionResources[0].Profiles
		skillCatalog = professionResources[0].SkillCatalog
	}
	accountID := s.accountIDForSession(session)
	var experienceBonusPercent int64
	if growthEffect, active := s.currentGrowthContractEffect(ctx, accountID); active {
		experienceBonusPercent = growthEffect.BonusExperiencePercent
	}
	timing.markTxStart()
	// 与本次会话的玩法时钟一致：每日任务的完成日键按这个时间落库。
	result, err := owner.ApplyFinishSettlement(ctx, catalog, dnfquest.FinishCommitInput{
		AccountID:                     accountID,
		CharacterID:                   characterID,
		QuestID:                       int64(request.QuestID),
		RewardSelectIndex:             request.RewardSelectIndex,
		HasRewardSelect:               request.HasRewardSelect,
		Multiplier:                    request.Multiplier,
		MainQuestExperienceMultiplier: s.options.mainQuestExperienceMultiplier,
		CommittedAt:                   s.gameplayNow().UTC(),
		Progression:                   tables,
		ExperienceBonusPercent:        experienceBonusPercent,
		AllocateItem:                  allocator,
		ProfessionProfiles:            professionProfiles,
		SkillCatalog:                  skillCatalog,
	})
	timing.markTxDone()
	if err != nil {
		s.logGameEvent(session, "game-upper-finish-quest-blocked",
			"quest_id", request.QuestID,
			"reward_select", request.RewardSelectIndex,
			"has_reward_select", request.HasRewardSelect,
			"reason", "pvf_db_atomic_finish_rejected",
			"error", err)
		if currentFinishQuestBusinessFailure(err) {
			return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketFinishQuest), 22)
		}
		// A1：结算因为**上下文超时/取消**失败时也必须回包。旧行为是这里
		// `return nil`（一个字节都不回），2026-09-27 23:28 由此产生了 4 次
		// 「点了没反应、每次空等 ≥3 秒」（ctx 上界 = createWriteTimeout = 3s，
		// 错误链 = errors.Join(context deadline exceeded, sql.ErrTxDone)）。
		// 修复档复用的就是上面那条业务失败分支**已经在线上跑**的失败包形状
		// （msg 34 / class 1 / 体 `00 16`）；开关、回退与"哪种失败会回包"的
		// 判定见 quest_finish_failure_reply.go。加载阶段的三条兄弟路径
		// （任务目录 / progression+掉落 / 专家职业资源）用同一个开关与同一个
		// 回包助手，只是 stage 不同。
		//
		// 安全性：markCurrentFinishQuestAnswered 只在 ACK 写成功后调用
		// （见文件末尾），失败不置位 ⇒ 客户端重试仍能成功，也不会触发
		// 2026-07-25 那次「重复回执 NULL 解引用崩客户端」。
		if currentFinishQuestContextFailureReason(err) != "" && currentQuestFinishFailureReplyEnabled() {
			return s.replyCurrentFinishQuestContextFailure(session, request.QuestID, "settlement", timing, err)
		}
		return nil
	}
	timing.markAckStart()
	payload, err := buildCurrentFinishQuestSuccessBody(result)
	if err != nil {
		s.logGameEvent(session, "game-upper-finish-quest-blocked",
			"quest_id", request.QuestID,
			"completion_key", result.CompletionKey,
			"reason", "committed_result_ack_shape_invalid",
			"error", err)
		return nil
	}
	replayKey := newCurrentFinishQuestReplayKey(session.selectedCharacterID, request)
	if result.Replayed && session.currentFinishQuestAnswered(replayKey) {
		// Same-session duplicate of an already-answered finish: the first
		// receipt completed the client's quest UI flow; resending it NULL-derefs
		// the client handler. Reconnect recovery is unaffected because a fresh
		// session has an empty answered set and still receives the replay.
		s.logGameEvent(session, "game-upper-finish-quest-replay-suppressed",
			"quest_id", request.QuestID,
			"completion_key", result.CompletionKey,
			"reason", "same_session_duplicate_receipt_would_crash_client_handler")
		return nil
	}
	s.logGameEvent(session, "game-upper-finish-quest-success-send",
		"quest_id", result.QuestID,
		"completion_key", result.CompletionKey,
		"reward_source", result.Source,
		"base_experience_delta", result.BaseExperienceDelta,
		"experience_bonus", result.ExperienceBonusDelta,
		"experience_delta", result.ExperienceDelta,
		"level_before", result.PreviousLevel,
		"level_after", result.NewLevel,
		"sp_delta", result.SPDelta,
		"item_count", len(result.Items),
		"consumed_count", len(result.ConsumedItems),
		"currency_count", len(result.Currency),
		"has_profession", result.HasProfession,
		"profession_chain_type", result.Profession.ChainType,
		"profession_grow_number", result.Profession.GrowNumber,
		"profession_grow_before", result.Profession.PreviousGrowType,
		"profession_grow_after", result.Profession.NewGrowType,
		"profession_grant_count", len(result.ProfessionGrants),
		"has_expert_job", result.HasExpertJob,
		"expert_job_type", result.ExpertJobType,
		"has_hell_challenge", result.HasHellChallenge,
		"hell_challenge_value", result.HellChallengeValue,
		"hell_challenge_asset", "none_no_proved_storage",
		"replayed", result.Replayed,
		"msg_id", uint16(dnfenum.CmdPacketFinishQuest),
		"classification", dnfproto.DefaultChannelClassification,
		"plain_body_len", len(payload)+1,
		"body_source", "current_exe_sub_1D32120_and_atomic_pvf_finish_receipt")
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketFinishQuest), payload); err != nil {
		return err
	}
	timing.markAckDone()
	// The client can safely receive no more than one terminal receipt per
	// selected character/session, including the first durable replay after a
	// reconnect. Mark only after the ACK write succeeds so a failed transport
	// remains retryable.
	session.markCurrentFinishQuestAnswered(replayKey)
	if result.HasExpertJob {
		// Current NoPack sub_1D32120 consumes chain type 20 as part of the
		// finish ACK, but the auxiliary-profession domain state has its own
		// class-0/op205 reader (sub_1D89C20). Publish that typed state
		// immediately after the ACK and before the generic character/quest
		// refreshes. 86JP's legacy op2 subtype0 and current op754 are not valid
		// local-actor carriers for this EXE; its compatibility unit mirrors the
		// validated op205 type into the existing current actor without a panel.
		if err := s.sendCurrentExpertJobInfoForCharacter(session, result.PostCommitCharacter, true); err != nil {
			return err
		}
	}
	if result.HasProfession {
		return s.sendCurrentProfessionFinishPostCommitSnapshots(session, result)
	}
	return s.sendCurrentFinishQuestPostCommitSnapshots(session, result)
}

func currentQuestRewardIsProfession(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value == "grow type" || value == "awakening type"
}

func validateCurrentProfessionFinishResult(session *gameSession, result dnfquest.FinishCommitResult) error {
	if session == nil {
		return errCurrentFinishQuestAckShape
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	if !result.AtomicCommitted || !result.HasProfession || result.CharacterID != characterID ||
		result.PostCommitCharacter.CharacterID != characterID || result.PostCommitSkill.CharacterID != characterID ||
		result.PostCommitCharacter.Stats["grow_type"] != int64(result.Profession.NewGrowType) {
		return errCurrentFinishQuestAckShape
	}
	return nil
}

// sendCurrentProfessionFinishPostCommitSnapshots applies only repository/PVF
// state returned by the committed quest owner. 当前 v97 的完成 ACK 将 GrowNumber
// 用于选择职业精通和授予技能，没有把它写入场景角色的职业身份；op37/op19 也不携带
// 该身份。因此先用既有严格 typed mode0 投影已提交职业，再刷新数值和技能。
// 不发送会打开个人面板的 mode1/mode3，不重做转职事务。
//
// 2026-09-26 — 转职/觉醒还必须下发一次 op19（SKILLINFO）：`profession.ApplySkillTransition`
// 在 KindClassChange 下把技能行整体换成新方向的免费授予、`Cooldowns` 清空、SP/TP 全额退回
// （profile.go:195-205，由 finish_owner.go:413 在同一个事务里提交），但当前连接里的客户端
// 仍持有转职前的技能表与 SP。「转职后技能不会被重置」就是少了这一帧。参考实现在同一处对
// chainType 1/2 发 0x0013：`QuestNotificationProjector.SendPreFinishAckNotificationsAsync`
// → `SendSkillInfoRefreshAsync`，注释写明「客户端在该通知上整体替换技能表」。普通结算路径
// （下方 sendCurrentFinishQuestPostCommitSnapshots）在等级/SP 变化时发的是同一份
// buildCurrentSceneSkillInfoBody，所以这里只是补上同形状的一帧，位置也保持
// 角色状态 → 技能表 → 任务列表 的既有顺序。
func (s *Service) sendCurrentProfessionFinishPostCommitSnapshots(session *gameSession, result dnfquest.FinishCommitResult) error {
	if err := validateCurrentProfessionFinishResult(session, result); err != nil {
		return err
	}
	if err := s.sendCurrentProfessionFinishActorIdentity(session, result); err != nil {
		return err
	}
	characterBody := buildCurrentFinishLoadingCharacterStateBody(result.PostCommitCharacter, result.PostCommitSkill.Points)
	if len(characterBody) != currentFinishLoadingCharacterStateBodySize {
		return errCurrentFinishQuestAckShape
	}
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, characterBody, 0); err != nil {
		return err
	}
	var skillCommands map[uint16][]byte
	if repositories, ok := s.repositoryGroup(); ok {
		skillCommands = s.loadSkillCommandProjection(context.Background(), repositories, result.PostCommitSkill.CharacterID)
	}
	skillBody, _, err := buildCurrentSceneSkillInfoBody(
		result.PostCommitSkill,
		result.PostCommitSkill.Layouts[dnfrepo.ActiveSkillTree(result.PostCommitSkill)],
		skillCommands,
	)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, currentSkillInfoMsgID, skillBody, 0); err != nil {
		return err
	}
	if err := s.sendCurrentActiveQuestSnapshotFromCommittedQuest(
		session,
		result.PostCommitQuest,
		"current_exe_op34_after_profession_actor_refresh",
	); err != nil {
		return err
	}
	if err := s.refreshCurrentTownQuestGateScene(session, result); err != nil {
		return err
	}
	// An empty op574 clears the client's completed-quest lookup too. Restore
	// that lookup with op356 before publishing newly acceptable definitions.
	if err := s.sendCurrentClearQuestListFromCommittedQuest(session, result.PostCommitQuest,
		"current_exe_op34_after_profession_active_snapshot_before_acceptable"); err != nil {
		return err
	}
	return s.sendCurrentAcceptableQuestListOnlyForSession(session, "current_exe_op34_after_profession_completed_snapshot")
}

// sendCurrentFinishQuestPostCommitSnapshots publishes only the immutable state
// returned by the committed settlement owner. Ordinary finishes must not push
// mode1/mode3 personal-information snapshots: current mode3 opens the personal
// information panel, and both bodies rebuild the entire actor/equipment view.
// The finish ACK owns ordinary EXP/item changes. A rare slot-expansion reward
// still needs one committed mode1 projection because that is the proved online
// carrier for ex_equip_slot_stat; mode3 remains suppressed.
//
// The order is intentional: changed inventory rows first, then only genuinely
// changed character/skill state, then active, completed and acceptable quest
// views. Empty active views reset the client's completed lookup as well.
func (s *Service) sendCurrentFinishQuestPostCommitSnapshots(session *gameSession, result dnfquest.FinishCommitResult) error {
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	if !result.AtomicCommitted || result.CharacterID != characterID ||
		result.PostCommitCharacter.CharacterID != characterID ||
		result.PostCommitSkill.CharacterID != characterID {
		return errCurrentFinishQuestAckShape
	}
	if err := s.sendCurrentFinishQuestRewardItemUpdate(session, result); err != nil {
		return err
	}
	if result.HasSlotExpansion {
		if err := s.sendCurrentFinishQuestSlotExpansionMode1Snapshot(session, result); err != nil {
			return err
		}
		s.logGameEvent(session, "game-upper-finish-quest-slot-expansion",
			"quest_id", result.QuestID,
			"slot_expansion_index", result.SlotExpansionIndex,
			"slot_expansion_bit", result.SlotExpansionBit,
			"ex_equip_slot_stat", result.PostCommitCharacter.Stats["ex_equip_slot_stat"])
	}
	if result.NewLevel != result.PreviousLevel || result.SPDelta != 0 || result.HasExpertJob {
		characterBody := buildCurrentFinishLoadingCharacterStateBody(
			result.PostCommitCharacter,
			result.PostCommitSkill.Points,
		)
		if len(characterBody) != currentFinishLoadingCharacterStateBodySize {
			return errCurrentFinishQuestAckShape
		}
		if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, characterBody, 0); err != nil {
			return err
		}
		var skillCommands map[uint16][]byte
		if repositories, ok := s.repositoryGroup(); ok {
			skillCommands = s.loadSkillCommandProjection(context.Background(), repositories, result.PostCommitSkill.CharacterID)
		}
		skillBody, _, err := buildCurrentSceneSkillInfoBody(
			result.PostCommitSkill,
			result.PostCommitSkill.Layouts[dnfrepo.ActiveSkillTree(result.PostCommitSkill)],
			skillCommands,
		)
		if err != nil {
			return err
		}
		if err := s.sendGameUpperRawClass(session, currentSkillInfoMsgID, skillBody, 0); err != nil {
			return err
		}
	} else {
		s.logGameEvent(session, "game-upper-finish-quest-character-skill-refresh-skipped",
			"quest_id", result.QuestID,
			"completion_key", result.CompletionKey,
			"reason", "finish_ack_owns_exp_and_no_level_sp_or_expert_job_delta")
	}
	// Current YKCN90 EXE op574 (0x2014370), when count is zero, calls
	// 0x1cb7e40 -> 0x1d506c0 to clear QuestManager+0xc4, the completed lookup.
	// op356 (0x2009690 -> 0x1cc07f0) must rebuild it after that reset. A
	// completed table sent first is lost just when the last active quest ends.
	if err := s.sendCurrentActiveQuestSnapshotFromCommittedQuest(
		session,
		result.PostCommitQuest,
		"current_exe_op34_after_atomic_finish_state_refresh",
	); err != nil {
		return err
	}
	if err := s.refreshCurrentTownQuestGateScene(session, result); err != nil {
		return err
	}
	if err := s.sendCurrentClearQuestListFromCommittedQuest(session, result.PostCommitQuest,
		"current_exe_op34_after_active_snapshot_completed_quest_scene_refresh"); err != nil {
		return err
	}
	// Publish acceptable definitions last, with their prerequisites present.
	// Acceptance remains owned by the player's native op31 request.
	return s.sendCurrentAcceptableQuestListOnlyForSession(session, "current_exe_op34_after_completed_snapshot_final")
}

func (s *Service) sendCurrentFinishQuestSlotExpansionMode1Snapshot(
	session *gameSession,
	result dnfquest.FinishCommitResult,
) error {
	if session == nil || session.selectedCharacterID == 0 {
		return errCurrentFinishQuestAckShape
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	if !result.AtomicCommitted || !result.HasSlotExpansion || result.CharacterID != characterID ||
		result.PostCommitCharacter.CharacterID != characterID {
		return errCurrentFinishQuestAckShape
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	var legacyRepo dnfrepo.LegacyUserInfoRepository
	if repositories, ok := s.repositoryGroup(); ok {
		legacyRepo = repositories.LegacyUserInfo
	}
	charID := session.selectedCharacterID
	summary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, result.PostCommitCharacter, true)
	adventureLevel := uint32(summary.ManageLevel)
	mode1Body := s.buildCurrentSelectedUserInfoMode1BodyWithAdventureLevelInContext(
		ctx,
		session,
		legacyRepo,
		result.PostCommitCharacter,
		true,
		charID,
		adventureLevel,
		currentTownActorOwnerContext(session),
	)
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), mode1Body, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-upper-finish-quest-slot-expansion-mode1-send",
		"quest_id", result.QuestID,
		"mode1_body_len", len(mode1Body),
		"owner_channel", currentTownActorOwnerContext(session),
		"mode3_suppressed", true,
		"ex_equip_slot_stat", result.PostCommitCharacter.Stats["ex_equip_slot_stat"],
		"body_source", "atomic_post_commit_mode1_only_for_proved_slot_expansion_carrier")
	return nil
}

func (s *Service) sendCurrentFinishQuestRewardItemUpdate(session *gameSession, result dnfquest.FinishCommitResult) error {
	if len(result.Items) == 0 && len(result.ConsumedItems) == 0 && len(result.Currency) == 0 {
		s.logGameEvent(session, "game-upper-finish-quest-reward-item-update-skipped",
			"quest_id", result.QuestID,
			"completion_key", result.CompletionKey,
			"reason", "no_item_currency_or_consumed_material_changes")
		return nil
	}
	entries := make([]currentItemListEntry, 0, len(result.Items)+len(result.ConsumedItems)+len(result.Currency))
	seenGold := false
	for _, currency := range result.Currency {
		if currency.Delta < 0 || currency.PostValue < 0 {
			return errCurrentFinishQuestAckShape
		}
		switch strings.ToLower(strings.TrimSpace(currency.Name)) {
		case "gold":
			if seenGold {
				return errCurrentFinishQuestAckShape
			}
			seenGold = true
			gold := currency.PostValue
			if gold > math.MaxInt32 {
				gold = math.MaxInt32
			}
			var entry currentItemListEntry
			entry.patchCore(0, 0, uint32(gold))
			entries = append(entries, entry)
		default:
			return errCurrentFinishQuestAckShape
		}
	}
	rewardSlots := make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		if item.SlotKey != "" {
			rewardSlots[item.SlotKey] = struct{}{}
		}
	}
	for _, consumed := range result.ConsumedItems {
		if consumed.SlotIndex > math.MaxInt16 || consumed.ItemID <= 0 || consumed.ItemID > math.MaxUint32 || consumed.Delta <= 0 || consumed.Delta > math.MaxUint32 || consumed.PostCount < 0 {
			return errCurrentFinishQuestAckShape
		}
		if _, replacedByReward := rewardSlots[consumed.SlotKey]; replacedByReward {
			continue
		}
		var entry currentItemListEntry
		if consumed.PostCount <= 0 {
			entry.patchCore(int16(consumed.SlotIndex), math.MaxUint32, 0)
			entries = append(entries, entry)
			continue
		}
		if len(consumed.RawEntry) == currentItemListEntryWireSize &&
			binary.LittleEndian.Uint16(consumed.RawEntry[0:2]) == consumed.SlotIndex &&
			binary.LittleEndian.Uint32(consumed.RawEntry[2:6]) == uint32(consumed.ItemID) {
			copy(entry.data[:], consumed.RawEntry)
			entries = append(entries, entry)
			continue
		}
		slots := result.PostCommitInventory.Slots
		if dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, int16(consumed.SlotIndex)) {
			slots = result.PostCommitAccountInventory.Slots
		}
		stack, found := slots[consumed.SlotKey]
		if !found || stack.ItemID != consumed.ItemID || stack.Count != consumed.PostCount {
			return errCurrentFinishQuestAckShape
		}
		entry = currentItemListEntryFromStack(dnfrepo.MainInventoryListType, int16(consumed.SlotIndex), stack)
		entries = append(entries, entry)
	}
	for _, item := range result.Items {
		if len(item.RawEntry) != currentItemListEntryWireSize ||
			binary.LittleEndian.Uint16(item.RawEntry[0:2]) != item.SlotIndex ||
			binary.LittleEndian.Uint32(item.RawEntry[2:6]) != uint32(item.ItemID) {
			return errCurrentFinishQuestAckShape
		}
		var entry currentItemListEntry
		copy(entry.data[:], item.RawEntry)
		entries = append(entries, entry)
	}
	body := buildCurrentItemUpdateBody(0, entries)
	s.logGameEvent(session, "game-upper-finish-quest-reward-item-update-send",
		"quest_id", result.QuestID,
		"completion_key", result.CompletionKey,
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"classification", 0,
		"list_type", 0,
		"entry_count", len(entries),
		"item_count", len(result.Items),
		"consumed_count", len(result.ConsumedItems),
		"currency_count", len(result.Currency),
		"entry_size", currentItemListEntryWireSize,
		"body_len", len(body),
		"body_source", "committed_pvf_finish_reward_consumed_material_inventory_and_gold_op14_raw77")
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0)
}

func (s *Service) currentQuestFinishResources(ctx context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
	if s == nil {
		return nil, nil, dnfquest.ErrFinishSettlementUnavailable
	}
	// 仅单测的注入点（生产恒为 nil）；见 Service.questFinishResourceLoader。
	if s.questFinishResourceLoader != nil {
		return s.questFinishResourceLoader(ctx)
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		return nil, nil, err
	}
	var items *pvfDungeonDropCatalog
	if s.dungeonMonsterTable != nil {
		items, err = s.dungeonMonsterTable.DropCatalog()
	} else {
		items, err = newPVFDungeonDropCatalog(archive)
	}
	if err != nil {
		return nil, nil, err
	}
	return tables, items, nil
}

func currentQuestFinishItemAllocator(catalog *pvfDungeonDropCatalog) dnfquest.FinishItemAllocator {
	return func(record *dnfrepo.InventoryRecord, request dnfquest.FinishItemGrantRequest) ([]dnfquest.FinishCommittedItem, error) {
		if catalog == nil || record == nil || request.ItemID <= 0 || request.ItemID > math.MaxUint32 || request.Count <= 0 || request.Count > math.MaxUint32 {
			return nil, errDungeonPickupItemInvalid
		}
		definition, err := catalog.ResolveItem(uint32(request.ItemID))
		if err != nil {
			return nil, err
		}
		definition, err = currentPVFItemDefinitionForGrantAt(definition, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		instances, quantity := int64(1), request.Count
		if definition.Kind == dungeonDropItemEquipment {
			// PVF同ID的多件装备不能形成count>1的堆栈，每件必须独立入格并回执。
			// 先检查原生ACK行数上限，避免巨大奖励循环；领域再核对整笔总行数。
			if request.Count > math.MaxUint8 {
				return nil, dnfquest.ErrQuestRewardMalformed
			}
			instances, quantity = request.Count, 1
		}
		items := make([]dnfquest.FinishCommittedItem, 0, int(instances))
		for instance := int64(0); instance < instances; instance++ {
			occupied := make(map[string]struct{}, len(record.Slots))
			for key := range record.Slots {
				occupied[key] = struct{}{}
			}
			slot, err := addCurrentDungeonPickupToInventory(record, definition, uint32(quantity))
			if err != nil {
				return nil, err
			}
			key := currentDungeonPickupMainSlotKey(int16(slot))
			stack, found := record.Slots[key]
			if !found || stack.ItemID != request.ItemID || stack.Count < quantity {
				return nil, errDungeonPickupItemInvalid
			}
			entry := currentItemListEntryFromStack(0, int16(slot), stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			if stack.Extra == nil {
				stack.Extra = make(map[string]string, 6)
			}
			if _, existed := occupied[key]; !existed {
				stack.Extra["source"] = "quest_pvf_finish_reward"
			}
			stack.Extra["last_grant_source"] = "quest_pvf_finish_reward"
			stack.Extra["last_grant_quest_id"] = strconv.FormatInt(request.QuestID, 10)
			stack.Extra["last_grant_completion_key"] = request.CompletionKey
			stack.Extra["last_grant_pvf_source"] = request.Source
			record.Slots[key] = stack
			countOrSeed := uint32(quantity)
			if definition.Kind == dungeonDropItemEquipment {
				countOrSeed = binary.LittleEndian.Uint32(entry.data[0x06:0x0a])
			}
			items = append(items, dnfquest.FinishCommittedItem{
				SlotKey: key, SlotIndex: slot, ItemID: request.ItemID,
				Delta: quantity, PostCount: stack.Count, CountOrSeed: countOrSeed,
				RawEntry: append([]byte(nil), entry.data[:]...),
			})
		}
		return items, nil
	}
}

func buildCurrentFinishQuestSuccessBody(result dnfquest.FinishCommitResult) ([]byte, error) {
	if !result.AtomicCommitted || result.QuestID <= 0 || result.QuestID > math.MaxUint16 ||
		result.CompletionKey == "" || result.Source == "" || len(result.Items) > math.MaxUint8 || len(result.ConsumedItems) > math.MaxUint8 {
		return nil, errCurrentFinishQuestAckShape
	}
	var writer packetWriter
	writer.writeUint16(uint16(result.QuestID))
	writer.writeByte(0) // completionType=0: current reader consumes the normal item sections.
	writer.writeUint32(result.ExperienceDelta)
	writer.writeByte(byte(len(result.ConsumedItems)))
	for _, consumed := range result.ConsumedItems {
		if consumed.SlotIndex > math.MaxInt16 || consumed.ItemID <= 0 || consumed.ItemID > math.MaxUint32 ||
			consumed.Delta <= 0 || consumed.Delta > math.MaxUint32 || consumed.PostCount < 0 {
			return nil, errCurrentFinishQuestAckShape
		}
		writer.writeByte(0) // updateType=0: current EXE subtracts consumed count from the slot.
		writer.writeUint16(consumed.SlotIndex)
		writer.writeUint32(uint32(consumed.Delta))
	}
	if result.HasProfession {
		if len(result.Items) != 0 || len(result.Currency) != 0 ||
			(result.Profession.ChainType != 1 && result.Profession.ChainType != 2) ||
			result.Profession.GrowNumber == 0 || result.Profession.NewGrowType == result.Profession.PreviousGrowType ||
			len(result.ProfessionGrants) > math.MaxUint8 {
			return nil, errCurrentFinishQuestAckShape
		}
		writer.writeByte(result.Profession.ChainType)
		writer.writeByte(result.Profession.GrowNumber)
		for tree := 0; tree < currentSkillInfoTreeCount; tree++ {
			writer.writeByte(byte(len(result.ProfessionGrants)))
			for _, grant := range result.ProfessionGrants {
				if grant.SkillID == 0 || grant.Level <= 0 || grant.Level > math.MaxUint8 {
					return nil, errCurrentFinishQuestAckShape
				}
				// Current NoPack sub_1D32120 consumes absolute level, skill ID,
				// then learned/enabled=1 for each of the two skill trees.
				writer.writeByte(byte(grant.Level))
				writer.writeUint16(grant.SkillID)
				writer.writeByte(1)
			}
		}
		return writer.bytes(), nil
	}
	if result.HasExpertJob {
		if len(result.Items) != 0 || len(result.Currency) != 0 || result.ExpertJobType == 0 {
			return nil, errCurrentFinishQuestAckShape
		}
		// Current NoPack sub_1D32120 handles chain 20 in the same branch as
		// class change/awakening: u8 type, then two inline skill-tree counts.
		// Expert-job completion has no inline skill grants; the server persists
		// expert_job_type atomically before this ACK is emitted.
		writer.writeByte(20)
		writer.writeByte(result.ExpertJobType)
		writer.writeByte(0)
		writer.writeByte(0)
		return writer.bytes(), nil
	}
	if result.HasSlotExpansion {
		expectedBit, validIndex := dnfquest.ExEquipSlotBitForPVFIndex(result.SlotExpansionIndex)
		if len(result.Items) != 0 || len(result.Currency) != 0 ||
			!validIndex ||
			result.SlotExpansionBit != expectedBit {
			return nil, errCurrentFinishQuestAckShape
		}
		// Current NoPack sub_1D32120 chain type 21 does read a u32, but the
		// receiving model is unrelated to the support/magic-stone/earring
		// bitset. The committed bit is projected by the slot-expansion-only
		// mode1 actor snapshot, so finish uses the ordinary empty chain.
	}
	writer.writeByte(0) // chainType=0: ordinary PVF item reward.
	writer.writeByte(byte(len(result.Items)))
	for _, item := range result.Items {
		if item.ItemID <= 0 || item.ItemID > math.MaxUint32 || item.CountOrSeed == 0 || len(item.RawEntry) != currentItemListEntryWireSize ||
			binary.LittleEndian.Uint16(item.RawEntry[0:2]) != item.SlotIndex ||
			binary.LittleEndian.Uint32(item.RawEntry[2:6]) != uint32(item.ItemID) {
			return nil, errCurrentFinishQuestAckShape
		}
		writer.writeUint16(item.SlotIndex)
		writer.writeUint32(uint32(item.ItemID))
		writer.writeUint32(item.CountOrSeed)
		writer.writeByte(item.RawEntry[0x0a])
		writer.writeUint16(binary.LittleEndian.Uint16(item.RawEntry[0x0b:0x0d]))
		writer.writeUint32(binary.LittleEndian.Uint32(item.RawEntry[0x0e:0x12]))
		writer.writeByte(item.RawEntry[0x12])
		writer.writeByte(item.RawEntry[0x13])
	}
	return writer.bytes(), nil
}

func currentFinishQuestBusinessFailure(err error) bool {
	return errors.Is(err, dnfquest.ErrFinishQuestNotPending) ||
		errors.Is(err, dnfquest.ErrFinishQuestCompletionConflict) ||
		errors.Is(err, dnfquest.ErrFinishQuestSettlementStale) ||
		errors.Is(err, dnfquest.ErrFinishQuestSettlementCorrupt) ||
		errors.Is(err, dnfquest.ErrFinishQuestMultiplierUnsupported) ||
		errors.Is(err, dnfquest.ErrFinishMainQuestExperienceMultiplierInvalid) ||
		errors.Is(err, dnfquest.ErrFinishMainQuestExperienceOverflow) ||
		errors.Is(err, dnfquest.ErrFinishProgressionUnproven) ||
		errors.Is(err, dnfquest.ErrFinishRequiredItemsMissing) ||
		errors.Is(err, dnfquest.ErrQuestDefinitionMissing) ||
		errors.Is(err, dnfquest.ErrQuestNotAcceptable) ||
		errors.Is(err, dnfquest.ErrQuestRewardUnsupported) ||
		errors.Is(err, dnfquest.ErrQuestRewardMalformed) ||
		errors.Is(err, dnfquest.ErrQuestRewardSelectionInvalid)
}
