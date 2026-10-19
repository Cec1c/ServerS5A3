package dnfbridge

import (
	"bytes"
	"testing"
	"time"
)

func TestDungeonMemberExitRemovesActivePeerBeforeSurvivorRoster(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	round69AlignInstancePartyID(t, f, before.ID)
	f.member.dungeon.runtime = nil
	if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
		t.Fatal(err)
	}
	seen, roster := -1, -1
	for index, frame := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
		if frame.msgID == currentTownUserLeaveMsgID {
			if seen >= 0 || !bytes.Equal(frame.body, []byte{0, 0, 0, 100, 0}) {
				t.Fatalf("active removal body=%x", frame.body)
			}
			seen = index
		}
		if frame.msgID == 153 {
			roster = index
		}
	}
	if seen < 0 || roster <= seen {
		t.Fatalf("missing active removal before survivor roster: remove=%d roster=%d", seen, roster)
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
	seen := 0
	for _, frame := range splitUpperFrames(t, f.memberConn.write.Bytes()) {
		if frame.msgID == currentTownUserLeaveMsgID {
			if !bytes.Equal(frame.body, []byte{0, 0, 0, 99, 0}) {
				t.Fatalf("wrong departing key: %x", frame.body)
			}
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("remaining peer active actor removals=%d", seen)
	}
	if f.member.dungeon.runtime != f.memberRuntime {
		t.Fatal("remaining peer pulled out of dungeon")
	}
}
