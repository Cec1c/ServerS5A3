package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func peerRecoveryFixture(t *testing.T) (leaderExitFixtureState, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	q := newFakeCurrentDungeonDeathTimerQueue()
	f.service.gameplayTimers = q
	for _, p := range []*gameSession{f.leader, f.member} {
		p.setFinishLoadingGateState(true)
		p.setFinishLoadingGateCompletion(true)
		p.setFinishLoadingGatePostFinish(true)
	}
	return f, q
}

func TestPartyPeerRecoveryProfilePreparationCannotCrossReentryABA(t *testing.T) {
	f, q := peerRecoveryFixture(t)
	key, _ := f.service.currentDungeonPartyRunKey(f.leader)
	f.service.scheduleDungeonPeerActorRecovery(f.leader, f.member, key)
	changed := false
	wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, func(id string) {
		if !changed && id == strconv.Itoa(int(f.member.selectedCharacterID)) {
			changed = true
			f.service.resetDungeonPeerProjectionGeneration(f.leader, "test_profile_reentry")
		}
	})
	q.fire(q.task(t, 0), false)
	if !changed || f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
		t.Fatal("old recovery crossed a reentry during repository preparation")
	}
	if n, _, _ := q.counts(); n != 1 {
		t.Fatal("retired recovery kept scheduling", n)
	}
}

func TestPartyPeerRecoveryIsBoundedAndDoesNotReplayMapOrMonsters(t *testing.T) {
	f, q := peerRecoveryFixture(t)
	key, _ := f.service.currentDungeonPartyRunKey(f.leader)
	before := f.leaderRuntime.Room.Snapshot()
	f.service.scheduleDungeonPeerActorRecovery(f.leader, f.member, key)
	f.service.scheduleDungeonPeerActorRecovery(f.leader, f.member, key)
	if n, _, _ := q.counts(); n != 1 {
		t.Fatal("duplicate arming", n)
	}
	delays := []time.Duration{750 * time.Millisecond, 1250 * time.Millisecond, 3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second}
	for i, delay := range delays {
		task := q.task(t, i)
		if task.delay != delay {
			t.Fatal("unexpected recovery spacing", task.delay)
		}
		q.fire(task, false)
		frames := peerRecoveryFrames(t, f.leaderConn.write.Bytes())
		created, bound, roster := r160UpperPeerActorCounts(frames, f.member.selectedCharacterID)
		if created != 1 || bound != 1 || roster != 1 {
			t.Fatal("recovery did not deliver one peer snapshot", created, bound, roster)
		}
		for _, frame := range frames {
			if frame.msgID == 29 || frame.msgID == 37 || frame.msgID == 120 || frame.msgID == 23 {
				t.Fatal("recovery replayed scene/placement state", frame.msgID)
			}
		}
		if !reflect.DeepEqual(before, f.leaderRuntime.Room.Snapshot()) {
			t.Fatal("recovery changed monster/room state")
		}
		f.leaderConn.write.Reset()
		q.fire(task, true)
		if f.leaderConn.write.Len() != 0 {
			t.Fatal("duplicate timer callback replayed actors")
		}
	}
	if n, _, active := q.counts(); n != 6 || active != 0 {
		t.Fatal("unbounded recovery", n, active)
	}
	if f.memberConn.write.Len() != 0 {
		t.Fatal("recovery reset the actor's own client")
	}
}

func TestPartyPeerRecoveryRejectsStaleRoomBindingAndDeadActors(t *testing.T) {
	for _, fault := range []string{"receiver_room", "actor_room", "generation_reset", "actor_binding", "receiver_dead", "actor_dead"} {
		t.Run(fault, func(t *testing.T) {
			f, q := peerRecoveryFixture(t)
			key, _ := f.service.currentDungeonPartyRunKey(f.leader)
			f.service.scheduleDungeonPeerActorRecovery(f.leader, f.member, key)
			switch fault {
			case "receiver_room":
				f.leader.dungeonPartyRun.Seed++
			case "actor_room":
				f.member.dungeonPartyRun.Seed++
			case "generation_reset":
				f.service.resetDungeonPeerProjectionGeneration(f.leader, "test_reentry")
			case "actor_binding":
				f.member.characterGeneration++
			case "receiver_dead":
				markPartyDungeonLife(f.leaderRuntime, f.leader.selectedCharacterID, true)
			case "actor_dead":
				markPartyDungeonLife(f.memberRuntime, f.member.selectedCharacterID, true)
			}
			q.fire(q.task(t, 0), true)
			if f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
				t.Fatal("stale/dead recovery wrote actor state")
			}
			if n, _, _ := q.counts(); n != 1 {
				t.Fatal("stale recovery kept scheduling", n)
			}
		})
	}
}

// Recovery carries standard game notifications followed by one class1 fixed16
// challenge. Verify that extension's exact shape before parsing ordinary frames.
func peerRecoveryFrames(t *testing.T, stream []byte) []upperFrame {
	t.Helper()
	var ordinary []byte
	queries := 0
	for len(stream) > 0 {
		if len(stream) < 13 {
			t.Fatal("truncated recovery header")
		}
		size := int(binary.LittleEndian.Uint32(stream[3:7]))
		if size < 13 || size > len(stream) {
			t.Fatal("invalid recovery frame size")
		}
		frame := stream[:size]
		if binary.LittleEndian.Uint16(frame[1:3]) == dungeonPeerNativeRegistrationOpcode {
			if len(frame) != 80 || frame[0] != 1 || frame[15] != 0 || string(frame[16:20]) != "ARQ1" || frame[20] != 1 {
				t.Fatalf("invalid native registration query: %x", frame)
			}
			queries++
		} else {
			ordinary = append(ordinary, frame...)
		}
		stream = stream[size:]
	}
	if queries != 1 {
		t.Fatalf("queries=%d want exactly one per round", queries)
	}
	return splitUpperFrames(t, ordinary)
}
