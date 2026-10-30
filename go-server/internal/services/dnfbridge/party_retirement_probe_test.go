package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	"testing"
)

func replacementPartyForRetirementProbe(t *testing.T, f leaderExitFixtureState) (party.RuntimePartySnapshot, alignedcmd.PartyState) {
	t.Helper()
	manager := f.service.runtimePartyManagerForService()
	before, ok := manager.SnapshotByUser(99, 0)
	if !ok {
		t.Fatal("old party missing")
	}
	var leader, member party.RuntimePartyMember
	for _, m := range before.Members {
		if m.UserID == 99 {
			leader = m
		}
		if m.UserID == 100 {
			member = m
		}
	}
	created := manager.Create(leader, before.Settings)
	if !created.OK {
		t.Fatal(created.Reason)
	}
	joined := manager.Join(created.Party.ID, member)
	if !joined.OK {
		t.Fatal(joined.Reason)
	}
	if joined.Party.ID == before.ID {
		t.Fatal("replacement kept old ID")
	}
	f.service.publishRuntimePartySnapshot(joined.Party)
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	return joined.Party, before.StateFor(99)
}

func TestRetirementProbeOldDungeonExitPreservesReplacementParty(t *testing.T) {
	f := newLeaderExitFixture(t)
	replacement, _ := replacementPartyForRetirementProbe(t, f)
	sent := f.service.disbandRuntimePartyOnDungeonLeaderExit(f.leader, f.instance, 99, "old_instance_probe")
	actual, found := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	if !found || !sameCurrentPartyProjectionMembership(replacement, actual) || sent != 0 || f.memberConn.write.Len() != 0 || f.leaderConn.write.Len() != 0 {
		t.Fatalf("old instance damaged replacement: found=%t party=%d want=%d sent=%d leader_bytes=%d member_bytes=%d", found, actual.ID, replacement.ID, sent, f.leaderConn.write.Len(), f.memberConn.write.Len())
	}
}

func TestRetirementProbeOldRemovalDoesNotClearReplacementProjection(t *testing.T) {
	f := newLeaderExitFixture(t)
	replacement, previous := replacementPartyForRetirementProbe(t, f)
	if err := f.service.broadcastRuntimePartyMemberRemoved(previous, alignedcmd.PartyState{}, 99, f.leader); err != nil {
		t.Fatal(err)
	}
	current := runtimePartyStateSnapshot(f.member)
	if current.PartyID != int(replacement.ID) || f.memberConn.write.Len() != 0 {
		t.Fatalf("old removal changed new projection: party=%d want=%d bytes=%d", current.PartyID, replacement.ID, f.memberConn.write.Len())
	}
}

func TestRetirementProbeOldRecordClearDoesNotReachReplacementParty(t *testing.T) {
	for _, town := range []bool{false, true} {
		t.Run(map[bool]string{false: "dungeon", true: "town"}[town], func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			_, previous := replacementPartyForRetirementProbe(t, f)
			var err error
			if town {
				err = f.service.clearDungeonSurvivorTownPartyRecords(f.member, previous.Members, "old_record_probe")
			} else {
				err = f.service.clearDisbandedDungeonPartyRecords(f.member, previous.Members, 99, false, "old_record_probe")
			}
			if err != nil || f.memberConn.write.Len() != 0 {
				t.Fatalf("old record removal sent: bytes=%d err=%v", f.memberConn.write.Len(), err)
			}
		})
	}
}
