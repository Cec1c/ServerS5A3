package dnfbridge

import (
	"encoding/binary"
	"testing"
	"time"
)

func peerNativeRecoveryFixture(t *testing.T) (leaderExitFixtureState, *fakeCurrentDungeonDeathTimerQueue, *dungeonPeerActorRecovery) {
	t.Helper()
	f, q := peerRecoveryFixture(t)
	key, _ := f.service.currentDungeonPartyRunKey(f.leader)
	f.service.scheduleDungeonPeerActorRecovery(f.leader, f.member, key)
	var p *dungeonPeerActorRecovery
	for _, current := range f.leader.dungeonPeerRecovery {
		p = current
	}
	if p == nil {
		t.Fatal("recovery not armed")
	}
	return f, q, p
}

func peerNativeReply(p *dungeonPeerActorRecovery) []byte {
	b := make([]byte, 80)
	copy(b, p.nativeQuery[:])
	b[2] = 'R'
	b[64] = 5
	binary.LittleEndian.PutUint32(b[68:], 1)
	binary.LittleEndian.PutUint32(b[72:], 0x12340000)
	binary.LittleEndian.PutUint32(b[76:], 1)
	return b
}

func TestPartyPeerNativeRegistrationStopsOnlyAcknowledgedRecovery(t *testing.T) {
	f, q, p := peerNativeRecoveryFixture(t)
	q.fire(q.task(t, 0), false)
	if p.nativeQuery[0] == 0 {
		t.Fatal("authoritative membership did not produce native query")
	}
	reply := peerNativeReply(p)
	if err := f.service.handleDungeonPeerNativeRegistrationReply(f.leader, reply); err != nil || !p.nativeRegistered {
		t.Fatal("matching native registry receipt rejected", err)
	}
	f.leaderConn.write.Reset()
	q.fire(q.task(t, 1), false)
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("acknowledged peer was replayed")
	}
	if n, _, active := q.counts(); n != 2 || active != 0 {
		t.Fatal("ACK left recovery running", n, active)
	}
}

func TestPartyPeerNativeRegistrationRejectsStaleAndForeignProof(t *testing.T) {
	for _, fault := range []string{"nonce", "roster", "foreign_connection", "receiver_epoch", "actor_epoch", "new_generation", "deadline", "missing_registry", "missing_epoch", "attempts", "reserved", "non_success", "duplicate_query"} {
		t.Run(fault, func(t *testing.T) {
			f, q, p := peerNativeRecoveryFixture(t)
			q.fire(q.task(t, 0), false)
			if p.nativeQuery[0] == 0 {
				t.Fatal("no challenge")
			}
			b := peerNativeReply(p)
			receiver := f.leader
			switch fault {
			case "nonce":
				b[8] ^= 1
			case "roster":
				b[24] ^= 1
			case "foreign_connection":
				receiver = f.member
			case "receiver_epoch":
				f.leader.dungeonPlacementEpoch.Add(1)
			case "actor_epoch":
				f.member.dungeonPlacementEpoch.Add(1)
			case "new_generation":
				f.service.resetDungeonPeerProjectionGeneration(f.leader, "test")
			case "deadline":
				q.mu.Lock()
				q.now = p.deadline
				q.mu.Unlock()
			case "missing_registry":
				binary.LittleEndian.PutUint32(b[72:], 0)
			case "missing_epoch":
				binary.LittleEndian.PutUint32(b[68:], 0)
			case "attempts":
				binary.LittleEndian.PutUint32(b[76:], 61)
			case "reserved":
				b[66] = 1
			case "non_success":
				b[64] = 4
			case "duplicate_query":
				if err := f.service.sendDungeonPeerNativeRegistrationQuery(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.service.handleDungeonPeerNativeRegistrationReply(receiver, b); err != nil || p.nativeRegistered {
				t.Fatal("stale/invalid receipt accepted", fault, err)
			}
		})
	}
}

func TestPartyPeerRecoveryWaitsForTemporaryLoadingButExpires(t *testing.T) {
	f, q, p := peerNativeRecoveryFixture(t)
	f.member.setFinishLoadingGatePostFinish(false)
	q.fire(q.task(t, 0), false)
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("loading peer replayed")
	}
	if n, _, _ := q.counts(); n != 2 {
		t.Fatal("temporary readiness consumed the whole recovery", n)
	}
	f.member.setFinishLoadingGatePostFinish(true)
	q.fire(q.task(t, 1), false)
	if f.leaderConn.write.Len() == 0 {
		t.Fatal("late ready peer did not resume")
	}
	f.leaderConn.write.Reset()
	q.mu.Lock()
	q.now = p.deadline.Add(time.Nanosecond)
	q.mu.Unlock()
	q.fire(q.task(t, 2), false)
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("recovery exceeded deadline")
	}
	if n, _, active := q.counts(); n != 3 || active != 0 {
		t.Fatal("expired recovery remained armed", n, active)
	}
}
