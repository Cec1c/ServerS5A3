package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

func TestRaidProjectionReadBudgetAdmissionOncePerMember(t *testing.T) {
	f := newRaidEntryCostInventoryRefreshFixture(t)
	f.setMaterial(t, f.leader.selectedCharacterID, 1)
	loads := wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, nil)
	if err := f.service.broadcastRuntimeRaidEntryCostInventoryRefresh(f.state); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"19", "20"} {
		if loads[id] != 1 {
			t.Fatalf("one inventory notification reread owner %s %d times", id, loads[id])
		}
	}
	f.assertOneEntryCostPacket(t, f.leaderConn)
	f.assertOneEntryCostPacket(t, f.peerConn)
	f.setMaterial(t, f.leader.selectedCharacterID, 0)
	if err := f.service.broadcastRuntimeRaidEntryCostInventoryRefresh(f.state); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"19", "20"} {
		if loads[id] != 2 {
			t.Fatal("broadcast reused ownership across refreshes", loads)
		}
	}
	body := f.assertOneEntryCostPacket(t, f.leaderConn)
	assertEntryCostMemberStatus(t, body, 0, 19, 0, 0)
}

func TestRaidProjectionReadBudgetProfilesOncePerMember(t *testing.T) {
	s, a, b, _, _, state := prepareApprovedCurrentRaid(t)
	loads := wrapCurrentCrossSceneProfileBatchCharacterLoads(t, s, nil)
	batch := newCurrentRaidProfileBatch(s, context.Background())
	for _, peer := range []*gameSession{a, b} {
		if err := s.sendCurrentRuntimeRaidProfilesWithBatchForActors(peer, state, batch, map[uint16]bool{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range state.Members {
		id := strconv.Itoa(int(member.CharID))
		if loads[id] != 1 {
			t.Fatalf("compact profile owner %s loaded %d times", id, loads[id])
		}
	}
}

func TestRaidProjectionReadBudgetReboundRecipientGetsNoCostPacket(t *testing.T) {
	f := newRaidEntryCostInventoryRefreshFixture(t)
	wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, func(id string) {
		if id == "19" {
			f.service.unbindGameSession(f.leader)
		}
	})
	if err := f.service.broadcastRuntimeRaidEntryCostInventoryRefresh(f.state); err != nil {
		t.Fatal(err)
	}
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("stale recipient received inventory admission data")
	}
}

func TestRaidProjectionReadBudgetStartHonorsPreloadedCatalogFailure(t *testing.T) {
	f, peers := raidStartWaitingFixture(t, 0)
	sentinel := errors.New("preloaded immutable item catalog unavailable")
	f.service.pvfItemCatalogLoadErr = sentinel
	_, err := f.service.prepareCurrentRaidBattle(peers[0], cloneRuntimeRaidState(*f.state))
	if !errors.Is(err, sentinel) {
		t.Fatalf("start rebuilt item indexes instead of using preloaded catalog: %v", err)
	}
}
