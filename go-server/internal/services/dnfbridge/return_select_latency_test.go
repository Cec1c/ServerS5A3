package dnfbridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/protocol"
)

// returnSelectWriteGate blocks the receiver's real socket write until Release.
// The write runs on the receiver's session event loop, so the leaving player's
// op7 ACK must already be on its own wire while this gate is still closed.
type returnSelectWriteGate struct {
	*bufferConn
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newReturnSelectWriteGate(conn *bufferConn) *returnSelectWriteGate {
	return &returnSelectWriteGate{
		bufferConn: conn,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (gate *returnSelectWriteGate) Write(data []byte) (int, error) {
	gate.enterOnce.Do(func() {
		close(gate.entered)
		<-gate.release
	})
	return gate.bufferConn.Write(data)
}

func (gate *returnSelectWriteGate) Release() {
	gate.releaseOnce.Do(func() { close(gate.release) })
}

func startReturnSelectReceiverLoop(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	session.ctx = ctx
	if err := service.startGameSessionEvents(ctx, session); err != nil {
		cancel()
		t.Fatalf("start receiver event loop: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		stopTestGameSessionEvents(t, service, session)
	})
}

// holdReturnSelectReceiverLoop keeps the receiver's real event loop busy, so
// the detach projection is queued instead of running immediately.
func holdReturnSelectReceiverLoop(t *testing.T, service *Service, session *gameSession) func() {
	t.Helper()
	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	active := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	if err := service.postGameSessionCharacterEvent(session, "return-select-receiver-hold", session.selectedCharacterID, session.characterGeneration, func() error {
		defer close(active)
		close(entered)
		<-releaseCh
		return nil
	}); err != nil {
		release()
		t.Fatalf("occupy receiver event loop: %v", err)
	}
	waitTestSignal(t, entered, "receiver event-loop hold did not start")
	t.Cleanup(func() {
		release()
		select {
		case <-active:
		case <-time.After(time.Second):
			t.Error("receiver event-loop hold did not exit")
		}
	})
	return release
}

func drainReturnSelectReceiver(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.callGameSession(ctx, session, "return-select-receiver-drain", func() error { return nil }); err != nil {
		t.Fatalf("drain receiver event loop: %v", err)
	}
}

func assertReturnSelectACK(t *testing.T, stream []byte) {
	t.Helper()
	ack, rest := splitGameServerUpperPacket(t, stream)
	if ack.Header.Classification != protocol.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketReturnSelectCharacter) ||
		!bytesEqualByte(ack.Body, 1) {
		t.Fatalf("op7 ack = class %d msg %d body % X", ack.Header.Classification, ack.Header.MsgID, ack.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("op7 ack trailing bytes = %d", len(rest))
	}
}

func returnSelectFrameCounts(t *testing.T, stream []byte) (notices, rosters, realtime int) {
	t.Helper()
	for _, frame := range splitUpperFrames(t, stream) {
		switch frame.msgID {
		case currentAreaChatNotifyOpcode:
			notices++
		case uint16(dnfenum.CmdPacketRecoverStamina):
			rosters++
		case 0x0099:
			realtime++
		}
	}
	return notices, rosters, realtime
}

// TestReturnSelectMemberACKDoesNotWaitForBlockedSurvivorWire pins the ordinary
// member path: returning to character select commits the manager removal and
// the member's own cleanup, then writes op7. The survivor's notice and roster
// belong to the survivor's event loop, so a blocked peer wire cannot delay the
// ACK; once the wire is released both frames arrive.
func TestReturnSelectMemberACKDoesNotWaitForBlockedSurvivorWire(t *testing.T) {
	h := newPartyLeaveHarness(t)
	manager := h.service.runtimePartyManagerForService()

	leaderBase := h.leaderConn
	gate := newReturnSelectWriteGate(leaderBase)
	h.leader.conn = gate
	startReturnSelectReceiverLoop(t, h.service, h.leader)
	t.Cleanup(gate.Release)

	returned := make(chan error, 1)
	go func() {
		returned <- h.service.handleUpperReturnSelectCharacter(h.member, nil)
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("member return-select: %v", err)
		}
	case <-time.After(time.Second):
		gate.Release()
		t.Fatal("member op7 ACK waited for the survivor's blocked wire")
	}

	if snapshot, found := manager.SnapshotByUser(1002, 0); found {
		t.Fatalf("member is still in the runtime party after the ACK: %+v", snapshot)
	}
	if state := runtimePartyStateSnapshot(h.member); state.PartyID != 0 {
		t.Fatalf("member client projection was not cleared before the ACK: %+v", state)
	}
	if h.member.selectedCharacterID != 0 {
		t.Fatalf("member selection was not cleared before the ACK: char=%d", h.member.selectedCharacterID)
	}
	assertReturnSelectACK(t, h.memberConn.write.Bytes())

	waitTestSignal(t, gate.entered, "survivor event loop never attempted the queued projection")
	if got := leaderBase.write.Len(); got != 0 {
		t.Fatalf("survivor wire received %d bytes while blocked", got)
	}

	gate.Release()
	drainReturnSelectReceiver(t, h.service, h.leader)
	notices, rosters, _ := returnSelectFrameCounts(t, leaderBase.write.Bytes())
	if notices != 1 || rosters == 0 {
		t.Fatalf("survivor notices=%d rosters=%d after release; want one notice and at least one roster", notices, rosters)
	}
}

// TestReturnSelectLeaderACKDoesNotWaitForBlockedSurvivorWire covers the retired
// party path. A leaving leader retires the old generation and rebuilds the
// survivor's lobby; neither the retired clear nor the rebuilt roster may hold
// the leader's op7 ACK behind the survivor's wire.
func TestReturnSelectLeaderACKDoesNotWaitForBlockedSurvivorWire(t *testing.T) {
	h := newPartyLeaveHarness(t)
	manager := h.service.runtimePartyManagerForService()

	memberBase := h.memberConn
	gate := newReturnSelectWriteGate(memberBase)
	h.member.conn = gate
	startReturnSelectReceiverLoop(t, h.service, h.member)
	t.Cleanup(gate.Release)

	returned := make(chan error, 1)
	go func() {
		returned <- h.service.handleUpperReturnSelectCharacter(h.leader, nil)
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("leader return-select: %v", err)
		}
	case <-time.After(time.Second):
		gate.Release()
		t.Fatal("leader op7 ACK waited for the survivor's blocked wire")
	}

	if snapshot, found := manager.SnapshotByUser(1001, 0); found {
		t.Fatalf("leader is still in the runtime party after the ACK: %+v", snapshot)
	}
	rebuilt, found := manager.SnapshotByUser(1002, 0)
	if !found || rebuilt.ID == h.partyID || rebuilt.Leader != 1002 || len(rebuilt.Members) != 1 || rebuilt.Members[0].UserID != 1002 {
		t.Fatalf("survivor rebuild = found:%t snapshot:%+v; want a new party led by 1002", found, rebuilt)
	}
	if state := runtimePartyStateSnapshot(h.leader); state.PartyID != 0 {
		t.Fatalf("leader client projection was not cleared before the ACK: %+v", state)
	}
	if h.leader.selectedCharacterID != 0 {
		t.Fatalf("leader selection was not cleared before the ACK: char=%d", h.leader.selectedCharacterID)
	}
	assertReturnSelectACK(t, h.leaderConn.write.Bytes())

	waitTestSignal(t, gate.entered, "survivor event loop never attempted the retired-party projection")
	if got := memberBase.write.Len(); got != 0 {
		t.Fatalf("survivor wire received %d bytes while blocked", got)
	}

	gate.Release()
	drainReturnSelectReceiver(t, h.service, h.member)
	frames := splitUpperFrames(t, memberBase.write.Bytes())
	rosters, realtime := 0, 0
	var lastRoster *upperFrame
	for index := range frames {
		switch frames[index].msgID {
		case uint16(dnfenum.CmdPacketRecoverStamina):
			rosters++
			lastRoster = &frames[index]
		case 0x0099:
			realtime++
		}
	}
	if rosters == 0 || realtime == 0 {
		t.Fatalf("survivor rosters=%d realtime=%d after release; want the retired clear and rebuilt roster", rosters, realtime)
	}
	if len(lastRoster.body) < 8 {
		t.Fatalf("final roster frame too short: % X", lastRoster.body)
	}
	hasIdentity := int(lastRoster.body[7]) != currentClientSelectedChannelID(h.member)
	rows, leaderSlot := parseOp9PartyFrame(t, lastRoster.body, hasIdentity)
	if len(rows) != 1 || rows[0].uid != 1002 || leaderSlot != rows[0].slot {
		t.Fatalf("final roster rows=%+v leader_slot=%d; want the rebuilt party led by 1002", rows, leaderSlot)
	}
	if state := runtimePartyStateSnapshot(h.member); state.PartyID != int(rebuilt.ID) || state.UserID != 1002 {
		t.Fatalf("survivor projection = %+v; want rebuilt party %d led by 1002", state, rebuilt.ID)
	}
}

// TestReturnSelectQueuedRosterDropsAfterReceiverRegroups replaces the receiver's
// party generation after the detach roster was queued but before it ran. The
// queued projection must not overwrite the newer lobby.
func TestReturnSelectQueuedRosterDropsAfterReceiverRegroups(t *testing.T) {
	h := newPartyLeaveHarness(t)
	startReturnSelectReceiverLoop(t, h.service, h.leader)
	release := holdReturnSelectReceiverLoop(t, h.service, h.leader)

	returned := make(chan error, 1)
	go func() {
		returned <- h.service.handleUpperReturnSelectCharacter(h.member, nil)
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("member return-select: %v", err)
		}
	case <-time.After(time.Second):
		release()
		t.Fatal("member op7 ACK waited for the queued receiver projection")
	}

	identity, ok := h.service.boundGameSessionCharacterSnapshot(h.leader)
	if !ok {
		t.Fatal("leader character binding is missing")
	}
	manager := h.service.runtimePartyManagerForService()
	if result := manager.Leave(1001, identity.generation); !result.OK {
		t.Fatalf("receiver leave old lobby: %+v", result)
	}
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: identity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, alignedcmd.PartyState{NameBytes: []byte("regrouped"), MaxMembers: 4, UserState: 1})
	if !created.OK || created.Party.ID == h.partyID {
		t.Fatalf("receiver regroup: %+v", created)
	}
	if state := h.service.publishRuntimePartySnapshot(created.Party); state.PartyID != int(created.Party.ID) {
		t.Fatalf("receiver regroup projection = %+v; want party %d", state, created.Party.ID)
	}

	release()
	drainReturnSelectReceiver(t, h.service, h.leader)
	notices, rosters, realtime := returnSelectFrameCounts(t, h.leaderConn.write.Bytes())
	if rosters != 0 || realtime != 0 {
		t.Fatalf("stale roster frames=%d realtime=%d (notice=%d) overwrote the regrouped lobby stream=% X",
			rosters, realtime, notices, h.leaderConn.write.Bytes())
	}
	if state := runtimePartyStateSnapshot(h.leader); state.PartyID != int(created.Party.ID) {
		t.Fatalf("stale queue replaced the regrouped projection: %+v", state)
	}
	if current, found := manager.SnapshotByUser(1001, 0); !found || current.ID != created.Party.ID {
		t.Fatalf("regrouped lobby was lost: found=%t snapshot=%+v", found, current)
	}
}

// TestReturnSelectQueuedRosterDropsAfterReceiverGenerationRebind pins the
// binding guard: once the receiver binds a new character generation, a roster
// queued for the previous generation must not reach the wire or its cache.
func TestReturnSelectQueuedRosterDropsAfterReceiverGenerationRebind(t *testing.T) {
	h := newPartyLeaveHarness(t)
	startReturnSelectReceiverLoop(t, h.service, h.leader)
	release := holdReturnSelectReceiverLoop(t, h.service, h.leader)

	returned := make(chan error, 1)
	go func() {
		returned <- h.service.handleUpperReturnSelectCharacter(h.member, nil)
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("member return-select: %v", err)
		}
	case <-time.After(time.Second):
		release()
		t.Fatal("member op7 ACK waited for the queued receiver projection")
	}

	if drifted := advanceGameSessionCharacterGeneration(h.leader); drifted == 0 {
		t.Fatal("advance receiver generation failed")
	}
	release()
	drainReturnSelectReceiver(t, h.service, h.leader)
	if got := h.leaderConn.write.Len(); got != 0 {
		t.Fatalf("stale generation wrote %d bytes after the receiver rebound", got)
	}
}
