package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestOfficialTournamentStateReplyUsesCommandClassification(t *testing.T) {
	s, session, _, _ := newTournamentRewardDeliveryFixture(t)
	s.options.currentClientVersion = "official-24.9"
	handled, err := s.handleCurrentTournamentRewardCommand(session, 457, nil)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	p, tail := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if len(tail) != 0 || p.Header.MsgID != 457 || p.Header.Classification != 1 || len(p.Body) != 9 || p.Body[0] != 1 {
		t.Fatalf("native command reply header=%+v body=%x trailing=%d", p.Header, p.Body, len(tail))
	}
}

func TestTournamentRewardReplyClassificationStaysProfileBound(t *testing.T) {
	for _, profile := range []string{"", "90cn", "unknown"} {
		s, session, _, _ := newTournamentRewardDeliveryFixture(t)
		s.options.currentClientVersion = profile
		if _, err := s.handleCurrentTournamentRewardCommand(session, 457, nil); err != nil {
			t.Fatal(err)
		}
		p, _ := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
		if p.Header.Classification != 0 {
			t.Fatalf("profile=%q changed its existing reply route", profile)
		}
	}
}

func TestOfficialTournamentReadyTimerProjectsCommittedRewardsOnce(t *testing.T) {
	s, session, runtime, g := newTournamentRewardDeliveryFixture(t)
	s.options.currentClientVersion = "official-24.9"
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, "")
	before, _, _ := g.Character.Load(context.Background(), "99")
	requestTournamentRewardSelectStateForTest(t, s, session)
	autoSelectTournamentRewardsForTest(t, s, session, runtime)
	data := session.conn.(*bufferConn).write.Bytes()
	var ids []uint16
	var projections [][]byte
	for len(data) > 0 {
		p, tail := splitGameServerUpperPacket(t, data)
		data = tail
		if p.Header.MsgID != 457 && p.Header.MsgID != 458 {
			continue
		}
		if p.Header.Classification != 1 {
			t.Fatalf("handshake uses notification route: %+v", p.Header)
		}
		ids = append(ids, p.Header.MsgID)
		if p.Header.MsgID == 458 {
			projections = append(projections, p.Body)
		}
	}
	if len(ids) != 3 || ids[0] != 458 || ids[1] != 457 || ids[2] != 458 || len(projections) != 2 || !bytes.Equal(projections[0], []byte{1, 2, 0, 255, 2, 255, 255}) || !bytes.Equal(projections[1], []byte{1, 2, 0, 255, 2, 0, 255}) {
		t.Fatalf("timer handshake ids=%v projections=%x", ids, projections)
	}
	after, _, _ := g.Character.Load(context.Background(), "99")
	if after.Stats["gold"]-before.Stats["gold"] != 2468 {
		t.Fatalf("reward gold before=%d after=%d", before.Stats["gold"], after.Stats["gold"])
	}
	session.conn.(*bufferConn).write.Reset()
	for _, request := range [][]byte{{0, 1}, {1, 1}} {
		if _, err := s.handleCurrentTournamentRewardCommand(session, 458, request); err != nil {
			t.Fatal(err)
		}
	}
	duplicate, _, _ := g.Character.Load(context.Background(), "99")
	if duplicate.Stats["gold"] != after.Stats["gold"] {
		t.Fatal("late click granted rewards a second time")
	}
	for _, p := range tournamentSelectionHandshakePackets(t, session) {
		if p.msgID != 458 || !bytes.Equal(p.body, []byte{1, 2, 0, 255, 2, 0, 255}) {
			t.Fatalf("late click did not project original choices: %+v", p)
		}
	}
}

func TestOfficialTournamentNoProjectionBeforeSuccessfulStateReply(t *testing.T) {
	for _, failedReply := range []bool{false, true} {
		s, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
		s.options.currentClientVersion = "official-24.9"
		// A stale connection latch must not make a fresh run ready.
		session.tournamentRewardStateRequested = true
		if failedReply {
			failure := errors.New("state reply failed")
			session.conn = &failNthDungeonWriteConn{failAt: 1, err: failure}
			if _, err := s.handleCurrentTournamentRewardCommand(session, 457, nil); !errors.Is(err, failure) {
				t.Fatalf("state reply err=%v", err)
			}
			session.conn = &bufferConn{}
		}
		autoSelectTournamentRewardsForTest(t, s, session, runtime)
		assertTournamentRewardStateOnlyReply(t, session, 1)
	}
}

func TestOfficialTournamentRollbackStillSuppressesSelection(t *testing.T) {
	s, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	s.options.currentClientVersion = "official-24.9"
	setTournamentRewardAckRollbackForTest(t, s)
	requestTournamentRewardSelectStateForTest(t, s, session)
	autoSelectTournamentRewardsForTest(t, s, session, runtime)
	assertTournamentRewardStateOnlyReply(t, session, 1)
}

func TestOfficialTournamentLegacyOptInsCannotReuseAnotherRunReadiness(t *testing.T) {
	s, session, runtime, _ := newTournamentRewardDeliveryFixture(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.dungeonTournamentRewardAck = dungeonTournamentRewardAckSelectTimer
	t.Setenv(dungeonTournamentRewardSolicitEnvKey, dungeonTournamentRewardSolicitStateRequestValue)
	session.tournamentRewardStateRequested = true
	autoSelectTournamentRewardsForTest(t, s, session, runtime)
	assertTournamentRewardStateOnlyReply(t, session, 1)
}
