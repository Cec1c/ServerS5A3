package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/crystalcontract"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Crystal Contract is a client-applied weapon attribute whose consumable cost
// belongs to the active dungeon lifecycle. Current NoPack sends op535 only
// when the selection changes and never sends a periodic cube-use command.
const currentCrystalContractConsumeInterval = 30 * time.Second

func (s *Service) armCurrentCrystalContractTimer(
	session *gameSession,
	runtime *runtimeDungeonState,
) error {
	if s == nil || session == nil || runtime == nil || currentCrystalContractPracticeRuntime(runtime) {
		return nil
	}
	owner, err := s.currentCrystalContractOwner()
	if err != nil {
		return fmt.Errorf("build crystal contract dungeon owner: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	state, err := owner.State(
		ctx,
		s.accountIDForSession(session),
		strconv.Itoa(int(session.selectedCharacterID)),
		s.gameplayNow().UTC(),
	)
	cancel()
	if err != nil {
		return fmt.Errorf("load crystal contract dungeon state: %w", err)
	}
	if !state.Active || state.Selection == crystalcontract.SelectionNone || state.CubeItemID <= 0 {
		s.syncCurrentCrystalContractTimer(session, state, "副本契约状态已失效")
		return nil
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	return s.scheduleCurrentCrystalContractTimerLocked(session, runtime, queue)
}

func (s *Service) scheduleCurrentCrystalContractTimerLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	queue gameplayTimeQueue,
) error {
	if queue == nil || !currentCrystalContractTimerRuntimeValid(session, runtime) {
		return nil
	}
	if runtime.crystalContractTimerActive {
		return nil
	}
	runtime.crystalContractTimerGeneration = nextCurrentCrystalContractTimerGeneration(
		runtime.crystalContractTimerGeneration,
	)
	generation := runtime.crystalContractTimerGeneration
	timerName := fmt.Sprintf(
		"dnf-crystal-contract:%s:run:%d:tick:%d",
		session.connID,
		runtime.lifecycleToken,
		generation,
	)
	runtime.crystalContractTimerActive = true
	runtime.crystalContractTimerName = timerName
	runtime.crystalContractTimerDueAt = queue.Now().Add(currentCrystalContractConsumeInterval)
	characterID, characterGeneration, err := gameSessionCharacterEventIdentity(
		session,
		session.selectedCharacterID,
	)
	if err != nil {
		s.cancelCurrentCrystalContractTimerLocked(session, runtime, "timer_character_identity_changed")
		return err
	}
	dungeonID := runtime.Dungeon.ID
	runToken := runtime.lifecycleToken
	if err := queue.ScheduleAfter(timerName, currentCrystalContractConsumeInterval, func(due time.Time) {
		postErr := s.postGameSessionCharacterEvent(
			session,
			"crystal-contract-timequeue",
			characterID,
			characterGeneration,
			func() error {
				s.fireCurrentCrystalContractTimer(session, runtime, generation, due)
				return nil
			},
		)
		if postErr != nil && !isClosedGameSessionEventError(postErr) {
			s.logPacketEvent("game-session-event-submit-failed",
				"conn_id", session.connID,
				"source", "crystal-contract-timequeue",
				"char_id", characterID,
				"character_generation", characterGeneration,
				"dungeon_id", dungeonID,
				"run_token", runToken,
				"error", postErr)
		}
	}); err != nil {
		s.cancelCurrentCrystalContractTimerLocked(session, runtime, "timer_schedule_failed")
		return err
	}
	s.logGameEvent(session, "晶体契约-副本扣除已排程",
		"char_id", characterID,
		"dungeon_id", dungeonID,
		"run_token", runToken,
		"timer_generation", generation,
		"timer_name", timerName,
		"due_at_utc", runtime.crystalContractTimerDueAt.UTC().Format(time.RFC3339Nano),
		"interval_seconds", int64(currentCrystalContractConsumeInterval/time.Second),
		"source", "current_exe_no_periodic_c2s_server_owned_dungeon_tick")
	return nil
}

func (s *Service) fireCurrentCrystalContractTimer(
	session *gameSession,
	runtime *runtimeDungeonState,
	generation uint64,
	due time.Time,
) {
	if s == nil || session == nil || runtime == nil {
		return
	}
	session.dungeon.mu.Lock()
	if !runtime.crystalContractTimerActive || runtime.crystalContractTimerGeneration != generation {
		session.dungeon.mu.Unlock()
		return
	}
	if !currentCrystalContractTimerRuntimeValid(session, runtime) {
		s.cancelCurrentCrystalContractTimerLocked(session, runtime, "副本已结束或归属已变更")
		session.dungeon.mu.Unlock()
		return
	}
	runtime.crystalContractTimerActive = false
	runtime.crystalContractTimerName = ""
	runtime.crystalContractTimerDueAt = time.Time{}
	runToken := runtime.lifecycleToken
	dungeonID := runtime.Dungeon.ID
	session.dungeon.mu.Unlock()

	owner, err := s.currentCrystalContractOwner()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		result, consumeErr := owner.ConsumeSelected(
			ctx,
			s.accountIDForSession(session),
			strconv.Itoa(int(session.selectedCharacterID)),
			s.gameplayNow().UTC(),
		)
		cancel()
		err = consumeErr
		if err == nil {
			s.logGameEvent(session, "晶体契约-副本扣除已提交",
				"char_id", session.selectedCharacterID,
				"dungeon_id", dungeonID,
				"run_token", runToken,
				"timer_generation", generation,
				"slot_index", result.SlotIndex,
				"item_id", result.ItemID,
				"consumed", result.Consumed,
				"remaining", result.Remaining,
				"selection_after", result.SelectionAfter,
				"state_source", "atomic_selected_account_crystal_warehouse_tick")
			if sendErr := s.sendCurrentCrystalContractCubeInventoryUpdate(session, result); sendErr != nil {
				s.logGameEvent(session, "晶体契约-库存通知失败",
					"dungeon_id", dungeonID, "run_token", runToken, "error", sendErr)
			}
			if result.SelectionAfter == crystalcontract.SelectionNone {
				if sendErr := s.sendCurrentCrystalContractState(session, "dungeon_tick_after_last_selected_cube"); sendErr != nil {
					s.logGameEvent(session, "晶体契约-状态通知失败",
						"dungeon_id", dungeonID, "run_token", runToken, "error", sendErr)
				}
				return
			}
			s.rearmCurrentCrystalContractTimer(session, runtime)
			return
		}
	}

	stop := errors.Is(err, crystalcontract.ErrContractInactive) ||
		errors.Is(err, crystalcontract.ErrSelectionInvalid) ||
		errors.Is(err, crystalcontract.ErrCubeUnavailable) ||
		errors.Is(err, crystalcontract.ErrCubeRequestMismatch) ||
		errors.Is(err, crystalcontract.ErrCharacterOwnerMismatch) ||
		errors.Is(err, crystalcontract.ErrCharacterNotFound) ||
		errors.Is(err, crystalcontract.ErrAccountNotFound)
	s.logGameEvent(session, "晶体契约-副本扣除失败",
		"char_id", session.selectedCharacterID,
		"dungeon_id", dungeonID,
		"run_token", runToken,
		"timer_generation", generation,
		"terminal_for_current_selection", stop,
		"error", err)
	if stop {
		if sendErr := s.sendCurrentCrystalContractState(session, "dungeon_tick_after_selection_became_unavailable"); sendErr != nil {
			s.logGameEvent(session, "晶体契约-状态通知失败",
				"dungeon_id", dungeonID, "run_token", runToken, "error", sendErr)
		}
		return
	}
	// A transient repository failure must not permanently disable an otherwise
	// active contract for the rest of this dungeon attempt.
	s.rearmCurrentCrystalContractTimer(session, runtime)
}

func (s *Service) rearmCurrentCrystalContractTimer(
	session *gameSession,
	runtime *runtimeDungeonState,
) {
	queue := s.ensureGameplayTimeQueue()
	if queue == nil || session == nil || runtime == nil {
		return
	}
	session.dungeon.mu.Lock()
	err := s.scheduleCurrentCrystalContractTimerLocked(session, runtime, queue)
	session.dungeon.mu.Unlock()
	if err != nil {
		s.logGameEvent(session, "晶体契约-重新排程失败", "error", err)
	}
}

func (s *Service) sendCurrentCrystalContractCubeInventoryUpdate(
	session *gameSession,
	result crystalcontract.ConsumeResult,
) error {
	stack := dnfrepo.ItemStack{
		ItemID: result.ItemID,
		Count:  result.Remaining,
		Extra:  map[string]string{"item_kind": "stackable"},
	}
	if result.Remaining <= 0 {
		stack.ItemID = 0
		stack.Count = 0
	}
	entry := currentItemListEntryFromStack(
		dnfrepo.MainInventoryListType,
		int16(result.SlotIndex),
		stack,
	)
	body := buildCurrentItemUpdateBody(
		dnfrepo.MainInventoryListType,
		[]currentItemListEntry{entry},
	)
	return s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		body,
		0,
	)
}

// cancelCurrentCrystalContractTimerLocked invalidates a dequeued callback and
// removes the pending named task. The caller holds session.dungeon.mu.
func (s *Service) cancelCurrentCrystalContractTimerLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source string,
) {
	if runtime == nil {
		return
	}
	wasActive := runtime.crystalContractTimerActive
	timerName := runtime.crystalContractTimerName
	previousGeneration := runtime.crystalContractTimerGeneration
	runtime.crystalContractTimerGeneration = nextCurrentCrystalContractTimerGeneration(previousGeneration)
	runtime.crystalContractTimerActive = false
	runtime.crystalContractTimerName = ""
	runtime.crystalContractTimerDueAt = time.Time{}
	cancelled := false
	if timerName != "" && s != nil && s.gameplayTimers != nil {
		cancelled = s.gameplayTimers.Cancel(timerName)
	}
	if wasActive || timerName != "" {
		s.logGameEvent(session, "晶体契约-副本扣除已取消",
			"char_id", runtime.Character.CharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_token", runtime.lifecycleToken,
			"previous_generation", previousGeneration,
			"current_generation", runtime.crystalContractTimerGeneration,
			"timer_name", timerName,
			"queue_item_cancelled", cancelled,
			"source", source)
	}
}

func nextCurrentCrystalContractTimerGeneration(current uint64) uint64 {
	next := current + 1
	if next == 0 {
		next++
	}
	return next
}

// 调用者持有 dungeon.mu；只对已完成进图落位的当前角色、当前局扣除。
func currentCrystalContractTimerRuntimeValid(session *gameSession, runtime *runtimeDungeonState) bool {
	return session != nil && runtime != nil && session.dungeon.runtime == runtime &&
		runtime.lifecycleToken != 0 && runtime.lifecycleToken == session.dungeon.runToken &&
		dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) &&
		session.postStartMapPlayerStateSent && !runtime.townReturnPending &&
		runtime.Session != nil && runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunActive &&
		!currentCrystalContractPracticeRuntime(runtime)
}

func currentCrystalContractPracticeRuntime(runtime *runtimeDungeonState) bool {
	return runtime != nil && runtime.Request.HellPartyRequestFlag == 0 && runtime.Request.HellPartyDifficultyFlag != 0
}

// 切换颜色保留本周期；取消立即使已出队的回调失效，再启用另起周期。
func (s *Service) syncCurrentCrystalContractTimer(session *gameSession, state crystalcontract.State, source string) {
	if s == nil || session == nil {
		return
	}
	queue := s.ensureGameplayTimeQueue()
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if !state.Active || state.Selection == crystalcontract.SelectionNone || state.CubeItemID <= 0 {
		s.cancelCurrentCrystalContractTimerLocked(session, runtime, source)
		session.dungeon.mu.Unlock()
		return
	}
	err := s.scheduleCurrentCrystalContractTimerLocked(session, runtime, queue)
	session.dungeon.mu.Unlock()
	if err != nil {
		s.logGameEvent(session, "晶体契约-重新排程失败", "source", source, "error", err)
	}
}

// 契约发放和回城撤销可能发生在 dungeon.mu 内。经队列转回会话事件，
// 再读取当前权益及选择，避免重入锁，也不使用发放时已过期的选择快照。
func (s *Service) requestCurrentCrystalContractTimerRefresh(session *gameSession) {
	if s == nil || session == nil || !session.postStartMapPlayerStateSent {
		return
	}
	characterID, generation, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		return
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return
	}
	name := fmt.Sprintf("dnf-crystal-contract-refresh:%s:%d:%d", session.connID, characterID, generation)
	err = queue.ScheduleAfter(name, 0, func(time.Time) {
		err := s.postGameSessionCharacterEvent(session, "crystal-contract-refresh", characterID, generation, func() error {
			session.dungeon.mu.Lock()
			runtime := session.dungeon.runtime
			session.dungeon.mu.Unlock()
			return s.armCurrentCrystalContractTimer(session, runtime)
		})
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logGameEvent(session, "晶体契约-状态排程失败", "error", err)
		}
	})
	if err != nil {
		s.logGameEvent(session, "晶体契约-状态排程失败", "error", err)
	}
}
