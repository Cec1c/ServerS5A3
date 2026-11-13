package dnfbridge

import (
	"testing"
	"time"
)

func TestReturnSelectDungeonDetachKeepsSurvivorWithoutWaitingForWire(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	gate := newReturnSelectWriteGate(f.memberConn)
	f.member.conn = gate
	startReturnSelectReceiverLoop(t, f.service, f.member)
	t.Cleanup(gate.Release)
	result := make(chan error, 1)
	go func() {
		removed, err := f.service.leaveRuntimePartyDungeonMember(f.leader, f.instance, "detach:return_select_character", false)
		if !removed && err == nil {
			t.Error("dungeon departure did not remove the departing owner")
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		gate.Release()
		t.Fatal("dungeon detach waited for survivor wire")
	}
	if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0); found {
		t.Fatal("departing leader still owns dungeon membership")
	}
	if f.member.dungeon.runtime != f.memberRuntime || f.memberRuntime.partyInstance != f.instance {
		t.Fatal("survivor dungeon runtime changed")
	}
	waitTestSignal(t, gate.entered, "survivor roster was not queued")
	if f.memberConn.write.Len() != 0 {
		t.Fatal("blocked survivor received bytes")
	}
	gate.Release()
	drainReturnSelectReceiver(t, f.service, f.member)
	_, rosters, realtime := returnSelectFrameCounts(t, f.memberConn.write.Bytes())
	if rosters == 0 || realtime == 0 {
		t.Fatalf("survivor roster=%d realtime=%d", rosters, realtime)
	}
}

func TestReturnSelectRaidRosterDoesNotWaitForOccupiedReceiver(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	f.formOrdinaryParty(t)
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	startReturnSelectReceiverLoop(t, f.service, f.leader)
	release := holdReturnSelectReceiverLoop(t, f.service, f.leader)
	done := make(chan struct{})
	go func() {
		f.service.detachRuntimePartySession(f.member, "return_select_character")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		release()
		t.Fatal("raid roster delayed character detach")
	}
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("raid refresh bypassed occupied receiver queue")
	}
	release()
	drainReturnSelectReceiver(t, f.service, f.leader)
	found := false
	for _, frame := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
		found = found || frame.msgID == 591
	}
	if !found {
		t.Fatal("survivor did not receive the updated raid solo roster")
	}
}
