package dnfbridge

import (
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"time"
)

var errCurrentFiendWarAdmissionBusy = errors.New("fiendwar encounter admission already claimed")

type currentFiendWarAdmissionMember struct {
	CharacterID uint16
	Generation  uint64
	Source      int
	TargetMove  bool
}

// One battle-wide claim covers the interval from party replacement through
// dungeon reservation. Its value is the release token.
type currentFiendWarAdmissionClaim struct {
	InstanceID       string
	SourceArea       int
	TargetArea       int
	Participants     []uint16
	Members          []currentFiendWarAdmissionMember
	Leader           uint16
	LeaderGeneration uint64
	TakenAt          time.Time
}

func (c currentFiendWarAdmissionClaim) active() bool { return c.InstanceID != "" }

func (c currentFiendWarAdmissionClaim) contains(id uint16) bool {
	if !c.active() {
		return false
	}
	for _, member := range c.Participants {
		if member == id {
			return true
		}
	}
	return false
}

func (c currentFiendWarAdmissionClaim) equal(other currentFiendWarAdmissionClaim) bool {
	return c.InstanceID == other.InstanceID && c.SourceArea == other.SourceArea && c.TargetArea == other.TargetArea &&
		c.Leader == other.Leader && c.LeaderGeneration == other.LeaderGeneration && c.TakenAt.Equal(other.TakenAt) &&
		sameRaidParticipants(c.Participants, other.Participants)
}

// pending.from is the only origin override. A peer still standing at that
// origin is included; the destination is never unioned into the roster.
func currentFiendWarEncounterSourceArea(p *gameSession, target, waitingTown int, instance string) (source int, ready, atTarget, fromPending, ok bool) {
	source, ready, atTarget, fromPending, ok, _ = currentFiendWarEncounterSourceAreaWithReason(p, target, waitingTown, instance)
	return source, ready, atTarget, fromPending, ok
}

func currentFiendWarEncounterSourceAreaWithReason(p *gameSession, target, waitingTown int, instance string) (source int, ready, atTarget, fromPending, ok bool, reason string) {
	if p == nil || p.selectedCharacterID == 0 || waitingTown <= 0 {
		return 0, false, false, false, false, "source_context_missing"
	}
	p.townMu.Lock()
	position := p.townPositionSnapshot
	pending := p.fiendWarPendingMove
	ready = p.townSceneReadyCharacterID == p.selectedCharacterID
	p.townMu.Unlock()
	if position.CharacterID != p.selectedCharacterID || !position.PositionValid || int(position.TownID) != waitingTown {
		if position.CharacterID != p.selectedCharacterID {
			return 0, ready, false, false, false, "source_position_owner_mismatch"
		}
		if !position.PositionValid {
			return 0, ready, false, false, false, "source_position_invalid"
		}
		return 0, ready, false, false, false, "source_town_mismatch"
	}
	source = int(position.AreaID)
	atTarget = source == target
	if pending != nil && pending.instance == instance && pending.character == p.selectedCharacterID &&
		pending.to == target && pending.from > 0 {
		source = pending.from
		atTarget = int(position.AreaID) == target
		fromPending = true
	}
	if !ready {
		return source, false, atTarget, fromPending, true, "source_scene_not_ready"
	}
	if !atTarget {
		return source, true, false, fromPending, true, "source_not_target"
	}
	return source, ready, atTarget, fromPending, true, "source_ready"
}

// Source and generation are snapshots taken before raidMu. No session or town
// lock is acquired here. Domain state must still be source, or target only for
// a participant that has the exact captured source-to-target pending move.
func (s *Service) claimCurrentFiendWarEncounter(captured runtimeRaidState, p *gameSession, source, target int, members []currentFiendWarAdmissionMember) (currentFiendWarAdmissionClaim, error) {
	if s == nil || p == nil || captured.Battle == nil || len(members) == 0 {
		return currentFiendWarAdmissionClaim{}, fmt.Errorf("fiendwar admission context unavailable")
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[captured.RaidKey]
	if state == nil || state.Battle == nil || state.Battle.InstanceID != captured.Battle.InstanceID || state.Battle.FiendWar == nil ||
		!state.Started || state.Battle.TimedOut || state.Battle.PhaseCompleted || time.Now().Before(state.Battle.EnterAfter) {
		return currentFiendWarAdmissionClaim{}, fmt.Errorf("fiendwar admission battle changed")
	}
	b := state.Battle
	f := b.FiendWar
	area, targetFound := f.Config.Areas[target]
	if _, sourceFound := f.Config.Areas[source]; !sourceFound || !targetFound || area.Dungeon <= 0 || !f.AreaOpen(target) || f.Areas[target].Group != 0 {
		return currentFiendWarAdmissionClaim{}, fmt.Errorf("fiendwar admission area unavailable")
	}
	if b.FiendWarAdmissionClaim.active() {
		return currentFiendWarAdmissionClaim{}, errCurrentFiendWarAdmissionBusy
	}
	if len(members) > area.MemberMax {
		return currentFiendWarAdmissionClaim{}, fmt.Errorf("fiendwar admission exceeds PVF member max")
	}
	ids := make([]uint16, 0, len(members))
	seen := make(map[uint16]bool, len(members))
	leaderGeneration := uint64(0)
	for _, member := range members {
		id := member.CharacterID
		if id == 0 || seen[id] || member.Source != source || member.Generation == 0 {
			return currentFiendWarAdmissionClaim{}, fmt.Errorf("fiendwar admission participant duplicate or source changed")
		}
		seen[id] = true
		ids = append(ids, id)
		index := runtimeRaidMemberIndex(*state, id)
		peer, online := s.onlineGameSession(id)
		currentArea := f.MemberArea(id, b.MemberGroups[id])
		if index < 0 || b.MemberGroups[id] == 0 || b.MemberAccounts[id] == "" || !online || peer.characterGeneration != member.Generation ||
			peer.channel.ID != p.channel.ID || b.MemberAccounts[id] != s.accountIDForSession(peer) ||
			(currentArea != source && !(currentArea == target && member.TargetMove)) {
			return currentFiendWarAdmissionClaim{}, fmt.Errorf("fiendwar admission participant owner or area changed")
		}
		if id == p.selectedCharacterID {
			leaderGeneration = member.Generation
		}
	}
	if leaderGeneration == 0 {
		return currentFiendWarAdmissionClaim{}, fmt.Errorf("fiendwar admission leader missing")
	}
	claim := currentFiendWarAdmissionClaim{
		InstanceID:       b.InstanceID,
		SourceArea:       source,
		TargetArea:       target,
		Participants:     append([]uint16(nil), ids...),
		Members:          append([]currentFiendWarAdmissionMember(nil), members...),
		Leader:           p.selectedCharacterID,
		LeaderGeneration: leaderGeneration,
		TakenAt:          time.Now(),
	}
	b.FiendWarAdmissionClaim = claim
	return claim, nil
}

// The leader has crossed the door while a selected peer can still stand in the
// source map. Replace only the ordinary same-area predicate, using this exact
// admission token; do not manufacture town presence or bypass peer preparation.
func (s *Service) currentFiendWarAdmissionAllowsPeer(leader *gameSession, peerID uint16, partyID int, dungeonID int64) bool {
	if !s.currentFiendWarAutomaticPartyOnly(leader) {
		return false
	}
	lead, leadOK := s.boundGameSessionCharacterSnapshot(leader)
	peer, peerOK := s.onlineGameSessionCharacterSnapshot(peerID)
	if !leadOK || !peerOK || !s.currentFiendWarAutomaticPartyOnly(peer.session) || peer.session.channel.ID != leader.channel.ID {
		return false
	}
	state, found := s.runtimeRaidForMember(lead.character)
	if !found || state.Battle == nil || state.Battle.FiendWar == nil {
		return false
	}
	b := state.Battle
	c := b.FiendWarAdmissionClaim
	if !c.active() || c.InstanceID != b.InstanceID || c.Leader != lead.character || c.LeaderGeneration != lead.generation || !c.contains(peerID) || int64(b.FiendWar.Config.Areas[c.TargetArea].Dungeon) != dungeonID {
		return false
	}
	matched := false
	for _, member := range c.Members {
		matched = matched || member.CharacterID == peerID && member.Generation == peer.generation
	}
	if !matched {
		return false
	}
	for _, identity := range []boundGameSessionCharacter{lead, peer} {
		source, ready, _, _, valid := currentFiendWarEncounterSourceArea(identity.session, c.TargetArea, b.FiendWar.Config.WaitingTown, c.InstanceID)
		if !valid || !ready || source != c.SourceArea || b.MemberAccounts[identity.character] != s.accountIDForSession(identity.session) {
			return false
		}
	}
	party, exists := s.runtimePartyManagerForService().SnapshotByUser(lead.character, lead.generation)
	if !exists || int(party.ID) != partyID || party.Leader != lead.character || len(party.Members) != len(c.Participants) {
		return false
	}
	for _, member := range party.Members {
		matched := false
		for _, admitted := range c.Members {
			matched = matched || admitted.CharacterID == member.UserID && admitted.Generation == member.SessionGeneration
		}
		if !matched {
			return false
		}
	}
	if !s.boundGameSessionCharacterCurrent(lead) || !s.boundGameSessionCharacterCurrent(peer) {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	live := s.raids[state.RaidKey]
	return live != nil && live.Battle != nil && !live.Battle.TimedOut && !live.Battle.PhaseCompleted &&
		live.Battle.FiendWar != nil && live.Battle.FiendWarAdmissionClaim.equal(c) &&
		live.Battle.FiendWar.MemberArea(peerID, live.Battle.MemberGroups[peerID]) == c.TargetArea &&
		live.Battle.FiendWar.MemberArea(lead.character, live.Battle.MemberGroups[lead.character]) == c.TargetArea
}

func (s *Service) releaseCurrentFiendWarEncounterClaim(captured runtimeRaidState, claim currentFiendWarAdmissionClaim) {
	if s == nil || captured.Battle == nil || !claim.active() {
		return
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[captured.RaidKey]
	if state == nil || state.Battle == nil || state.Battle.InstanceID != captured.Battle.InstanceID {
		return
	}
	if state.Battle.FiendWarAdmissionClaim.equal(claim) {
		state.Battle.FiendWarAdmissionClaim = currentFiendWarAdmissionClaim{}
	}
}

// A normal official FiendWar selection must use the co-area roster. It never
// falls back to the managed town party or a one-member runtime.
func (s *Service) interceptCurrentFiendWarNormalSelection(session *gameSession, dungeonID uint32) (bool, error) {
	if s == nil || session == nil || !isCurrentFiendWarChannel(session) {
		return false, nil
	}
	diagnostic := newCurrentFiendWarAdmissionDiagnostic(session, dungeonID)
	defer func() { s.logCurrentFiendWarAdmissionDiagnostic(diagnostic) }()
	return s.interceptCurrentFiendWarNormalSelectionWithDiagnostic(session, dungeonID, &diagnostic)
}

// interceptCurrentFiendWarNormalSelectionWithDiagnostic shares the per-request
// context created by the real op16 path. A nil argument is accepted by the
// internal helpers and disables diagnostics without changing admission.
func (s *Service) interceptCurrentFiendWarNormalSelectionWithDiagnostic(session *gameSession, dungeonID uint32, diagnostic *currentFiendWarAdmissionDiagnostic) (bool, error) {
	if s == nil || session == nil || !isCurrentFiendWarChannel(session) {
		return false, nil
	}
	if s.options.currentClientVersion != "official-24.9" {
		diagnostic.setReason("client_version_not_official")
		return false, nil
	}
	state, ok := s.runtimeRaidForMember(session.selectedCharacterID)
	if !ok {
		diagnostic.setReason("raid_member_missing")
		return false, nil
	}
	diagnostic.captureRaid(state)
	if state.Battle == nil || state.Battle.FiendWar == nil {
		diagnostic.setReason("fiendwar_battle_missing")
		return false, nil
	}
	if !state.Started {
		diagnostic.setReason("raid_not_started")
		return false, nil
	}
	if state.Battle.TimedOut {
		diagnostic.setReason("raid_timed_out")
		return false, nil
	}
	if state.Battle.PhaseCompleted {
		diagnostic.setReason("raid_phase_completed")
		return false, nil
	}
	b := state.Battle
	area := b.FiendWar.MemberArea(session.selectedCharacterID, b.MemberGroups[session.selectedCharacterID])
	a, found := b.FiendWar.Config.Areas[area]
	if !found || a.Dungeon <= 0 {
		diagnostic.setReason("member_area_not_selectable")
		return false, nil
	}
	selectDungeon := uint16(dnfenum.GameTypeSelectDungeon)
	if uint32(a.Dungeon) != dungeonID {
		diagnostic.setReason("target_dungeon_mismatch")
		return true, s.sendGameUpperRawClass(session, selectDungeon, []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0}, 1)
	}
	if err := s.tryCurrentFiendWarEncounterWithDiagnostic(session, diagnostic); err != nil {
		if diagnostic == nil || diagnostic.reason == "" {
			diagnostic.setReason("try_error")
		}
		s.logGameEvent(session, "game-fiendwar-normal-selection-admission-failed", "dungeon_id", dungeonID, "error", err)
		return true, s.sendGameUpperRawClass(session, selectDungeon, []byte{0, currentSelectDungeonUnavailableReason, 0}, 1)
	}
	session.dungeon.mu.Lock()
	entered := session.dungeon.runtime != nil && session.dungeon.runtime.raidEntry != nil && session.dungeon.runtime.raidEntry.InstanceID == b.InstanceID
	session.dungeon.mu.Unlock()
	if entered {
		if diagnostic == nil || diagnostic.reason == "" {
			diagnostic.setReason("entry_committed")
		}
		return true, nil
	}
	if diagnostic == nil || diagnostic.reason == "" {
		diagnostic.setReason("entry_not_committed")
	}
	return true, s.sendGameUpperRawClass(session, selectDungeon, []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0}, 1)
}
