package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func tagReturnRosterSourceOffForTest(t *testing.T, s *Service, p *gameSession) func() {
	t.Helper()
	if len(p.currentRenameRoster) == 0 {
		t.Fatal("fixture did not retain the client-visible roster")
	}
	roster := p.currentRenameRoster
	s.mu.Lock()
	cached := s.tagTournamentSentRosters
	s.tagTournamentSentRosters = nil
	s.mu.Unlock()
	p.currentRenameRoster = nil
	return func() {
		p.currentRenameRoster = roster
		s.mu.Lock()
		s.tagTournamentSentRosters = cached
		s.mu.Unlock()
	}
}

func tagCompletedReturnRosterFailureForTest(
	t *testing.T,
) (*Service, *gameSession, *currentTagTournamentBattle, *runtimeDungeonState, func()) {
	t.Helper()
	s, p := tagResultActionFixture(t, tagtournament.Hard, true)
	b, runtime := p.tagTournamentBattle, p.dungeon.runtime
	if err := s.closeCurrentTagTournamentForReturn(p); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Session.Complete(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, p, p.selectedCharacterID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	restoreRoster := tagReturnRosterSourceOffForTest(t, s, p)
	p.conn.(*bufferConn).write.Reset()
	if err = s.sendCurrentCompletedDungeonReturnToTown(
		p,
		runtime,
		transition,
		uint16(dnfenum.CmdPacketGiveupGame),
		"roster_registration_failure_regression",
	); err == nil {
		t.Fatal("missing roster source was not rejected")
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[currentSceneTransitionMsgID] != 1 ||
		counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		p.dungeon.runtime != runtime || p.tagTournamentBattle != b ||
		p.tagTournamentReturnRoster != nil || runtime.townReturnAckSent {
		t.Fatalf("registration failure crossed the acknowledgement/runtime boundary: counts=%v runtime=%p battle=%p roster=%p ack=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, p.tagTournamentReturnRoster, runtime.townReturnAckSent)
	}
	return s, p, b, runtime, restoreRoster
}

func tagReturnPositionConfirmationBody(runtime *runtimeDungeonState) []byte {
	var body [7]byte
	binary.LittleEndian.PutUint16(body[0:2], uint16(runtime.townReturnTransition.PositionX))
	binary.LittleEndian.PutUint16(body[2:4], uint16(runtime.townReturnTransition.PositionY))
	body[4] = runtime.townReturnTransition.Direction
	return body[:]
}

func TestCompletedTagReturnRetriesRosterBeforeOp35(t *testing.T) {
	s, p, b, runtime, restoreRoster := tagCompletedReturnRosterFailureForTest(t)
	body := tagReturnPositionConfirmationBody(runtime)
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(p, body); err == nil {
		t.Fatal("op35 with the roster source still missing was not rejected")
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		counts[currentTagTournamentRosterNotification] != 0 ||
		p.dungeon.runtime != runtime || p.tagTournamentBattle != b ||
		p.tagTournamentReturnRoster != nil || runtime.townReturnAckSent {
		t.Fatalf("failed op35 retry did not preserve ownership: counts=%v runtime=%p battle=%p roster=%p ack=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, p.tagTournamentReturnRoster, runtime.townReturnAckSent)
	}

	restoreRoster()
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(p, body); err != nil {
		t.Fatal(err)
	}
	counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 1 ||
		counts[currentTagTournamentRosterNotification] != 1 ||
		p.dungeon.runtime != nil || p.tagTournamentBattle != nil ||
		p.tagTournamentReturnRoster != nil || !b.returnRosterSent {
		t.Fatalf("op35 did not retry once before release/Ack: counts=%v runtime=%p battle=%p roster=%p sent=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, p.tagTournamentReturnRoster, b.returnRosterSent)
	}

	p.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(p, body); err != nil {
		t.Fatal(err)
	}
	counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		counts[currentTagTournamentRosterNotification] != 0 {
		t.Fatalf("repeated op35 acknowledged or restored the roster again: %v", counts)
	}
}

func TestCompletedTagReturnRetriesRosterBeforeOp15(t *testing.T) {
	s, p, b, _, restoreRoster := tagCompletedReturnRosterFailureForTest(t)
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendEnterSelectDungeonState(p, "tag_return_roster_retry_missing", false, true); err == nil {
		t.Fatal("op15 with the roster source still missing was not rejected")
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		counts[currentTagTournamentRosterNotification] != 0 ||
		p.dungeon.runtime == nil || p.tagTournamentBattle != b ||
		p.tagTournamentReturnRoster != nil || p.dungeon.runtime.townReturnAckSent {
		t.Fatalf("failed op15 retry did not preserve ownership: counts=%v runtime=%p battle=%p roster=%p",
			counts, p.dungeon.runtime, p.tagTournamentBattle, p.tagTournamentReturnRoster)
	}

	restoreRoster()
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendEnterSelectDungeonState(p, "tag_return_roster_retry", false, true); err != nil {
		t.Fatal(err)
	}
	counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 1 ||
		counts[currentTagTournamentRosterNotification] != 2 ||
		p.dungeon.runtime != nil || p.tagTournamentReturnRoster != nil || !b.returnRosterSent ||
		p.tagTournamentBattle == nil || p.tagTournamentBattle == b {
		t.Fatalf("op15 did not retry once before release/Ack: counts=%v runtime=%p battle=%p roster=%p sent=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, p.tagTournamentReturnRoster, b.returnRosterSent)
	}
	// The first roster restores the saved lobby team. The second installs the
	// root-first native identities before creation of the next paid challenge.
	// They must both precede creation; no delayed roster may destroy new actors.
	var sequence []uint16
	var rosters [][]byte
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		rest = tail
		switch packet.Header.MsgID {
		case currentTagTournamentRosterNotification, 1389, 15:
			sequence = append(sequence, packet.Header.MsgID)
		}
		if packet.Header.MsgID == currentTagTournamentRosterNotification {
			rosters = append(rosters, append([]byte(nil), packet.Body...))
		}
	}
	if !slices.Equal(sequence, []uint16{1388, 1388, 1389, 15}) {
		t.Fatal("return restoration / creation identity / creation / entry order", sequence)
	}
	// This fixture registers the logged-in character first, so saved and
	// creation identities are equal. Reverse registration has separate coverage.
	if !bytes.Equal(rosters[0], b.prepared.creationRosterBody) ||
		!bytes.Equal(rosters[1], p.tagTournamentBattle.prepared.creationRosterBody) {
		t.Fatal("retry restored a different team", rosters)
	}
	assertTagCreationRoster(t, p, p.tagTournamentBattle)

	p.conn.(*bufferConn).write.Reset()
	if err := s.sendEnterSelectDungeonState(p, "tag_return_roster_retry_repeat", false, true); err != nil {
		t.Fatal(err)
	}
	counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		counts[currentTagTournamentRosterNotification] != 0 {
		t.Fatalf("repeated op15 acknowledged or restored the roster again: %v", counts)
	}
}

func TestCompletedTagReturnRosterRetryRejectsStaleCharacterOwner(t *testing.T) {
	s, p, b, runtime, restoreRoster := tagCompletedReturnRosterFailureForTest(t)
	restoreRoster()
	b.identity.generation++
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(p, tagReturnPositionConfirmationBody(runtime)); err == nil {
		t.Fatal("stale character generation released the pending tag return")
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		counts[currentTagTournamentRosterNotification] != 0 ||
		p.dungeon.runtime != runtime || p.tagTournamentBattle != b ||
		p.tagTournamentReturnRoster != nil || runtime.townReturnAckSent {
		t.Fatalf("stale owner escaped isolation: counts=%v runtime=%p battle=%p roster=%p ack=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, p.tagTournamentReturnRoster, runtime.townReturnAckSent)
	}
}

func TestActiveTagReturnRetriesRosterBeforeOp35(t *testing.T) {
	s, p := tagResultActionFixture(t, tagtournament.Hard, false)
	b, runtime := p.tagTournamentBattle, p.dungeon.runtime
	restoreRoster := tagReturnRosterSourceOffForTest(t, s, p)
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonGiveupGame(p, nil); err == nil {
		t.Fatal("active give-up with the roster source missing was not rejected")
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[currentSceneTransitionMsgID] != 1 ||
		counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		p.dungeon.runtime != runtime || p.tagTournamentBattle != b ||
		p.tagTournamentReturnRoster != nil || runtime.townReturnAckSent {
		t.Fatalf("active registration failure crossed the ACK/runtime boundary: counts=%v runtime=%p battle=%p ack=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, runtime.townReturnAckSent)
	}

	body := tagReturnPositionConfirmationBody(runtime)
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(p, body); err == nil {
		t.Fatal("active op35 with the roster source still missing was not rejected")
	}
	counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		counts[currentTagTournamentRosterNotification] != 0 ||
		p.dungeon.runtime != runtime || p.tagTournamentBattle != b ||
		p.tagTournamentReturnRoster != nil || runtime.townReturnAckSent {
		t.Fatalf("failed active op35 retry did not preserve ownership: counts=%v runtime=%p battle=%p ack=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, runtime.townReturnAckSent)
	}

	restoreRoster()
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(p, body); err != nil {
		t.Fatal(err)
	}
	counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 1 ||
		counts[currentTagTournamentRosterNotification] != 1 ||
		p.dungeon.runtime != nil || p.tagTournamentBattle != nil ||
		p.tagTournamentReturnRoster != nil || !b.returnRosterSent {
		t.Fatalf("active op35 did not retry once before release/ACK: counts=%v runtime=%p battle=%p roster=%p sent=%t",
			counts, p.dungeon.runtime, p.tagTournamentBattle, p.tagTournamentReturnRoster, b.returnRosterSent)
	}

	p.conn.(*bufferConn).write.Reset()
	if err := s.handleTownSetUserPosition(p, body); err != nil {
		t.Fatal(err)
	}
	counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[uint16(dnfenum.CmdPacketGiveupGame)] != 0 ||
		counts[currentTagTournamentRosterNotification] != 0 {
		t.Fatalf("repeated active op35 acknowledged or restored the roster again: %v", counts)
	}
}
