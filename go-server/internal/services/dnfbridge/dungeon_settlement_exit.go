package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentDungeonCharacterStatisticBodySize = 24

// handleDungeonCharacterStatistic records the six-u32 telemetry packet emitted
// by the current EXE's completed tutorial/settlement manager. It is a barrier
// observation only: the values never become authoritative rewards or assets,
// and this request has no proven server response.
func (s *Service) handleDungeonCharacterStatistic(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	if len(body) != currentDungeonCharacterStatisticBodySize {
		s.logGameEvent(session, "game-dungeon-character-statistic-blocked",
			"body_len", len(body),
			"expected_body_len", currentDungeonCharacterStatisticBodySize,
			"reason", "current_exe_op123_six_u32_boundary_mismatch")
		return nil
	}

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if !completedDungeonSettlementEntryReady(runtime, session.selectedCharacterID) {
		s.logGameEvent(session, "game-dungeon-character-statistic-blocked",
			"char_id", session.selectedCharacterID,
			"body_len", len(body),
			"reason", "completed_settlement_entry_owner_not_ready")
		return nil
	}
	if runtime.settlementStatisticReceived {
		if bytes.Equal(runtime.settlementStatisticBody, body) {
			s.logGameEvent(session, "game-dungeon-character-statistic-replay",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"body_len", len(body),
				"reason", "exact_replay_is_idempotent")
			return nil
		}
		s.logGameEvent(session, "game-dungeon-character-statistic-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"body_len", len(body),
			"reason", "conflicting_replay_after_first_capture")
		return nil
	}

	runtime.settlementStatisticReceived = true
	runtime.settlementStatisticBody = append([]byte(nil), body...)
	s.logGameEvent(session, "game-dungeon-character-statistic-captured",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"body_len", len(body),
		"value0", binary.LittleEndian.Uint32(body[0:4]),
		"value1", binary.LittleEndian.Uint32(body[4:8]),
		"value2", binary.LittleEndian.Uint32(body[8:12]),
		"value3", binary.LittleEndian.Uint32(body[12:16]),
		"value4", binary.LittleEndian.Uint32(body[16:20]),
		"value5", binary.LittleEndian.Uint32(body[20:24]),
		"authoritative_for_rewards", false,
		"response_sent", false,
		"source", "current_exe_sub_1CAECD0_six_u32_writer")
	return nil
}

// handleDungeonGiveupGame closes both current EXE op42 meanings without
// conflating them: an active run remains a give-up request, while a completed
// run is accepted only after the server's settlement-entry op31, current EXE
// op46 play-result request, and post-settlement op123 statistic barrier. The
// client owns the wait before it emits op42; the server must not replace that
// lifecycle with an unrelated timer. An active-run typed op24 remains pending
// until positive town-side scene evidence commits it. A completed tutorial's
// first op42 instead uses the already accepted full completed-town route and
// detaches only after that route writes its typed op24.
// A completed raid map with an authenticated frozen entry and accepted raid
// clear journal uses that route without the ordinary op69/70/71 card handshake;
// its rewards remain owned by the raid phase/card flow.
func (s *Service) handleDungeonGiveupGame(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	if len(body) != 0 {
		s.logGameEvent(session, "game-dungeon-giveup-game-blocked",
			"body_len", len(body),
			"reason", "current_exe_op42_body_must_be_empty")
		return nil
	}
	if session.selectedCharacterID == 0 {
		s.logGameEvent(session, "game-dungeon-giveup-game-blocked",
			"body_len", len(body),
			"reason", "selected_character_missing")
		return nil
	}
	if _, err := s.persistCurrentActiveTutorialExit(
		session,
		session.selectedCharacterID,
		"confirmed_mid_tutorial_bodyless_op42_active_run_giveup",
	); err != nil {
		s.logGameEvent(session, "game-dungeon-giveup-game-blocked",
			"char_id", session.selectedCharacterID,
			"reason", "confirmed_tutorial_abandonment_persistence_failed",
			"error", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, session, session.selectedCharacterID)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-giveup-game-blocked",
			"char_id", session.selectedCharacterID,
			"reason", "persisted_town_transition_unavailable",
			"error", err)
		return nil
	}

	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-giveup-game-blocked",
			"char_id", session.selectedCharacterID,
			"reason", "active_dungeon_runtime_owner_missing")
		return nil
	}
	snapshot := runtime.Session.Snapshot()
	// 锦标赛终止态的出本门（本批接线）。厂商 `DungeonClearPresentationPolicy`
	// 对 Tournament 关掉普通结算投影（`CompletesAtClearCommit`，`DungeonEventEnvelope.cs:15-25`），
	// 客户端**不会**走 op46/op123 那套普通结算握手，所以"完成局的 op42"在锦标赛里
	// 只能由**锦标赛自己的**就绪判据放行（`currentDungeonTournamentResultExitReady`）。
	// 默认档 `runtime.Tournament == nil` ⇒ 该判据恒 false ⇒ 下面控制流逐字节不变。
	completedTournament := currentDungeonTournamentResultExitReady(runtime, session.selectedCharacterID)
	if snapshot.Run.Status == worldmap.DungeonRunCompleted && !completedTournament &&
		!completedDungeonSettlementResultReady(runtime, session.selectedCharacterID) {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-giveup-game-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_status", snapshot.Run.Status,
			"settlement_entry_sent", runtime.settlementEntrySent,
			"settlement_play_result_received", runtime.settlementPlayResultReceived,
			"settlement_statistic_received", runtime.settlementStatisticReceived,
			"reason", "completed_run_missing_play_result_or_statistic_barrier")
		return nil
	}
	if snapshot.Run.Status != worldmap.DungeonRunActive && snapshot.Run.Status != worldmap.DungeonRunCompleted {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-giveup-game-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_status", snapshot.Run.Status,
			"reason", "dungeon_run_not_active_or_completed")
		return nil
	}
	if runtime.townReturnPending && snapshot.Run.Status != worldmap.DungeonRunCompleted {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-giveup-game-replay",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_status", snapshot.Run.Status,
			"op24_sent", runtime.townReturnOp24Sent,
			"reason", "town_transition_already_pending")
		return nil
	}

	source := "current_exe_bodyless_op42_active_run_giveup"
	if snapshot.Run.Status == worldmap.DungeonRunCompleted {
		source = "current_exe_op31_completed_tutorial_or_settlement_timed_op42"
		completedTutorial := isPVFTutorialDungeon(runtime)
		completedRaid := s.currentRaidCompletedMapTownReturnReady(session, runtime)
		completedDespair := currentDespairCompletedResultExitReady(runtime, session.selectedCharacterID)
		if completedTutorial {
			source = "current_exe_completed_pvf_tutorial_first_op42_return_to_town"
		}
		if completedTournament {
			// 锦标赛终止态：厂商在锦标赛 clear 上把普通结算投影整个跳过
			// （`DungeonSettlementHandler.cs:2529-2533/:2693-2706`），因此这里同样
			// **不**碰普通翻牌链（auto-flip / reward-retry / 卡链未完成门），
			// 直接放行客户端自有的 返回城镇/再次挑战/选择其它地下城 动作。
			runtime.advanceSettlementPhase(currentDungeonSettlementPhaseEnding)
			session.dungeon.mu.Unlock()
			s.logGameEvent(session, "game-dungeon-tournament-result-exit-accepted",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"request_msg_id", uint16(dnfenum.CmdPacketGiveupGame),
				"run_status", snapshot.Run.Status,
				"completed_rounds", currentTournamentCompletedRounds(runtime),
				"champion", runtime.Tournament != nil && runtime.Tournament.isChampion(),
				"settlement_card_layout_sent", runtime.settlementCardLayoutSent,
				"source", "vendor_DungeonClearPresentationPolicy.cs_Tournament_completes_at_clear_commit")
			return s.sendCurrentCompletedDungeonReturnToTown(
				session,
				runtime,
				transition,
				uint16(dnfenum.CmdPacketGiveupGame),
				source,
			)
		}
		if completedDespair {
			runtime.advanceSettlementPhase(currentDungeonSettlementPhaseEnding)
			session.dungeon.mu.Unlock()
			s.logGameEvent(session, "game-dungeon-despair-completed-exit-accepted",
				"char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID,
				"completion_key", runtime.clearMapCompletionKey,
				"source", "current_exe_completed_floor_op46_op123_op42_without_ordinary_cards")
			return s.sendCurrentCompletedDungeonReturnToTown(session, runtime, transition,
				uint16(dnfenum.CmdPacketGiveupGame), "current_exe_completed_despair_floor_op42_return_to_town")
		}
		if !completedTutorial && !completedRaid && runtime.settlementCardLayoutSent &&
			!runtime.settlementCardSideSelectionSent[dungeonCardSideFree] {
			if err := s.selectCurrentDungeonCardLocked(
				session,
				runtime,
				dungeonCardSideFree,
				0,
				"current_exe_completed_op42_auto_flip_free_row_before_exit",
			); err != nil {
				s.logGameEvent(session, "game-dungeon-giveup-game-completed-auto-flip-blocked",
					"char_id", session.selectedCharacterID,
					"dungeon_id", runtime.Dungeon.ID,
					"request_msg_id", uint16(dnfenum.CmdPacketGiveupGame),
					"error", err)
			}
		}
		if !completedTutorial && !completedRaid && runtime.settlementCardSelectionSent && !runtime.settlementCardRewardCommitted {
			if err := s.commitCurrentDungeonCardRewardLocked(session, runtime, "current_exe_completed_op42_reward_retry_before_exit"); err != nil {
				s.logGameEvent(session, "game-dungeon-giveup-game-completed-exit-reward-retry-blocked",
					"char_id", session.selectedCharacterID,
					"dungeon_id", runtime.Dungeon.ID,
					"request_msg_id", uint16(dnfenum.CmdPacketGiveupGame),
					"error", err)
			}
		}
		if !completedTutorial && !completedRaid && (!runtime.settlementCardLayoutSent ||
			!runtime.settlementCardSelectionSent || !runtime.settlementCardRewardCommitted ||
			runtime.settlementPhase < currentDungeonSettlementPhaseRewardCommitted) {
			s.logGameEvent(session, "game-dungeon-giveup-game-completed-exit-deferred-before-card-reward",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"request_msg_id", uint16(dnfenum.CmdPacketGiveupGame),
				"layout_sent", runtime.settlementCardLayoutSent,
				"selection_sent", runtime.settlementCardSelectionSent,
				"reward_committed", runtime.settlementCardRewardCommitted,
				"settlement_phase", runtime.settlementPhase.String(),
				"reason", "current_exe_card_request_chain_not_finished")
			session.dungeon.mu.Unlock()
			return nil
		}
		runtime.advanceSettlementPhase(currentDungeonSettlementPhaseEnding)
		session.dungeon.mu.Unlock()
		return s.sendCurrentCompletedDungeonReturnToTown(
			session,
			runtime,
			transition,
			uint16(dnfenum.CmdPacketGiveupGame),
			source,
		)
	}
	err = s.sendCurrentDungeonReturnToTownLocked(
		session,
		runtime,
		transition,
		uint16(dnfenum.CmdPacketGiveupGame),
		source,
	)
	session.dungeon.mu.Unlock()
	return err
}

func completedDungeonSettlementExitReady(runtime *runtimeDungeonState, characterID uint16) bool {
	if currentDespairCompletedResultExitReady(runtime, characterID) {
		return true
	}
	if currentDungeonTournamentResultExitReady(runtime, characterID) {
		return true
	}
	if !completedDungeonSettlementResultReady(runtime, characterID) {
		return false
	}
	if isPVFTutorialDungeon(runtime) {
		return true
	}
	return runtime.settlementCardLayoutSent && runtime.settlementCardSelectionSent &&
		runtime.settlementCardRewardCommitted &&
		runtime.settlementPhase >= currentDungeonSettlementPhaseRewardCommitted
}

func completedDungeonSettlementResultReady(runtime *runtimeDungeonState, characterID uint16) bool {
	if currentDungeonTournamentResultExitReady(runtime, characterID) {
		return true
	}
	return completedDungeonSettlementEntryReady(runtime, characterID) &&
		runtime.settlementPlayResultReceived && runtime.settlementStatisticReceived &&
		runtime.settlementClearRewardSent &&
		runtime.settlementPhase >= currentDungeonSettlementPhaseResultShown
}

// currentDungeonTournamentResultExitReady 报告"锦标赛终止态的客户端出本门已经打开"。
//
// **本仓的口径（有实机验收证据）**：厂商在锦标赛 clear 上不发任何普通结算投影
// （`DungeonClearPresentationPolicy.UsesStandardResultProjection` 对 Tournament 为 false，
// `DungeonEventEnvelope.cs:15-17`；`DungeonSettlementHandler.cs:2529-2533/:2693-2706`
// 跳过 NOTI 31/279 与 op34/35/37，只把 run 推到 ResultShown + Completed）。
// 因此普通那套 `settlementPlayResultReceived && settlementStatisticReceived &&
// settlementClearRewardSent` 屏障在锦标赛里**永远不会**满足，完成局的 op42 会被
// 永久拒绝 —— 现场症状就是"奖励拿完却出不了本"。
//
// 就绪判据（全部来自本仓既有的、已发给客户端的状态，不发明新包）：
//  1. run 已被服务端提交为 Completed（冠军+选奖结束后由
//     `submitCurrentTournamentClearCommitLocked` 写入；被淘汰不写）；
//  2. 本局确实有锦标赛运行时，且它已进入终止态；
//  3. NOTI 389 的奖励态已经冻结并**成功下发**（`tournamentReward != nil`）。
//
// 默认档：`runtime.Tournament == nil` ⇒ 恒 false ⇒ 调用方控制流逐字节不变。
//
// 对拍参考：同族 90CN 生产 fork 的 `currentDungeonTournamentResultExitReady`
// （`dnf90-source-oneclick-main/go-server/internal/services/dnfbridge/dungeon_tournament.go:704-706`
// `return currentDungeonTournamentRewardStateReady(...) && runtime.Tournament.RewardStateSent`），
// 其 changlog 记录实机验收已"returns to Shonan and advances quest 6840"。
// 本仓用的是同一判据，只是把"奖励态已下发"表达为 `tournamentReward != nil`
// （本仓的奖励态在 389 发送成功之后才被写上 runtime，见
// `beginCurrentTournamentRewardLocked`）。
func currentDungeonTournamentResultExitReady(runtime *runtimeDungeonState, characterID uint16) bool {
	if runtime == nil || runtime.Tournament == nil || runtime.tournamentReward == nil {
		return false
	}
	if !dungeonRuntimeOwnsCharacter(runtime, characterID) {
		return false
	}
	if runtime.Session == nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		return false
	}
	return !runtime.Tournament.isActive()
}

// currentTournamentCompletedRounds 只用于日志（默认档返回 0）。
func currentTournamentCompletedRounds(runtime *runtimeDungeonState) byte {
	if runtime == nil || runtime.Tournament == nil {
		return 0
	}
	return runtime.Tournament.snapshot().CompletedRounds
}

func completedDungeonSettlementEntryReady(runtime *runtimeDungeonState, characterID uint16) bool {
	if runtime == nil || runtime.Session == nil || !dungeonRuntimeOwnsCharacter(runtime, characterID) ||
		!runtime.settlementEntrySent {
		return false
	}
	return runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunCompleted
}
