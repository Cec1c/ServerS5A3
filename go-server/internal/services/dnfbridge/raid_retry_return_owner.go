package dnfbridge

import (
	"context"
	"fmt"
)

// Keep the completed run as a return owner until the standard entry commits a
// replacement. A refusal after peers retired restores only the same bound
// character generation; it never overwrites a new runtime or grants assets.
type currentRaidRetryReturnOwner struct {
	session    *gameSession
	runtime    *runtimeDungeonState
	character  uint16
	generation uint64
	gates      currentRaidRetrySceneGates
	retired    bool
}
type currentRaidRetrySceneGates struct {
	sceneBootstrapTailPacketIndex           int
	sceneBootstrapTailObjectMode1Pending    bool
	sceneBootstrapTailPostStage             currentDeferredSelectSceneTailPostStage
	confirmedDungeonReturnStatePending      bool
	initialTownRouteCharacterID             uint16
	initialTownRouteStage                   currentInitialTownRouteStage
	initialTownLocationNotificationsSent    bool
	initialTownQuestSnapshotsSent           bool
	initialTownSkillInfoPrepared            bool
	initialTownSkillInfoSent                bool
	initialTownSkillInfo                    currentSceneSkillInfoProjection
	initialTownLegacySceneReadyAccepted     bool
	initialTownAdventureOverheadRefreshSent bool
	initialTownCombatPowerAffixesSent       bool
	townSceneReadyCharacterID               uint16
	townActorOwnerChannel                   byte
	sceneBootstrapTailDeferred              bool
	sceneBootstrapTailSent                  bool
	runtimeAfterBlacklistSent               bool
	runtimeFinishLoadingGateSent            bool
	fpsFinishLoadingGateSent                bool
	selectedUserInfoRefreshSent             bool
	selectedUserInfoMode3Sent               bool
	currentSceneObjectListSent              bool
	selectedItemListRefreshSent             bool
	selectedItemListBootstrapCharacterID    uint16
	selectedEquipmentUpdateSent             bool
	selectedCreatureStateTableSent          bool
	selectedRentalWalletStateSent           bool
	expertJobInfoCharacterID                uint16
	selectPreviewActorRemoved               bool
	preDungeonContextPlayerStateSent        bool
	postStartMapPlayerStateSent             bool
	deferredDungeonUserStateObjectKey       uint16
	currentFinishLoadingStateSent           bool
	currentFinishLoadingCompletionSent      bool
	postFinishLoadingPlayerStateSent        bool
	returnTownFinishLoadingAckOnly          bool
	enterSelectDungeonSent                  bool
	enterSelectDungeonAckSent               bool
	enterSelectDungeonContextSent           bool
	backToVillageEnterSelectPending         bool
	townSelectorOriginSnapshot              currentTownPositionSnapshot
	townSelectorOriginBound                 bool
	townPositionSnapshot                    currentTownPositionSnapshot
}

func captureCurrentRaidRetrySceneGates(session *gameSession) currentRaidRetrySceneGates {
	return currentRaidRetrySceneGates{sceneBootstrapTailPacketIndex: session.sceneBootstrapTailPacketIndex, sceneBootstrapTailObjectMode1Pending: session.sceneBootstrapTailObjectMode1Pending, sceneBootstrapTailPostStage: session.sceneBootstrapTailPostStage, confirmedDungeonReturnStatePending: session.confirmedDungeonReturnStatePending, initialTownRouteCharacterID: session.initialTownRouteCharacterID, initialTownRouteStage: session.initialTownRouteStage, initialTownLocationNotificationsSent: session.initialTownLocationNotificationsSent, initialTownQuestSnapshotsSent: session.initialTownQuestSnapshotsSent, initialTownSkillInfoPrepared: session.initialTownSkillInfoPrepared, initialTownSkillInfoSent: session.initialTownSkillInfoSent, initialTownSkillInfo: session.initialTownSkillInfo, initialTownLegacySceneReadyAccepted: session.initialTownLegacySceneReadyAccepted, initialTownAdventureOverheadRefreshSent: session.initialTownAdventureOverheadRefreshSent, initialTownCombatPowerAffixesSent: session.initialTownCombatPowerAffixesSent, townSceneReadyCharacterID: session.townSceneReadyCharacterID, townActorOwnerChannel: session.townActorOwnerChannel, sceneBootstrapTailDeferred: session.sceneBootstrapTailDeferred, sceneBootstrapTailSent: session.sceneBootstrapTailSent, runtimeAfterBlacklistSent: session.runtimeAfterBlacklistSent, runtimeFinishLoadingGateSent: session.runtimeFinishLoadingGateSent, fpsFinishLoadingGateSent: session.fpsFinishLoadingGateSent, selectedUserInfoRefreshSent: session.selectedUserInfoRefreshSent, selectedUserInfoMode3Sent: session.selectedUserInfoMode3Sent, currentSceneObjectListSent: session.currentSceneObjectListSent, selectedItemListRefreshSent: session.selectedItemListRefreshSent, selectedItemListBootstrapCharacterID: session.selectedItemListBootstrapCharacterID, selectedEquipmentUpdateSent: session.selectedEquipmentUpdateSent, selectedCreatureStateTableSent: session.selectedCreatureStateTableSent, selectedRentalWalletStateSent: session.selectedRentalWalletStateSent, expertJobInfoCharacterID: session.expertJobInfoCharacterID, selectPreviewActorRemoved: session.selectPreviewActorRemoved, preDungeonContextPlayerStateSent: session.preDungeonContextPlayerStateSent, postStartMapPlayerStateSent: session.postStartMapPlayerStateSent, deferredDungeonUserStateObjectKey: session.deferredDungeonUserStateObjectKey, currentFinishLoadingStateSent: session.currentFinishLoadingStateSent, currentFinishLoadingCompletionSent: session.currentFinishLoadingCompletionSent, postFinishLoadingPlayerStateSent: session.postFinishLoadingPlayerStateSent, returnTownFinishLoadingAckOnly: session.returnTownFinishLoadingAckOnly, enterSelectDungeonSent: session.enterSelectDungeonSent, enterSelectDungeonAckSent: session.enterSelectDungeonAckSent, enterSelectDungeonContextSent: session.enterSelectDungeonContextSent, backToVillageEnterSelectPending: session.backToVillageEnterSelectPending, townSelectorOriginSnapshot: session.townSelectorOriginSnapshot, townSelectorOriginBound: session.townSelectorOriginBound, townPositionSnapshot: session.townPositionSnapshot}
}
func (g currentRaidRetrySceneGates) restore(session *gameSession) {
	session.sceneBootstrapTailPacketIndex = g.sceneBootstrapTailPacketIndex
	session.sceneBootstrapTailObjectMode1Pending = g.sceneBootstrapTailObjectMode1Pending
	session.sceneBootstrapTailPostStage = g.sceneBootstrapTailPostStage
	session.confirmedDungeonReturnStatePending = g.confirmedDungeonReturnStatePending
	session.initialTownRouteCharacterID = g.initialTownRouteCharacterID
	session.initialTownRouteStage = g.initialTownRouteStage
	session.initialTownLocationNotificationsSent = g.initialTownLocationNotificationsSent
	session.initialTownQuestSnapshotsSent = g.initialTownQuestSnapshotsSent
	session.initialTownSkillInfoPrepared = g.initialTownSkillInfoPrepared
	session.initialTownSkillInfoSent = g.initialTownSkillInfoSent
	session.initialTownSkillInfo = g.initialTownSkillInfo
	session.initialTownLegacySceneReadyAccepted = g.initialTownLegacySceneReadyAccepted
	session.initialTownAdventureOverheadRefreshSent = g.initialTownAdventureOverheadRefreshSent
	session.initialTownCombatPowerAffixesSent = g.initialTownCombatPowerAffixesSent
	session.townSceneReadyCharacterID = g.townSceneReadyCharacterID
	session.townActorOwnerChannel = g.townActorOwnerChannel
	session.sceneBootstrapTailDeferred = g.sceneBootstrapTailDeferred
	session.sceneBootstrapTailSent = g.sceneBootstrapTailSent
	session.runtimeAfterBlacklistSent = g.runtimeAfterBlacklistSent
	session.runtimeFinishLoadingGateSent = g.runtimeFinishLoadingGateSent
	session.fpsFinishLoadingGateSent = g.fpsFinishLoadingGateSent
	session.selectedUserInfoRefreshSent = g.selectedUserInfoRefreshSent
	session.selectedUserInfoMode3Sent = g.selectedUserInfoMode3Sent
	session.currentSceneObjectListSent = g.currentSceneObjectListSent
	session.selectedItemListRefreshSent = g.selectedItemListRefreshSent
	session.selectedItemListBootstrapCharacterID = g.selectedItemListBootstrapCharacterID
	session.selectedEquipmentUpdateSent = g.selectedEquipmentUpdateSent
	session.selectedCreatureStateTableSent = g.selectedCreatureStateTableSent
	session.selectedRentalWalletStateSent = g.selectedRentalWalletStateSent
	session.expertJobInfoCharacterID = g.expertJobInfoCharacterID
	session.selectPreviewActorRemoved = g.selectPreviewActorRemoved
	session.preDungeonContextPlayerStateSent = g.preDungeonContextPlayerStateSent
	session.postStartMapPlayerStateSent = g.postStartMapPlayerStateSent
	session.deferredDungeonUserStateObjectKey = g.deferredDungeonUserStateObjectKey
	session.currentFinishLoadingStateSent = g.currentFinishLoadingStateSent
	session.currentFinishLoadingCompletionSent = g.currentFinishLoadingCompletionSent
	session.postFinishLoadingPlayerStateSent = g.postFinishLoadingPlayerStateSent
	session.returnTownFinishLoadingAckOnly = g.returnTownFinishLoadingAckOnly
	session.enterSelectDungeonSent = g.enterSelectDungeonSent
	session.enterSelectDungeonAckSent = g.enterSelectDungeonAckSent
	session.enterSelectDungeonContextSent = g.enterSelectDungeonContextSent
	session.backToVillageEnterSelectPending = g.backToVillageEnterSelectPending
	session.townSelectorOriginSnapshot = g.townSelectorOriginSnapshot
	session.townSelectorOriginBound = g.townSelectorOriginBound
	session.townPositionSnapshot = g.townPositionSnapshot
}

func (s *Service) captureCurrentRaidRetryReturnOwners(leader *gameSession, runtime *runtimeDungeonState) ([]currentRaidRetryReturnOwner, error) {
	if runtime.raidEntry == nil {
		return nil, nil
	}
	ids := append([]uint16(nil), runtime.raidEntry.Participants...)
	owners := make([]currentRaidRetryReturnOwner, 0, len(ids))
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	for _, id := range ids {
		peer, ok := s.onlineGameSession(id)
		if !ok || peer == nil {
			return nil, fmt.Errorf("raid retry return owner %d offline", id)
		}
		capture := func() error {
			peer.dungeon.mu.Lock()
			defer peer.dungeon.mu.Unlock()
			old := peer.dungeon.runtime
			if old == nil || old.raidEntry == nil || old.raidEntry.InstanceID != runtime.raidEntry.InstanceID || old.raidEntry.RunID != runtime.raidEntry.RunID || !s.currentRaidCompletedMapExitReady(peer, old) {
				return fmt.Errorf("raid retry completed return owner %d changed", id)
			}
			peer.townMu.Lock()
			gates := captureCurrentRaidRetrySceneGates(peer)
			peer.townMu.Unlock()
			owners = append(owners, currentRaidRetryReturnOwner{session: peer, runtime: old, character: id, generation: peer.characterGeneration, gates: gates})
			return nil
		}
		var err error
		if peer == leader {
			err = capture()
		} else {
			err = s.callGameSession(ctx, peer, "raid-retry-save-completed-return-owner", capture)
		}
		if err != nil {
			return nil, err
		}
	}
	return owners, nil
}
func (s *Service) restoreCurrentRaidRetryReturnOwners(leader *gameSession, owners []currentRaidRetryReturnOwner) {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	for _, owner := range owners {
		if !owner.retired || owner.runtime.townReturnPending {
			continue
		}
		restore := func() error {
			peer := owner.session
			if peer.selectedCharacterID != owner.character || peer.characterGeneration != owner.generation || !s.currentRaidAuthenticated(peer) {
				return nil
			}
			peer.dungeon.mu.Lock()
			if peer.dungeon.runtime != nil {
				peer.dungeon.mu.Unlock()
				return nil
			}
			peer.dungeon.runtime = owner.runtime
			if !s.currentRaidCompletedMapExitReady(peer, owner.runtime) {
				peer.dungeon.runtime = nil
				peer.dungeon.mu.Unlock()
				return nil
			}
			if scene, ok := owner.runtime.Session.Scene(); ok {
				s.adoptPartyDungeonInstance(peer, owner.runtime, scene)
			}
			peer.townMu.Lock()
			owner.gates.restore(peer)
			peer.townMu.Unlock()
			peer.dungeon.mu.Unlock()
			if s.onlinePlayers != nil {
				s.onlinePlayers.LeaveAreaSession(owner.character, peer)
			}
			if err := s.switchCurrentPetGrowthClock(peer, currentPetGrowthClockDungeon, s.gameplayNow(), "raid_retry_return_owner_restored"); err != nil {
				s.logGameEvent(peer, "game-pet-growth-clock-start-deferred", "source", "raid_retry_return_owner_restored", "error", err)
			}
			s.logGameEvent(peer, "game-raid-retry-completed-return-owner-restored", "run_id", owner.runtime.raidEntry.RunID, "reason", "new_entry_not_committed")
			return nil
		}
		var err error
		if owner.session == leader {
			err = restore()
		} else {
			err = s.callGameSession(ctx, owner.session, "raid-retry-restore-completed-return-owner", restore)
		}
		if err != nil {
			s.logWarn("raid retry restore return owner deferred", "error", err)
		}
	}
}

func currentRaidRetryOwnerForSession(owners []currentRaidRetryReturnOwner, session *gameSession) *currentRaidRetryReturnOwner {
	for i := range owners {
		if owners[i].session == session {
			return &owners[i]
		}
	}
	return nil
}
