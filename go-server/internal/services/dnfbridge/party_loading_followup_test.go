package dnfbridge

import (
	"testing"
	"time"
)

func TestPartyLoadingProjectionDoesNotArmBlindRecovery(t *testing.T) {
	f, q := peerRecoveryFixture(t)
	f.service.finishDungeonPartyPeerActorProjection(f.leader, "loading_completed")
	if n, _, _ := q.counts(); n != 0 {
		t.Fatalf("loading armed %d blind actor recovery timers", n)
	}
}

func TestPartyLoadingSocialDefersAndRejectsOldRoom(t *testing.T) {
	f, q := peerRecoveryFixture(t)
	f.service.deferPartyLoadingSocial(f.leader, "loaded")
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("social packets blocked the loading call")
	}
	task := q.task(t, 0)
	if task.delay != time.Second {
		t.Fatal("social delay", task.delay)
	}
	f.leader.dungeonPlacementEpoch.Add(1)
	q.fire(task, false)
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("old-room social callback wrote into the new scene")
	}
}
