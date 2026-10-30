package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"time"
)

const currentTownEnterSelectReasonPositionNotReported = "town_scene_position_not_reported"

const currentTownEnterSelectDeferredLifetime = 3 * time.Second

const currentTownEnterSelectUpperRequestSource = "upper_15"

// currentTownEnterSelectDeferredRequest preserves the client's intent when a
// town area is committed but its first op35 position report has not arrived.
// The old path emitted the op15 success ACK and fatigue without op27, then lost
// the request; the client saw no selectable dungeon page until it clicked again.
type currentTownEnterSelectDeferredRequest struct {
	CharacterID         uint16
	CharacterGeneration uint64
	SceneGeneration     uint64
	TownID              byte
	AreaID              byte
	Source              string
	DeferredAt          time.Time
	PartyID             int
	PartyLeaderID       uint16
	PartyMemberCount    int
	PartyMembers        [4]uint16
	PartyBound          bool
}

type currentTownEnterSelectPartySnapshot struct {
	PartyID     int
	LeaderID    uint16
	MemberCount int
	Members     [4]uint16
	Bound       bool
}

func (s *Service) currentTownEnterSelectPartyFence(session *gameSession) currentTownEnterSelectPartySnapshot {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if bound {
		manager := s.runtimePartyManagerForService()
		if manager != nil {
			if snapshot, found := manager.SnapshotByUser(identity.character, identity.generation); found {
				ids := make([]uint16, 0, len(snapshot.Members))
				for _, member := range snapshot.Members {
					if member.UserID != 0 {
						ids = append(ids, member.UserID)
					}
				}
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				var members [4]uint16
				count := len(ids)
				if count > len(members) {
					count = len(members)
				}
				copy(members[:], ids[:count])
				return currentTownEnterSelectPartySnapshot{
					PartyID: int(snapshot.ID), LeaderID: snapshot.Leader,
					MemberCount: count, Members: members, Bound: true,
				}
			}
		}
		return currentTownEnterSelectPartySnapshot{Bound: true}
	}
	state := runtimePartyStateSnapshot(session)
	members := runtimePartyMembers(state)
	ids := make([]uint16, 0, len(members))
	for _, member := range members {
		if member.UserID != 0 {
			ids = append(ids, member.UserID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var result [4]uint16
	count := len(ids)
	if count > len(result) {
		count = len(result)
	}
	copy(result[:], ids[:count])
	return currentTownEnterSelectPartySnapshot{
		PartyID: state.PartyID, LeaderID: state.UserID,
		MemberCount: count, Members: result,
	}
}

func currentTownEnterSelectPartyFenceEqual(
	current currentTownEnterSelectPartySnapshot,
	deferred *currentTownEnterSelectDeferredRequest,
) string {
	if current.Bound != deferred.PartyBound {
		return "party_authority_changed"
	}
	if current.PartyID != deferred.PartyID {
		return "party_id_changed"
	}
	if current.LeaderID != deferred.PartyLeaderID {
		return "party_leader_changed"
	}
	if current.MemberCount != deferred.PartyMemberCount || current.Members != deferred.PartyMembers {
		return "party_membership_changed"
	}
	return ""
}

// resetCurrentTownEnterSelectSceneLocked invalidates deferred op15 work when
// a return or scene reset reuses the same character/town/area tuple. The
// caller must hold townMu.
func resetCurrentTownEnterSelectSceneLocked(session *gameSession) {
	if session == nil {
		return
	}
	session.townSceneGeneration++
	if session.townSceneGeneration == 0 {
		session.townSceneGeneration = 1
	}
	session.townEnterSelectDeferred = nil
}

func (s *Service) markCurrentTownSceneReady(session *gameSession) {
	if session == nil || session.selectedCharacterID == 0 {
		return
	}
	session.townMu.Lock()
	session.townSceneReadyCharacterID = session.selectedCharacterID
	session.townMu.Unlock()
}

func (s *Service) currentTownEnterSelectReady(session *gameSession) (bool, string) {
	if session == nil || session.selectedCharacterID == 0 {
		return false, "selected_character_missing"
	}
	session.townMu.Lock()
	readyCharacterID := session.townSceneReadyCharacterID
	positionSnapshot := session.townPositionSnapshot
	session.townMu.Unlock()
	if readyCharacterID == 0 || readyCharacterID != session.selectedCharacterID {
		return false, "town_scene_player_state_not_finalized"
	}
	if positionSnapshot.CharacterID != session.selectedCharacterID {
		return false, "town_scene_location_owner_missing"
	}
	session.dungeon.mu.Lock()
	hasDungeonRuntime := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if hasDungeonRuntime {
		return false, "active_dungeon_runtime"
	}
	// A retained runtime also fences the selector when its Session has not
	// been attached yet. Do not classify that boundary as a missing position
	// and park an op15 which must keep the existing ACK-only behavior.
	if !positionSnapshot.PositionValid {
		return false, currentTownEnterSelectReasonPositionNotReported
	}
	return true, "current_town_scene_player_state_finalized"
}

func (s *Service) deferCurrentTownEnterSelectRequest(session *gameSession, source, reason string) bool {
	if session == nil || session.selectedCharacterID == 0 || session.characterGeneration == 0 {
		return false
	}
	characterID := session.selectedCharacterID
	characterGeneration := session.characterGeneration
	if identity, current := s.boundGameSessionCharacterSnapshot(session); current {
		characterID, characterGeneration = identity.character, identity.generation
	}
	if s.sessionHasActiveDungeonRuntime(session) {
		return false
	}
	ready, readyReason := s.currentTownEnterSelectReady(session)
	if ready || readyReason != currentTownEnterSelectReasonPositionNotReported {
		return false
	}
	party := s.currentTownEnterSelectPartyFence(session)
	deferred := &currentTownEnterSelectDeferredRequest{
		CharacterID:         characterID,
		CharacterGeneration: characterGeneration,
		Source:              source,
		DeferredAt:          time.Now(),
		PartyID:             party.PartyID,
		PartyLeaderID:       party.LeaderID,
		PartyMemberCount:    party.MemberCount,
		PartyMembers:        party.Members,
		PartyBound:          party.Bound,
	}
	session.townMu.Lock()
	snapshot := session.townPositionSnapshot
	if session.selectedCharacterID != deferred.CharacterID || session.characterGeneration != deferred.CharacterGeneration ||
		session.townSceneReadyCharacterID != deferred.CharacterID ||
		snapshot.CharacterID != deferred.CharacterID || snapshot.PositionValid {
		session.townMu.Unlock()
		return false
	}
	deferred.SceneGeneration = session.townSceneGeneration
	deferred.TownID, deferred.AreaID = snapshot.TownID, snapshot.AreaID
	session.townEnterSelectDeferred = deferred
	session.townMu.Unlock()
	s.logInfo("dnfbridge town enter-select request deferred until current op35 position",
		"conn_id", session.connID,
		"char_id", deferred.CharacterID,
		"character_generation", deferred.CharacterGeneration,
		"town_scene_generation", deferred.SceneGeneration,
		"town_id", deferred.TownID,
		"area_id", deferred.AreaID,
		"source", source,
		"reason", reason)
	return true
}

func (s *Service) clearDeferredTownEnterSelectRequest(session *gameSession, reason string) {
	if session == nil {
		return
	}
	session.townMu.Lock()
	deferred := session.townEnterSelectDeferred
	session.townEnterSelectDeferred = nil
	session.townMu.Unlock()
	if deferred != nil && reason != "" {
		s.logInfo("dnfbridge town enter-select deferred request discarded",
			"conn_id", session.connID,
			"char_id", deferred.CharacterID,
			"source", deferred.Source,
			"reason", reason)
	}
}

func (s *Service) resumeDeferredTownEnterSelectAfterPosition(session *gameSession) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	session.townMu.Lock()
	deferred := session.townEnterSelectDeferred
	if deferred == nil {
		session.townMu.Unlock()
		return nil
	}
	currentSnapshot := session.townPositionSnapshot
	currentCharacterGeneration := session.characterGeneration
	currentTownSceneGeneration := session.townSceneGeneration
	discardReason := ""
	switch {
	case deferred.CharacterID != session.selectedCharacterID:
		discardReason = "character_changed"
	case deferred.CharacterGeneration != currentCharacterGeneration:
		discardReason = "character_generation_changed"
	case deferred.SceneGeneration != currentTownSceneGeneration:
		discardReason = "town_scene_generation_changed"
	case currentSnapshot.CharacterID != deferred.CharacterID || currentSnapshot.TownID != deferred.TownID || currentSnapshot.AreaID != deferred.AreaID:
		discardReason = "town_scene_snapshot_changed"
	}
	if discardReason != "" {
		session.townEnterSelectDeferred = nil
		session.townMu.Unlock()
		s.logInfo("dnfbridge town enter-select deferred request discarded",
			"conn_id", session.connID,
			"char_id", deferred.CharacterID,
			"source", deferred.Source,
			"reason", discardReason)
		return nil
	}
	session.townMu.Unlock()

	if age := time.Since(deferred.DeferredAt); age > currentTownEnterSelectDeferredLifetime {
		s.clearDeferredTownEnterSelectRequest(session, "deadline_expired")
		return nil
	}
	if currentIdentity, current := s.boundGameSessionCharacterSnapshot(session); current &&
		(currentIdentity.character != deferred.CharacterID || currentIdentity.generation != deferred.CharacterGeneration) {
		s.clearDeferredTownEnterSelectRequest(session, "bound_character_identity_changed")
		return nil
	} else if deferred.PartyBound && !current {
		s.clearDeferredTownEnterSelectRequest(session, "session_binding_removed")
		return nil
	}
	if reason := currentTownEnterSelectPartyFenceEqual(s.currentTownEnterSelectPartyFence(session), deferred); reason != "" {
		s.clearDeferredTownEnterSelectRequest(session, reason)
		return nil
	}

	ready, reason := s.currentTownEnterSelectReady(session)
	if !ready {
		s.clearDeferredTownEnterSelectRequest(session, "not_ready_"+reason)
		return nil
	}

	session.townMu.Lock()
	if session.townEnterSelectDeferred != deferred {
		session.townMu.Unlock()
		return nil
	}
	session.townEnterSelectDeferred = nil
	session.townMu.Unlock()
	s.logInfo("dnfbridge town enter-select deferred request resumed after current op35 position",
		"conn_id", session.connID,
		"char_id", deferred.CharacterID,
		"character_generation", deferred.CharacterGeneration,
		"town_scene_generation", deferred.SceneGeneration,
		"town_id", deferred.TownID,
		"area_id", deferred.AreaID,
		"source", deferred.Source,
		"deferred_ms", time.Since(deferred.DeferredAt).Milliseconds())
	return s.sendEnterSelectDungeonState(session, deferred.Source+"_after_current_op35_position", false, true)
}

// commitPendingDungeonReturnBeforeTownEnterSelect repairs the current EXE
// route where the client returns to town from an active dungeon and immediately
// clicks the dungeon gate again. That op15 can be the first town-side request
// after the typed return op24, so the wrapper commits the pending runtime and
// restores the town position owner needed by sendCurrentTownEnterSelectContext.
func (s *Service) commitPendingDungeonReturnBeforeTownEnterSelect(session *gameSession, source string) error {
	return s.commitPendingDungeonReturnBeforeTownAction(session, source, "current_exe_op15_enter_select_after_pending_town_return", nil)
}

// 选择器和体验结束后的正式转职都可能成为op24之后的第一条城镇动作。
// 共用已有回城提交、角色重建和来源坐标恢复，避免转职绕过场景确认。
func (s *Service) commitPendingDungeonReturnBeforeTownAction(session *gameSession, source, confirmationSource string, expectedRuntime *runtimeDungeonState) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	characterID := session.selectedCharacterID

	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if expectedRuntime != nil && runtime != expectedRuntime {
		session.dungeon.mu.Unlock()
		return fmt.Errorf("转职体验回城所属副本已改变")
	}
	shouldCommit := runtime != nil && runtime.Session != nil &&
		runtime.townReturnPending && runtime.townReturnOp24Sent
	transition := currentDungeonTownTransition{}
	origin := currentTownPositionSnapshot{}
	if shouldCommit {
		transition = cloneCurrentDungeonTownTransition(runtime.townReturnTransition)
		origin = runtime.townReturnOrigin
	}
	session.dungeon.mu.Unlock()
	if !shouldCommit {
		return s.ensureCurrentConfirmedDungeonReturnPlayerState(
			session,
			source+"_without_retained_runtime",
		)
	}
	snapshot := currentTownPositionSnapshot{
		CharacterID:   characterID,
		TownID:        transition.TownID,
		AreaID:        transition.AreaID,
		PositionX:     uint16(transition.PositionX),
		PositionY:     uint16(transition.PositionY),
		MovementCode:  transition.Direction,
		PositionValid: true,
	}
	positionSource := transition.PositionSource
	if origin.CharacterID == characterID &&
		origin.TownID == transition.TownID &&
		origin.AreaID == transition.AreaID &&
		origin.PositionValid {
		snapshot = origin
		positionSource = "current_exe_op35_runtime_origin_snapshot"
	}

	// 已确认的返回坐标先保存在会话；不提前标记城镇就绪。
	// 回城ACK或角色投影中途写失败后，原runtime可能已经退休，重试仍需该坐标。
	session.townMu.Lock()
	if session.selectedCharacterID != characterID {
		session.townMu.Unlock()
		return fmt.Errorf("回城确认期间所选角色已改变")
	}
	resetCurrentTownEnterSelectSceneLocked(session)
	session.townPositionSnapshot = snapshot
	session.townMu.Unlock()
	if err := s.commitPendingDungeonReturnForExpectedSceneRequest(session, confirmationSource, runtime); err != nil {
		return err
	}

	session.dungeon.mu.Lock()
	stillHasRuntime := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if stillHasRuntime || session.selectedCharacterID != characterID {
		s.logGameEvent(session, "game-upper-enter-select-dungeon-pending-return-committed",
			"source", source,
			"char_id", characterID,
			"runtime_retained", stillHasRuntime,
			"selected_character", session.selectedCharacterID,
			"reason", "pending_return_committed_but_town_context_changed")
		return nil
	}

	session.townMu.Lock()
	clearCurrentTownSelectorOriginLocked(session)
	resetCurrentTownEnterSelectSceneLocked(session)
	session.townSceneReadyCharacterID = characterID
	session.townPositionSnapshot = snapshot
	session.townMu.Unlock()

	s.logGameEvent(session, "game-upper-enter-select-dungeon-pending-return-committed",
		"source", source,
		"char_id", characterID,
		"town_id", snapshot.TownID,
		"area_id", snapshot.AreaID,
		"position_x", snapshot.PositionX,
		"position_y", snapshot.PositionY,
		"movement_code", snapshot.MovementCode,
		"position_source", positionSource,
		"reason", "town_action_can_be_first_confirmation_after_back_to_village")
	return nil
}

// retireCompletedDungeonForTownSelect maps the settlement page's
// "select another dungeon" action onto the current op15/op27 selector owner.
// 86JP establishes that this action retires the completed run before entering
// the selector; the location itself is reconstructed from the current run's
// frozen op35 origin or the current Go/PVF town-transition builder.
func (s *Service) retireCompletedDungeonForTownSelect(session *gameSession) (bool, error) {
	return s.retireCompletedDungeonForTownAction(session, false)
}

func (s *Service) retireCompletedDungeonForTownAction(session *gameSession, requireOpenRaidMap bool, expected ...*runtimeDungeonState) (bool, error) {
	if session == nil || session.selectedCharacterID == 0 {
		return false, nil
	}
	characterID := session.selectedCharacterID
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil || (len(expected) > 0 && runtime != expected[0]) ||
		(!s.currentCompletedDungeonTownActionReady(session, runtime, requireOpenRaidMap) || (requireOpenRaidMap && runtime.raidEntry != nil && !s.currentRaidDungeonSelectionAllowed(session, uint32(runtime.Dungeon.ID)))) {
		session.dungeon.mu.Unlock()
		return false, nil
	}
	origin := runtime.townReturnOrigin
	session.dungeon.mu.Unlock()

	originSource := "frozen_pre_dungeon_op35_origin"
	if origin.CharacterID != characterID || !origin.PositionValid {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, session, characterID)
		cancel()
		if err != nil {
			return false, err
		}
		origin = currentTownPositionSnapshot{
			CharacterID:   characterID,
			TownID:        transition.TownID,
			AreaID:        transition.AreaID,
			PositionX:     uint16(transition.PositionX),
			PositionY:     uint16(transition.PositionY),
			MovementCode:  transition.Direction,
			PositionValid: true,
		}
		originSource = transition.PositionSource
	}

	session.dungeon.mu.Lock()
	if session.dungeon.runtime != runtime ||
		(!s.currentCompletedDungeonTownActionReady(session, runtime, requireOpenRaidMap) || (requireOpenRaidMap && runtime.raidEntry != nil && !s.currentRaidDungeonSelectionAllowed(session, uint32(runtime.Dungeon.ID)))) {
		session.dungeon.mu.Unlock()
		return false, nil
	}
	s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "completed_run_retires_into_other_dungeon_selector")
	s.cancelCurrentCrystalContractTimerLocked(session, runtime, "completed_run_retires_into_other_dungeon_selector")
	s.cancelCurrentDungeonCardAutoFlipLocked(session, runtime, "completed_run_retires_into_other_dungeon_selector")
	session.dungeon.runtime = nil
	retirementSource := "completed_run_other_dungeon_selector"
	if requireOpenRaidMap {
		// Retry retains its old return owner until a new standard entry commits.
		// Keep the same committed occupancy through that reversible window.
		retirementSource = "completed_run_retry"
	}
	s.releasePartyDungeonInstance(runtime, characterID, retirementSource)
	session.dungeon.mu.Unlock()
	if err := s.switchCurrentPetGrowthClock(session, currentPetGrowthClockTown, s.gameplayNow(), "completed_run_other_dungeon_selector"); err != nil {
		s.logGameEvent(session, "game-pet-growth-clock-start-deferred",
			"char_id", characterID,
			"mode", currentPetGrowthClockTown.String(),
			"source", "completed_run_other_dungeon_selector",
			"error", err)
	}

	resetDungeonEntrySceneGates(session)
	clearCurrentDungeonSelectContext(session)
	session.townMu.Lock()
	clearCurrentTownSelectorOriginLocked(session)
	resetCurrentTownEnterSelectSceneLocked(session)
	session.townSceneReadyCharacterID = characterID
	session.townPositionSnapshot = origin
	session.townMu.Unlock()
	s.logGameEvent(session, "game-dungeon-completed-other-selector-committed",
		"char_id", characterID,
		"dungeon_id", runtime.Dungeon.ID,
		"town_id", origin.TownID,
		"area_id", origin.AreaID,
		"position_x", origin.PositionX,
		"position_y", origin.PositionY,
		"position_source", originSource,
		"runtime_detached", true,
		"next_owner", "current_exe_op15_ack_fatigue_op27_selector_context")
	return true, nil
}

func (s *Service) sendCurrentTownEnterSelectContext(session *gameSession, source string) error {
	ready, reason := s.currentTownEnterSelectReady(session)
	if !ready {
		s.logGameEvent(session, "game-upper-town-enter-select-context-deferred",
			"source", source,
			"char_id", session.selectedCharacterID,
			"msg_id", currentDungeonContextMsgID,
			"reason", reason)
		return nil
	}
	// 深渊派对按钮的置灰判据必须在**这一条 op27 之前**算出来：blocked 槽位唯一
	// 的落点就是它（= 厂商 `NOTI 0x001B`），厂商也在同一个位置算
	// （`DungeonEntryHandler.cs:723-726` 建、`:780-786` 随 NOTI 下发）。
	// 回退档（`DNFBRIDGE_HELL_SELECT_PAGE_ELIGIBILITY=legacy`）或
	// `DNFBRIDGE_HELL_BLOCKED_SLOTS` 未开时本调用一个字节都不碰。
	// 见 dungeon_hell_select_page.go。
	s.refreshHellSelectPageEligibility(session, source)
	if err := s.sendCurrentDungeonContextOp27(session, source); err != nil {
		return err
	}
	origin, originBound := bindCurrentTownSelectorOrigin(session)
	s.logGameEvent(session, "game-upper-town-enter-select-origin-bound",
		"source", source,
		"char_id", session.selectedCharacterID,
		"town_id", origin.TownID,
		"area_id", origin.AreaID,
		"position_x", origin.PositionX,
		"position_y", origin.PositionY,
		"position_valid", origin.PositionValid,
		"origin_bound", originBound,
		"reason", "freeze_current_town_origin_after_successful_op27_write")
	session.enterSelectDungeonContextSent = true
	return nil
}
