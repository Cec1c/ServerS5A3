package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The selected actor's mailbox serializes movement completion and encounter entry.
func (s *Service) queueCurrentFiendWarEncounter(p *gameSession) {
	s.queueCurrentFiendWarEncounterFrom(p, currentFiendWarAdmissionPhaseQueue)
}

// queueCurrentFiendWarEncounterFrom is the explicit phase-aware queue entry.
// Callers that know the triggering town packet should pass the matching
// currentFiendWarAdmissionPhase value. The wrapper above keeps older call sites
// compiling and labels their diagnostics as an unlabelled queue attempt.
func (s *Service) queueCurrentFiendWarEncounterFrom(p *gameSession, phase string) {
	if !isCurrentFiendWarChannel(p) {
		return
	}
	id, generation := p.selectedCharacterID, p.characterGeneration
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		err := s.callGameSession(ctx, p, "超时空区域遭遇战", func() error {
			if p.selectedCharacterID != id || p.characterGeneration != generation {
				return nil
			}
			var diagnostic *currentFiendWarAdmissionDiagnostic
			var attempt currentFiendWarAdmissionDiagnostic
			if queued, ok := currentFiendWarQueuedAdmissionDiagnostic(p, phase); ok {
				attempt = queued
				diagnostic = &attempt
			}
			err := s.tryCurrentFiendWarEncounterWithDiagnostic(p, diagnostic)
			if diagnostic != nil {
				s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, attempt)
			}
			return err
		})
		if err != nil {
			s.logGameEvent(p, "超时空区域遭遇战延后", "error", err)
		}
	}()
}

func (s *Service) tryCurrentFiendWarEncounter(p *gameSession) error {
	return s.tryCurrentFiendWarEncounterWithDiagnostic(p, nil)
}

func (s *Service) tryCurrentFiendWarEncounterWithDiagnostic(p *gameSession, diagnostic *currentFiendWarAdmissionDiagnostic) error {
	if !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) {
		diagnostic.setReason("entry_scope_not_authenticated")
		return nil
	}
	p.dungeon.mu.Lock()
	busy := p.dungeon.runtime != nil
	p.dungeon.mu.Unlock()
	if busy {
		diagnostic.setReason("session_runtime_present")
		return nil
	}
	state, ok := s.runtimeRaidForMember(p.selectedCharacterID)
	if !ok {
		diagnostic.setReason("raid_member_missing")
		return nil
	}
	diagnostic.captureRaid(state)
	if state.Battle == nil || state.Battle.FiendWar == nil {
		diagnostic.setReason("fiendwar_battle_missing")
		return nil
	}
	if !state.Started {
		diagnostic.setReason("raid_not_started")
		return nil
	}
	if state.Battle.TimedOut {
		diagnostic.setReason("raid_timed_out")
		return nil
	}
	if state.Battle.PhaseCompleted {
		diagnostic.setReason("raid_phase_completed")
		return nil
	}
	if time.Now().Before(state.Battle.EnterAfter) {
		diagnostic.setReason("entry_delay")
		return nil
	}
	p.townMu.Lock()
	pendingMove := p.fiendWarPendingMove
	p.townMu.Unlock()
	b := state.Battle
	f := b.FiendWar
	area := f.MemberArea(p.selectedCharacterID, b.MemberGroups[p.selectedCharacterID])
	a := f.Config.Areas[area]
	if !f.AreaOpen(area) {
		diagnostic.setReason("target_area_not_open")
		return nil
	}
	if f.Areas[area].Group != 0 {
		diagnostic.setReason("target_area_occupied")
		return nil
	}
	source, ready, atTarget, fromPending, ok, sourceReason := currentFiendWarEncounterSourceAreaWithReason(p, area, f.Config.WaitingTown, b.InstanceID)
	if !ok || !ready || !atTarget {
		diagnostic.setReason(sourceReason)
		return nil
	}
	// Freeze only the confirmed pre-combat map. pending.from is the origin;
	// destination members are never unioned into this roster. Peers still at
	// that origin are included even though the leader already reports target.
	var self runtimeRaidMemberState
	for _, m := range state.Members {
		if m.CharID == p.selectedCharacterID {
			self = m
			break
		}
	}
	if self.CharID == 0 {
		diagnostic.setReason("self_raid_member_missing")
		return nil
	}
	roster := []runtimeRaidMemberState{self}
	admission := []currentFiendWarAdmissionMember{{CharacterID: self.CharID, Generation: p.characterGeneration, Source: source, TargetMove: fromPending}}
	for _, m := range state.Members {
		if m.CharID == p.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(m.CharID)
		if !online || !s.currentRaidAuthenticated(peer) || peer.channel.ID != p.channel.ID || b.MemberAccounts[m.CharID] != s.accountIDForSession(peer) {
			continue // 断线保留的攻坚席位不占这次进图名单。
		}
		peerSource, peerReady, _, peerTargetMove, peerOK := currentFiendWarEncounterSourceArea(peer, area, f.Config.WaitingTown, b.InstanceID)
		if !peerOK || peerSource != source {
			continue
		}
		if !peerReady {
			diagnostic.setReason("peer_scene_not_ready")
			return nil
		}
		peer.dungeon.mu.Lock()
		peerBusy := peer.dungeon.runtime != nil
		peer.dungeon.mu.Unlock()
		if peerBusy {
			diagnostic.setReason("peer_runtime_present")
			return nil
		}
		if len(roster) >= a.MemberMax {
			diagnostic.setReason("roster_capacity")
			return nil
		}
		roster = append(roster, m)
		admission = append(admission, currentFiendWarAdmissionMember{CharacterID: m.CharID, Generation: peer.characterGeneration, Source: peerSource, TargetMove: peerTargetMove})
	}
	if len(roster) == 0 || len(roster) > a.MemberMax {
		diagnostic.setReason("roster_invalid")
		return nil
	}
	for _, m := range roster {
		peer, found := s.onlineGameSession(m.CharID)
		if !found {
			diagnostic.setReason("roster_peer_offline")
			return nil
		}
		if _, bound := bindCurrentTownSelectorOrigin(peer); !bound {
			diagnostic.setReason("selector_origin_unbindable")
			return nil
		}
	}
	body, err := s.currentFiendWarEncounterSelectBody(uint32(a.Dungeon))
	if err != nil {
		diagnostic.setReason("dungeon_difficulty_unavailable")
		return err
	}
	// One battle-wide claim precedes every party replacement. Same-area and
	// overlapping callers cannot replace this winner before reservation.
	claim, err := s.claimCurrentFiendWarEncounter(state, p, source, area, admission)
	if err != nil {
		if errors.Is(err, errCurrentFiendWarAdmissionBusy) {
			diagnostic.setReason("claim_busy")
			return nil
		}
		diagnostic.setReason("claim_rejected")
		return err
	}
	transferred := false
	release := func() { s.releaseCurrentFiendWarEncounterClaim(state, claim) }
	defer func() {
		if !transferred {
			release()
		}
	}()
	previous, err := s.moveCurrentFiendWarEncounterRoster(state, roster, area)
	if err != nil {
		diagnostic.setReason("roster_move_failed")
		return err
	}
	rollback := func() { s.rollbackCurrentFiendWarEncounterRoster(state, previous, area) }
	defer func() {
		if !transferred {
			rollback()
		}
	}()
	managed, ok := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(state, roster))
	if !ok {
		diagnostic.setReason("party_create_failed")
		return fmt.Errorf("超时空遭遇战队伍创建失败")
	}
	temporary, found := s.runtimePartyManagerForService().SnapshotByID(uint16(managed.PartyID))
	if !found {
		diagnostic.setReason("temporary_party_missing")
		return fmt.Errorf("超时空遭遇战临时队伍已更换")
	}
	entered := false
	retire := func() {
		if !entered {
			s.retireCurrentFiendWarPartySnapshot(p, temporary, "entry_not_committed")
		}
	}
	defer func() {
		if !transferred {
			retire()
		}
	}()
	if err := s.prepareCurrentFiendWarFormationActors(p, temporary, int64(a.Dungeon)); err != nil {
		diagnostic.setReason("party_actor_prepare_failed")
		return err
	}
	if err := s.sendManagedRuntimePartySnapshots(managed); err != nil {
		diagnostic.setReason("party_snapshot_send_failed")
		return err
	}
	enter := func(proof *partyNativeConnectionProof, d *currentFiendWarAdmissionDiagnostic) error {
		err := s.handleDungeonSelectUpperPlannedWithNativeAdmission(p, body, nil, true, nil, true, proof)
		p.dungeon.mu.Lock()
		entered = p.dungeon.runtime != nil && p.dungeon.runtime.raidEntry != nil && p.dungeon.runtime.raidEntry.InstanceID == b.InstanceID && p.dungeon.runtime.Dungeon.ID == int64(a.Dungeon)
		p.dungeon.mu.Unlock()
		if entered {
			if err != nil {
				d.setReason("entry_committed_with_error")
			} else {
				d.setReason("entry_committed")
			}
			p.townMu.Lock()
			if p.fiendWarPendingMove == pendingMove {
				p.fiendWarPendingMove = nil
			}
			p.townMu.Unlock()
		} else if err != nil {
			d.setReason("dungeon_entry_error")
		} else {
			d.setReason("entry_not_committed")
		}
		return err
	}

	// A member continuing from a cleared encounter must have working links to
	// this newly assembled roster before another fight can be charged.
	afterClear := false
	for _, member := range roster {
		if b.MemberClears[member.CharID] > 0 {
			afterClear = true
			break
		}
	}
	if !afterClear || len(roster) < 2 || !s.nativePartyConnectionAdmissionEnabled() {
		return enter(nil, diagnostic)
	}
	proof, err := s.newPartyNativeConnectionProof(p, managed, body)
	if err != nil {
		diagnostic.setReason("connection_query_unavailable")
		return err
	}
	result := newCurrentFiendWarAdmissionDiagnostic(p, uint32(a.Dungeon))
	result.captureRaid(state)
	if diagnostic != nil {
		result = *diagnostic
	}
	proof.resume = func() error {
		err := enter(proof, &result)
		s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, result)
		return err
	}
	proof.refuse = func(cause error) error {
		s.logInfo("fiendwar encounter connection refused", "raid_key", state.RaidKey, "dungeon_id", a.Dungeon,
			"char_id", claim.Leader, "error", cause)
		return nil
	}
	// Keep the same roster until the asynchronous reply and its cleanup finish,
	// including a timeout, leader disconnect or rejected session event.
	transferred, err = s.startPartyNativeConnectionQuery(proof, nil, true, func() {
		retire()
		rollback()
		release()
	})
	if err != nil {
		diagnostic.setReason("connection_query_unavailable")
		return err
	}
	diagnostic.setReason("connection_query_pending")
	return nil
}
