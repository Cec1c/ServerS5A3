package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func reviewLukePhaseTwoFixture(t *testing.T) (*raidStartGuardFixture, *gameSession) {
	t.Helper()
	f, session, at := normalLukeFixture(t)
	oldKey := f.state.RaidKey
	delete(f.service.raids, oldKey)
	f.state.RaidKey = uint32(f.service.channelAdvertiseID())<<24 | 32<<16 | oldKey&0xffff
	f.service.raids[f.state.RaidKey] = f.state
	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, session, id, at)
			at = at.Add(time.Second)
		}
	}
	b := f.state.Battle
	next, err := (&raid.LukeProgression{AntonProgression: b.Anton}).NextPhase(at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	b.Anton, b.Dungeons, b.Phase = next.AntonProgression, next.Dungeons(), 1
	b.PhaseCompleted = false
	b.EnterAfter, b.Deadline = time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	return f, session
}

func reviewLukeRuntimeFor(t *testing.T, session *gameSession, id uint32) *runtimeDungeonState {
	t.Helper()
	_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
	r.Dungeon.ID, r.AccountID = int64(id), session.accountID
	r.Character = repo.CharacterRecord{CharacterID: selectedCharacterIDStringForLukeTest(session), AccountID: session.accountID}
	return r
}

func selectedCharacterIDStringForLukeTest(s *gameSession) string {
	if s.selectedCharacterID == 19 {
		return "19"
	}
	return "20"
}

func TestRealPVFLukeAcceptedFollowersKeepRoutesThroughRejoinAndGroupMove(t *testing.T) {
	f, leader := reviewLukePhaseTwoFixture(t)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = leader.channel, true, true
	f.service.bindGameSessionCharacter(peer, 20)
	f.state.Members[1].GroupIndex = 1
	left, right := reviewLukeRuntimeFor(t, leader, 5311), reviewLukeRuntimeFor(t, peer, 5311)
	follower := &runtimePartyPreparedDungeonEntry{session: peer, runtime: right}
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, left, []*runtimePartyPreparedDungeonEntry{follower}); err != nil {
		t.Fatal(err)
	}
	if len(f.state.Battle.LukeMemberRoutes) != 0 {
		t.Fatal("preparation chose routes")
	}
	leader.dungeon.runtime, peer.dungeon.runtime = left, right
	at := time.Now()
	if _, _, err := f.service.reduceCurrentLukeSceneLocked(leader, left, "enter", at); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.state.Battle.LukeMemberRoutes[20]; ok {
		t.Fatal("leader fabricated follower accepted entry")
	}
	if _, _, err := f.service.reduceCurrentLukeSceneLocked(peer, right, "enter", at); err != nil {
		t.Fatal(err)
	}
	if len(f.state.Battle.LukeMemberRoutes) != 2 || f.state.Battle.LukeMemberRoutes[19].Route != 4 || f.state.Battle.LukeMemberRoutes[20].Route != 4 {
		t.Fatal("accepted follower lost route")
	}
	before := cloneRuntimeRaidBattle(f.state.Battle)
	replacement := f.session()
	replacement.selectedCharacterID, replacement.accountID = 20, "account-20"
	replacement.channel, replacement.loginPacketSeen, replacement.accountFromToken = peer.channel, true, true
	f.service.bindGameSessionCharacter(replacement, 20)
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, f.state.RaidKey)
	f.conn.write.Reset()
	if err := f.service.handleOnlineRejoinRaid(replacement, uint16(668), body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if !reflect.DeepEqual(ack.Body, []byte{1}) {
		t.Fatal("valid Luke rejoin rejected", ack.Body)
	}
	f.state.Members[1].GroupIndex = 2
	if !reflect.DeepEqual(before.LukeMemberRoutes, f.state.Battle.LukeMemberRoutes) {
		t.Fatal("rejoin/group move erased routes")
	}
	if f.service.currentRaidDungeonSelectionAllowed(replacement, 5313) {
		t.Fatal("rejoined follower selected dark entrance")
	}
	for _, id := range []uint32{5313, 5058, 5060, 5065, 5062} {
		if f.service.currentLukeRouteAllowedLocked(f.state.Battle, 20, id) {
			t.Fatalf("dark route %d allowed", id)
		}
	}
	for _, id := range []uint32{5311, 5057, 5059, 5064, 5061, 5063, 5066} {
		if !f.service.currentLukeRouteAllowedLocked(f.state.Battle, 20, id) {
			t.Fatalf("owned/shared route %d rejected", id)
		}
	}
	candidate := reviewLukeRuntimeFor(t, replacement, 5313)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(replacement, candidate, nil); err == nil || candidate.raidEntry != nil {
		t.Fatal("opposite route prepared/committed")
	}
	f.state.Battle.LukeMemberRoutes[20] = currentLukeMemberRoute{Attempt: f.state.Battle.Anton.Epoch() + 1, Route: 4}
	if !f.service.currentLukeRouteAllowedLocked(f.state.Battle, 20, 5313) {
		t.Fatal("different attempt inherited old route")
	}
	if before.LukeMemberRoutes[20].Route != 4 || before.LukeMemberRoutes[20].Attempt != before.Anton.Epoch() {
		t.Fatal("battle snapshot aliases route map")
	}
}

func TestRealPVFLukeCancelledPreparationAndSingleModeDoNotChooseRoutes(t *testing.T) {
	f, session := reviewLukePhaseTwoFixture(t)
	r := reviewLukeRuntimeFor(t, session, 5311)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
		t.Fatal(err)
	}
	f.service.cancelUncommittedLukeEntry(r)
	if len(f.state.Battle.LukeMemberRoutes) != 0 {
		t.Fatal("cancelled preparation left route")
	}
	for _, b := range []*runtimeRaidBattle{nil, {Mode: 1, Phase: 1}, {Mode: 2, Single: true, Phase: 1}, {Mode: 2, Phase: 0}} {
		if err := (&Service{}).commitCurrentLukeRouteLocked(b, 19, 5311); err != nil {
			t.Fatal("out-of-scope route commit read PVF", err)
		}
		if b != nil && len(b.LukeMemberRoutes) != 0 {
			t.Fatal("single/foreign phase recorded route")
		}
	}
}
