package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

const (
	currentAutoCompleteMainQuestRequestSize  = 4
	currentAutoCompleteMainQuestResponseSize = 13
	currentAutoCompleteMainQuestMaxLevel     = 89
)

// questClearTicketScope 报告任务手册"任务完成"(op1429, ENUM_CMDPACKET_CLEAR_QUEST_TICKET)
// 批量动作用的清任务口径，来源是启动环境变量 DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP
// （默认开启，见 options.questClearTicketActiveOnly）。
//
//   - 未设置 / 真（默认）= CouponClearScopeMainStorySweep：把运行时 PVF 里
//     `[level] <= min(角色等级-1, 89)` 的主线任务全部标记完成，角色没接过的也按需
//     创建 completed 行。这与客户端"任务完成"确认框的文案「自动完成 Lv89 为止的
//     主线任务，且无任务奖励」一致，也与完成券（op515 action 167）共用同一套规划器。
//   - 置 0/false/off/no = CouponClearScopeActiveOnly：只完成当前处于激活态的主线
//     任务，即 2026-09-25 现场那次"只完成了当前这一条"的旧口径；用于线上 A/B 或
//     临时回退，不需要重新编译。
//
// 选项按反向存放，因此零值 options（测试与探针里的 Service 字面量）走默认的 sweep
// 口径，与线上默认一致。启动日志 `dnfbridge started` 的 quest_clear_ticket_scope
// 字段可直接确认生效值。
func (s *Service) questClearTicketScope() dnfquest.CouponClearScope {
	if s != nil && s.options.questClearTicketActiveOnly {
		return dnfquest.CouponClearScopeActiveOnly
	}
	return dnfquest.CouponClearScopeMainStorySweep
}

// The current EXE's sub_79EB70 consumes one packed 13-byte result directly:
// request selector, highest completed quest id, cutoff level, committed flag.
// This is not the common one-byte success ACK used by op31/op34.
func buildCurrentAutoCompleteMainQuestResponse(
	requestSelector uint32,
	highestCompletedQuest uint32,
	cutoffLevel uint32,
	committed bool,
) []byte {
	body := make([]byte, currentAutoCompleteMainQuestResponseSize)
	binary.LittleEndian.PutUint32(body[0:4], requestSelector)
	binary.LittleEndian.PutUint32(body[4:8], highestCompletedQuest)
	binary.LittleEndian.PutUint32(body[8:12], cutoffLevel)
	if committed {
		body[12] = 1
	}
	return body
}

func (s *Service) handleCurrentAutoCompleteMainQuest(session *gameSession, body []byte) error {
	if session == nil || !session.loginPacketSeen || strings.TrimSpace(session.accountID) == "" {
		return nil
	}
	if len(body) != currentAutoCompleteMainQuestRequestSize {
		s.logGameEvent(session, "game-auto-complete-main-quest-blocked",
			"body_len", len(body),
			"reason", "current_exe_op1429_requires_exact_u32_request")
		return nil
	}
	requestSelector := binary.LittleEndian.Uint32(body)

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return nil
	}
	accountID := strings.TrimSpace(session.accountID)
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, accountID)
	if err != nil {
		return nil
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil {
		s.logGameEvent(session, "game-auto-complete-main-quest-blocked",
			"request_selector", requestSelector,
			"reason", "character_settlement_transaction_unavailable")
		return nil
	}
	characterID := strconv.Itoa(int(identity.character))
	_, _, character, found := s.characterForEnter(ctx, session, identity.character)
	if !found || character.CharacterID != characterID || strings.TrimSpace(character.AccountID) != accountID || character.Level <= 1 {
		s.logGameEvent(session, "game-auto-complete-main-quest-blocked",
			"request_selector", requestSelector,
			"character_found", found,
			"level", character.Level,
			"reason", "selected_character_or_previous_level_unavailable")
		return nil
	}
	job, validJob := characterJobByte(character)
	if !validJob {
		s.logGameEvent(session, "game-auto-complete-main-quest-blocked",
			"request_selector", requestSelector,
			"job", character.Job,
			"reason", "selected_character_job_invalid")
		return nil
	}
	cutoffLevel := character.Level - 1
	if cutoffLevel > currentAutoCompleteMainQuestMaxLevel {
		cutoffLevel = currentAutoCompleteMainQuestMaxLevel
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		s.logGameEvent(session, "game-auto-complete-main-quest-blocked",
			"request_selector", requestSelector,
			"reason", "quest_catalog_unavailable",
			"error", err)
		return nil
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return err
	}
	targetQuestID := int64(requestSelector)
	targetSource := "current_exe_nonzero_per_entry_selector"
	if requestSelector == 0 {
		targetSource = "zero_selector_requires_exactly_one_eligible_active_epic"
	}
	result, err := owner.ApplyAutoCompleteMain(ctx, catalog, dnfquest.AutoCompleteMainInput{
		AccountID:   accountID,
		CharacterID: characterID,
		Eligibility: dnfquest.CharacterEligibility{
			Level:    character.Level,
			Job:      int(job),
			GrowType: characterGrowTypeInt(character),
		},
		CutoffLevel:   cutoffLevel,
		TargetQuestID: targetQuestID,
		Scope:         s.questClearTicketScope(),
		CompletedAt:   time.Now().UTC(),
		Revalidate:    func() bool { return s.boundGameSessionCharacterCurrent(identity) },
	})
	// Binding writers may be waiting while holding scene locks. Finish the
	// committed mutation before projection takes those locks or performs I/O.
	s.releaseCurrentCharacterAssets(reservation)
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	if err != nil {
		if errors.Is(err, dnfquest.ErrAutoCompleteTargetAmbiguous) ||
			errors.Is(err, dnfquest.ErrAutoCompleteTargetInvalid) {
			response := buildCurrentAutoCompleteMainQuestResponse(
				requestSelector,
				0,
				uint32(cutoffLevel),
				false,
			)
			s.logGameEvent(session, "game-auto-complete-main-quest-single-target-required",
				"request_selector", requestSelector,
				"target_quest_id", targetQuestID,
				"target_source", targetSource,
				"cutoff_level", cutoffLevel,
				"reason", err,
				"quest_mutation", "none",
				"msg_id", uint16(dnfenum.CmdPacketClearQuestTicket),
				"classification", dnfproto.DefaultChannelClassification,
				"body_len", len(response))
			return s.sendGameUpperRawClass(
				session,
				uint16(dnfenum.CmdPacketClearQuestTicket),
				response,
				dnfproto.DefaultChannelClassification,
			)
		}
		s.logGameEvent(session, "game-auto-complete-main-quest-blocked",
			"request_selector", requestSelector,
			"target_quest_id", targetQuestID,
			"target_source", targetSource,
			"cutoff_level", cutoffLevel,
			"reason", "pvf_db_atomic_selected_main_quest_completion_rejected",
			"error", err)
		return nil
	}
	if targetQuestID == 0 && result.Idempotent {
		// 批量动作已经没有可完成的主线任务（整段主线都已完成，或开关回退到
		// ActiveOnly 且当前没有激活主线）。保持 2026-09-25 现场第二次点击的
		// 回包形状：selector 原样回填、highest=0、committed=0，且不再补发任务
		// 快照——库里的行一个字节都没动。
		response := buildCurrentAutoCompleteMainQuestResponse(
			requestSelector,
			0,
			uint32(cutoffLevel),
			false,
		)
		s.logGameEvent(session, "game-auto-complete-main-quest-nothing-to-complete",
			"request_selector", requestSelector,
			"target_source", targetSource,
			"cutoff_level", cutoffLevel,
			"clear_scope", string(s.questClearTicketScope()),
			"reason", "no_safely_completable_main_story_quest",
			"blocked_question_ids", int64ListText(result.BlockedQuestionIDs),
			"damaged_question_ids", int64ListText(result.DamagedQuestionIDs),
			"quest_mutation", "none",
			"msg_id", uint16(dnfenum.CmdPacketClearQuestTicket),
			"classification", dnfproto.DefaultChannelClassification,
			"body_len", len(response))
		return s.sendGameUpperRawClass(
			session,
			uint16(dnfenum.CmdPacketClearQuestTicket),
			response,
			dnfproto.DefaultChannelClassification,
		)
	}
	if result.HighestCompletedQuest < 0 || result.HighestCompletedQuest > int64(^uint32(0)) {
		return fmt.Errorf("auto-complete highest quest id out of wire range: %d", result.HighestCompletedQuest)
	}
	response := buildCurrentAutoCompleteMainQuestResponse(
		requestSelector,
		uint32(result.HighestCompletedQuest),
		uint32(cutoffLevel),
		true,
	)
	s.logGameEvent(session, "game-auto-complete-main-quest-success-send",
		"request_selector", requestSelector,
		"target_quest_id", result.HighestCompletedQuest,
		"target_source", targetSource,
		"cutoff_level", cutoffLevel,
		"clear_scope", string(s.questClearTicketScope()),
		"include_sub_quests", true,
		"targeted_active_epic_count", len(result.EligibleQuestIDs),
		"changed_epic_count", len(result.ChangedQuestIDs),
		"changed_linked_sub_count", len(result.ChangedLinkedSubIDs),
		"changed_linked_sub_ids", int64ListText(result.ChangedLinkedSubIDs),
		"active_completed_count", result.ActiveCompletedCount,
		"blocked_question_ids", int64ListText(result.BlockedQuestionIDs),
		"damaged_question_ids", int64ListText(result.DamagedQuestionIDs),
		"highest_completed_quest", result.HighestCompletedQuest,
		"idempotent", result.Idempotent,
		"reward_granted", false,
		"item_consumed", false,
		"msg_id", uint16(dnfenum.CmdPacketClearQuestTicket),
		"classification", dnfproto.DefaultChannelClassification,
		"body_len", len(response),
		"body_source", "current_exe_sub_79EB70_exact_13_byte_reader_and_atomic_no_reward_main_story_completion")
	if err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketClearQuestTicket),
		response,
		dnfproto.DefaultChannelClassification,
	); err != nil {
		return err
	}
	return s.sendCommittedStorySweepSnapshots(session, result.PostCommitCharacter, result.PostCommitQuest,
		append(append([]int64(nil), result.ChangedQuestIDs...), result.ChangedLinkedSubIDs...), "quest_manual")
}
