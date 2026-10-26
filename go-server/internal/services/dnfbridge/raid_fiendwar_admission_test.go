package dnfbridge

import (
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
)

func TestCurrentFiendWarEncounterSourceUsesPendingOriginOnly(t *testing.T) {
	leader := &gameSession{selectedCharacterID: 19, townSceneReadyCharacterID: 19}
	leader.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 98, AreaID: 12, PositionValid: true}
	leader.fiendWarPendingMove = &currentFiendWarMovePlan{instance: "battle", character: 19, from: 4, to: 12}
	source, ready, atTarget, fromPending, ok := currentFiendWarEncounterSourceArea(leader, 12, 98, "battle")
	if !ok || !ready || !atTarget || !fromPending || source != 4 {
		t.Fatalf("pending mover source=%d ready=%v at_target=%v pending=%v ok=%v", source, ready, atTarget, fromPending, ok)
	}

	peer := &gameSession{selectedCharacterID: 20, townSceneReadyCharacterID: 20}
	peer.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 20, TownID: 98, AreaID: 4, PositionValid: true}
	source, ready, atTarget, fromPending, ok = currentFiendWarEncounterSourceArea(peer, 12, 98, "battle")
	if !ok || !ready || atTarget || fromPending || source != 4 {
		t.Fatalf("origin peer must remain selectable: source=%d ready=%v at_target=%v pending=%v ok=%v", source, ready, atTarget, fromPending, ok)
	}

	leader.fiendWarPendingMove.instance = "other"
	source, _, _, fromPending, ok = currentFiendWarEncounterSourceArea(leader, 12, 98, "battle")
	if !ok || fromPending || source != 12 {
		t.Fatalf("stale pending move changed source=%d pending=%v ok=%v", source, fromPending, ok)
	}
}

func TestCurrentFiendWarAdmissionClaimTokenIsIdentityBound(t *testing.T) {
	claim := currentFiendWarAdmissionClaim{
		InstanceID: "battle", SourceArea: 4, TargetArea: 12, Participants: []uint16{19, 20},
		Leader: 19, LeaderGeneration: 7, TakenAt: time.Unix(1, 0),
	}
	same := claim
	same.Participants = append([]uint16(nil), claim.Participants...)
	if !claim.equal(same) {
		t.Fatal("identical claim token did not match")
	}
	same.LeaderGeneration++
	if claim.equal(same) {
		t.Fatal("generation change must not release another claim")
	}
	same = claim
	same.Participants = []uint16{19, 21}
	if claim.equal(same) {
		t.Fatal("participant replacement must not release another claim")
	}
}

func TestRealPVFFiendWarAdmissionClaimSerializesWholeBattle(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 4)
	f.state.Started = true
	b := f.state.Battle
	b.EnterAfter = time.Now().Add(-time.Minute)
	b.Deadline = time.Now().Add(time.Hour)
	for _, name := range []string{"top command center state", "mid command center state", "bot command center state"} {
		b.FiendWar.Symbols[b.FiendWar.Config.Symbols[name]] = 0
	}
	if err := b.FiendWar.Apply(fiendwar.FiendWarEvent{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	area := 30
	if !b.FiendWar.AreaOpen(area) || b.FiendWar.Areas[area].Group != 0 {
		t.Fatal("real FiendWar boss area unavailable")
	}
	members := make([]currentFiendWarAdmissionMember, 0, len(peers))
	for _, peer := range peers {
		b.FiendWar.MemberAreas[peer.selectedCharacterID] = area
		members = append(members, currentFiendWarAdmissionMember{
			CharacterID: peer.selectedCharacterID,
			Generation:  peer.characterGeneration,
			Source:      area,
		})
	}
	claim, err := f.service.claimCurrentFiendWarEncounter(*f.state, peers[0], area, area, members[:2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.claimCurrentFiendWarEncounter(*f.state, peers[2], area, area, members[2:]); !errors.Is(err, errCurrentFiendWarAdmissionBusy) {
		t.Fatalf("nonoverlapping participant overwrote battle claim: %v", err)
	}
	plan := &currentFiendWarMovePlan{raid: f.state.RaidKey, instance: b.InstanceID, character: peers[0].selectedCharacterID, from: area, to: 31}
	if err = f.service.applyCurrentFiendWarMove(plan); !errors.Is(err, errCurrentFiendWarAdmissionBusy) {
		t.Fatal("claimed participant moved during entry", err)
	}
	peers[0].characterGeneration++
	f.service.releaseCurrentFiendWarEncounterClaim(*f.state, claim)
	peers[0].characterGeneration--
	if b.FiendWarAdmissionClaim.active() {
		t.Fatal("reconnect leaked admission gate")
	}
	bad := append([]currentFiendWarAdmissionMember(nil), members...)
	bad[1].Generation++
	if _, err = f.service.claimCurrentFiendWarEncounter(*f.state, peers[1], area, area, bad); err == nil || errors.Is(err, errCurrentFiendWarAdmissionBusy) {
		t.Fatalf("stale generation was not rejected by domain claim: %v", err)
	}
	if _, err = f.service.claimCurrentFiendWarEncounter(*f.state, peers[1], area, area, members[1:2]); err != nil {
		t.Fatalf("released claim did not admit next owner: %v", err)
	}
	f.service.releaseCurrentFiendWarEncounterClaim(*f.state, claim)
	if !b.FiendWarAdmissionClaim.active() {
		t.Fatal("old release deleted newer claim")
	}
}
