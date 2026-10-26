package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestPartyDeathDeadlineRetainsSpectatorAndLateCoinEligibility(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue
	state := alignedcmd.PartyState{PartyID: 7, UserID: 99, Members: []alignedcmd.PartyMemberState{{UserID: 99}, {UserID: 100}}}
	storeRuntimePartyState(session, state)
	runtime.partyInstance = &dungeonPartyInstance{id: 1, members: map[uint16]time.Time{99: {}, 100: {}}, sessions: map[uint16]*gameSession{99: session}}
	if err := service.handleDungeonDieCharacter(session, make([]byte, currentDungeonDieCharacterBodySize)); err != nil {
		t.Fatal(err)
	}
	if !runtime.deathReturnWaiting || !runtime.partyInstance.deadMembers[99] {
		t.Fatal("death was not recorded")
	}
	session.conn.(*bufferConn).write.Reset()
	queue.fire(queue.task(t, 0), false)
	if session.dungeon.runtime != runtime || runtime.townReturnPending || !runtime.deathReturnWaiting || session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("party death deadline removed the spectator or invalidated late revival")
	}
	// A second death must not strand the first spectator without a timer.
	runtime.partyInstance.deadMembers[100] = true
	queue.fire(queue.task(t, 0), true)
	if runtime.townReturnPending || !runtime.deathReturnWaiting {
		t.Fatal("whole-party death split the party instead of retaining coin/quit choices")
	}
	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, 99)
	if err := service.handleDungeonUseCoin(session, request); err != nil {
		t.Fatal(err)
	}
	if runtime.deathReturnWaiting || runtime.partyInstance.deadMembers[99] {
		t.Fatal("successful late coin use retained death state")
	}
}

func TestPartyLifeNotifiesPeerAndRefreshesBothHudImmediately(t *testing.T) {
	service, leader, follower, _, runtime := partyDungeonDeathFixture(t)
	markPartyDungeonLife(runtime, follower.selectedCharacterID, true)
	if err := service.broadcastPartyDungeonLife(follower, runtime, false); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	if len(frames) != 2 || frames[0].msgID != 32 || frames[1].msgID != 153 || !bytes.Equal(frames[0].body, []byte{100, 0, 0, 0}) {
		t.Fatalf("peer death sequence = %+v", frames)
	}
	if frames[1].body[8] != 0 {
		t.Fatalf("dead peer HP = %x", frames[1].body)
	}
	leader.conn.(*bufferConn).write.Reset()
	follower.conn.(*bufferConn).write.Reset()
	markPartyDungeonLife(runtime, follower.selectedCharacterID, false)
	if err := service.broadcastPartyDungeonLife(follower, runtime, true); err != nil {
		t.Fatal(err)
	}
	frames = splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	if len(frames) != 2 || !bytes.Equal(frames[0].body, []byte{100, 0, 1, 0}) || frames[1].body[8] != 100 {
		t.Fatalf("peer revive sequence = %+v", frames)
	}
	self := splitUpperFrames(t, follower.conn.(*bufferConn).write.Bytes())
	if len(self) != 1 || self[0].msgID != 153 || !bytes.Equal(self[0].body, frames[1].body) {
		t.Fatal("revive HUD was not refreshed for both participants")
	}
}

func TestPartyDeadStateReplayedAfterRoomActorRebuild(t *testing.T) {
	service, leader, follower, _, runtime := partyDungeonDeathFixture(t)
	markPartyDungeonLife(runtime, follower.selectedCharacterID, true)
	if err := service.replayPartyDungeonDeadStateAfterLoading(leader); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	if len(frames) != 2 || !bytes.Equal(frames[0].body, []byte{100, 0, 0, 0}) || frames[1].msgID != 153 || frames[1].body[8] != 0 {
		t.Fatalf("rebuilt scene did not preserve peer death: %+v", frames)
	}
	leader.conn.(*bufferConn).write.Reset()
	markPartyDungeonLife(runtime, follower.selectedCharacterID, false)
	if err := service.replayPartyDungeonDeadStateAfterLoading(leader); err != nil {
		t.Fatal(err)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("living actors received synthetic death packets")
	}
}

func TestRepeatedDungeonRunWithRetainedKeyReleasesAppearanceResend(t *testing.T) {
	service, leader, follower := peerProjectionEntryFixture(t)
	first := enterDungeonRun(t, service, []*gameSession{leader, follower}, 7)
	peer, ok := service.boundGameSessionCharacterSnapshot(leader)
	if !ok {
		t.Fatal("leader not bound")
	}
	if !service.claimDungeonPeerActorResend(follower, first, peer) || service.claimDungeonPeerActorResend(follower, first, peer) {
		t.Fatal("first-run resend deduplication failed")
	}
	// The follower still has a valid previous run key when the new op29 arrives.
	second := enterDungeonRun(t, service, []*gameSession{leader, follower}, 8)
	if !service.claimDungeonPeerActorResend(follower, second, peer) || service.claimDungeonPeerActorResend(follower, second, peer) {
		t.Fatal("new run reused the previous run's appearance resend marker")
	}
}
