package dnfbridge

import (
	"bytes"
	"testing"
	"time"
)

func TestDungeonMemberExitRetiresNetworkRosterBeforeActivePeer(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	addRetirementPeer(t, f, 101)
	before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	round69AlignInstancePartyID(t, f, before.ID)
	f.member.dungeon.runtime = nil
	if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
		t.Fatal(err)
	}
	seen, roster, realtime := -1, -1, -1
	for index, frame := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
		if frame.msgID == currentTownUserLeaveMsgID {
			if seen >= 0 || !bytes.Equal(frame.body, []byte{0, 0, 0, 100, 0}) {
				t.Fatalf("active removal body=%x", frame.body)
			}
			seen = index
		}
		if frame.msgID == 9 && len(frame.body) > 10 {
			roster = index
		}
		if frame.msgID == 153 {
			realtime = index
		}
	}
	if roster < 0 || seen <= roster || realtime <= seen {
		t.Fatalf("need roster then actor removal then realtime: roster=%d remove=%d realtime=%d", roster, seen, realtime)
	}
	for _, frame := range splitUpperFrames(t, f.memberConn.write.Bytes()) {
		if frame.msgID == currentTownUserLeaveMsgID {
			t.Fatal("leaver was told to destroy its own active actor")
		}
	}
	if f.leader.dungeon.runtime != f.leaderRuntime {
		t.Fatal("survivor runtime changed")
	}
}

func TestDungeonMemberExitSurvivorRosterDoesNotTakePeerSessionLock(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	state := runtimePartyStateSnapshot(f.leader)
	f.leader.dungeon.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- f.service.sendMemberGiveupSurvivorRoster(state, f.instance) }()
	select {
	case err := <-done:
		f.leader.dungeon.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		f.leader.dungeon.mu.Unlock()
		<-done
		t.Fatal("cross-session lock blocks leave processing while survivor is busy")
	}
}

func TestDungeonLeaderExitRemovesActiveLeaderForRemainingPeer(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	f.service.disbandRuntimePartyOnDungeonLeaderExit(f.leader, f.instance, 99, "active_actor_test")
	seen, localRetired, activeRemoved := 0, -1, -1
	for index, frame := range splitUpperFrames(t, f.memberConn.write.Bytes()) {
		if frame.msgID == 9 && len(frame.body) == 10 && bytes.Equal(frame.body[4:6], []byte{100, 0}) {
			localRetired = index
		}
		if frame.msgID == currentTownUserLeaveMsgID {
			if !bytes.Equal(frame.body, []byte{0, 0, 0, 99, 0}) {
				t.Fatalf("wrong departing key: %x", frame.body)
			}
			seen++
			activeRemoved = index
		}
	}
	if seen != 1 {
		t.Fatalf("remaining peer active actor removals=%d", seen)
	}
	if localRetired < 0 || activeRemoved <= localRetired {
		t.Fatalf("leader actor erased before local roster retirement: local=%d active=%d", localRetired, activeRemoved)
	}
	if f.member.dungeon.runtime != f.memberRuntime {
		t.Fatal("remaining peer pulled out of dungeon")
	}
}
