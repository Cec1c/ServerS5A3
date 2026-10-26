package dnfbridge

import (
	"context"
	"testing"
)

// Exercise a new socket and new op16/op37, without transferring the retired
// runtime, RunID, loading readiness, or actor claims into the replacement.
func TestR186AntonReconnectThenNewEntryProjectsBothOwners(t *testing.T) {
	f, owners, conns := r160AntonLegalFixture(t)
	s := f.service
	r160EnterRaidGroup(t, f, owners, 0, 210)
	r160WaitRaidProjection(t, f, owners)
	old := owners[1]
	oldRun := old.dungeon.runtime
	firstRunID := oldRun.raidEntry.RunID
	s.shutdownGameSessionEvents(old, false)
	// The survivor leaves the old attempt through its real return handshake.
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.handleDungeonGiveupGame(owners[0], nil) })
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.sendEnterSelectDungeonState(owners[0], "upper_15", false, true) })
	if owners[0].dungeon.runtime != nil {
		t.Fatal("survivor retained old dungeon after confirmed return")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fresh := f.session()
	fresh.conn = &deadlineAuditV10Conn{}
	fresh.ctx, fresh.connID = ctx, "r186-replacement"
	fresh.accountID, fresh.channel = old.accountID, old.channel
	fresh.loginPacketSeen, fresh.accountFromToken = true, true
	s.bindGameSessionCharacter(fresh, old.selectedCharacterID)
	if err := s.startGameSessionEvents(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.shutdownGameSessionEvents(fresh, false) })
	deadlineAuditV10NormalTown(t, f, fresh)
	deadlineAuditV10Event(t, s, fresh, func() error { return s.handleGameCommand(fresh, 1, 668, rejoinContextRequest(f.state.RaidKey)) })
	if fresh.dungeon.runtime != nil {
		t.Fatal("rejoin copied old dungeon into uninitialized client")
	}
	owners[1], conns[1] = fresh, fresh.conn.(*deadlineAuditV10Conn)
	// Raid squad membership remains frozen; ordinary party formation uses the
	// same manager boundary as the public invitation accept handler.
	state, result, ok := s.createOrJoinManagedRuntimeParty(fresh, owners[0])
	if !ok && result.Reason != "already_member" {
		t.Fatalf("reconnect regroup failed: %+v", result)
	}
	if state.PartyID <= 0 {
		t.Fatal("reconnect regroup has no party")
	}
	for _, c := range conns {
		c.reset()
	}
	r160EnterRaidGroup(t, f, owners, 0, 210)
	r160WaitRaidProjection(t, f, owners)
	for i, p := range owners[:2] {
		p.dungeon.mu.Lock()
		r := p.dungeon.runtime
		valid := r != nil && r != oldRun && r.raidEntry != nil && r.raidEntry.RunID != firstRunID
		p.dungeon.mu.Unlock()
		if !valid {
			t.Fatalf("owner %d failed to acquire fresh runtime", i)
		}
		var frames []upperFrame
		for _, frame := range r160FindFrames(t, conns[i].bytes()) {
			frames = append(frames, upperFrame{msgID: frame.Header.MsgID, body: frame.Body})
		}
		a, b, c := r160UpperPeerActorCounts(frames, owners[1-i].selectedCharacterID)
		if a == 0 || b == 0 || c == 0 {
			t.Fatalf("receiver %d missing reconnected peer actors: %d/%d/%d", p.selectedCharacterID, a, b, c)
		}
	}
	left, right := owners[0].dungeon.runtime, fresh.dungeon.runtime
	if left.raidEntry.RunID != right.raidEntry.RunID || left.partyInstance == nil || left.partyInstance != right.partyInstance {
		t.Fatal("newly entered peers do not share the same raid run and dungeon instance")
	}
	// A late old owner cleanup must not erase the new participation or hit owner.
	s.releaseCurrentRaidParticipationSession(old)
	s.releasePartyDungeonInstance(oldRun, old.selectedCharacterID, "r186_late_socket_cleanup")
	s.raidMu.Lock()
	entry := f.state.Battle.Participation[fresh.selectedCharacterID]
	s.raidMu.Unlock()
	if entry.Session != fresh || entry.Runtime != right || !dungeonPartyInstanceHasMember(left.partyInstance, fresh.selectedCharacterID) {
		t.Fatal("retired socket removed the new participation or damage ownership")
	}
	for i, p := range owners[:2] {
		p.dungeon.mu.Lock()
		verdict := s.resolveCurrentDungeonPartyHitOwner(p, p.dungeon.runtime, owners[1-i].selectedCharacterID, dungeonPartyHitOwnerParty)
		p.dungeon.mu.Unlock()
		if !verdict.accepted || !verdict.sameInstance {
			t.Fatalf("reconnected hit owner %d rejected: %+v", p.selectedCharacterID, verdict)
		}
	}
}
