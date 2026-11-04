package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func (p *gameSession) stablePartyRecoveryScene() (uint64, bool) {
	if p == nil {
		return 0, false
	}
	p.finishLoadingMu.Lock()
	defer p.finishLoadingMu.Unlock()
	return p.finishLoadingRevision, p.currentFinishLoadingStateSent && p.currentFinishLoadingCompletionSent && p.postFinishLoadingPlayerStateSent
}

// Capture a stable, active ordinary dungeon before the relay confirmation
// window. The revision also rejects leaving and re-entering the same room:
// an equal map/run key alone does not prove an uninterrupted scene lifetime.
func (s *Service) prepareStalledPartyMemberRecovery(partyID uint16, binding currentPartyUDPRelayBinding) *partyUDPRelayRecoveryPlan {
	if s == nil || !s.partyUDPRelaySilentPeerKickMode() || s.options.currentClientVersion != "official-24.9" || partyID == 0 {
		return nil
	}
	identity, online := s.onlineGameSessionCharacterSnapshot(binding.characterID)
	if !online || identity.session == nil || identity.generation != binding.generation {
		return nil
	}
	p := identity.session
	snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(identity.character, identity.generation)
	if !found || snapshot.ID != partyID || len(snapshot.Members) < 2 {
		return nil
	}
	run, ready := s.currentDungeonPartyRunKey(p)
	if !ready || run.PartyID != partyID {
		return nil
	}
	state := snapshot.StateFor(identity.character)
	membership := s.currentPartyRetirementGuard(p, &state)
	var guards []func() bool
	var targetRuntime *runtimeDungeonState
	var targetVisit uint64
	var instance *dungeonPartyInstance
	for _, member := range snapshot.Members {
		peer, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok || peer.session == nil || peer.generation != member.SessionGeneration {
			return nil
		}
		q := peer.session
		revision, stable := q.stablePartyRecoveryScene()
		peerRun, peerReady := s.currentDungeonPartyRunKey(q)
		if !stable || !peerReady || peerRun != run {
			return nil
		}
		q.dungeon.mu.Lock()
		runtime := q.dungeon.runtime
		valid := runtime != nil && runtime.Session != nil && runtime.partyInstance != nil &&
			runtime.raidEntry == nil && !runtime.townReturnPending && runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunActive
		if valid {
			if instance == nil {
				instance = runtime.partyInstance
			}
			valid = runtime.partyInstance == instance
			if q == p {
				targetRuntime, targetVisit = runtime, runtime.loadingRoomVisit
			}
		}
		q.dungeon.mu.Unlock()
		if !valid {
			return nil
		}
		guards = append(guards, func() bool {
			version, stillStable := q.stablePartyRecoveryScene()
			key, present := s.currentDungeonPartyRunKey(q)
			return s.boundGameSessionCharacterCurrent(peer) && stillStable && version == revision && present && key == run &&
				runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunActive
		})
	}
	current := func() bool {
		if !membership() {
			return false
		}
		for _, guard := range guards {
			if !guard() {
				return false
			}
		}
		return true
	}
	if targetRuntime == nil || !current() {
		return nil
	}
	return &partyUDPRelayRecoveryPlan{
		current: current,
		deliver: func(stillStalled func() bool) bool {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			sent := false
			err := s.callGameSession(ctx, p, "party-stalled-peer-recovery", func() error {
				if !current() || !stillStalled() {
					return nil
				}
				p.dungeon.mu.Lock()
				valid := p.dungeon.runtime == targetRuntime && targetRuntime.loadingRoomVisit == targetVisit && !targetRuntime.townReturnPending
				p.dungeon.mu.Unlock()
				if !valid {
					return nil
				}
				body := s.buildRuntimePartyPeerEndpointInfoForReceiver(p, state)
				guard := func() bool { return current() && stillStalled() }
				var writeErr error
				sent, writeErr = s.sendRetiredPartyPacket(p, 0x000b, body, &state, guard, false)
				return writeErr
			})
			s.logGameEvent(p, "game-party-stalled-peer-endpoints-requested", "party_id", partyID,
				"char_id", identity.character, "generation", identity.generation, "sent", sent, "error", err,
				"scope", "one_member_endpoint_table_only_no_roster_or_scene_reset")
			return sent && err == nil
		},
	}
}
