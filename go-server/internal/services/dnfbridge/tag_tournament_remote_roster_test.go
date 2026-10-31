package dnfbridge

import (
	"bytes"
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func tagRemoteRosterLogin(t *testing.T, s *Service, token string, ch channelcatalog.Channel) *gameSession {
	t.Helper()
	p := &gameSession{conn: &bufferConn{}, clientIP: "203.0.113.99", loginPacketSeen: true, channel: ch}
	if !s.bindGameSessionAccountFromLoginToken(p, 1, buildClientLoginBody(token)) || !p.accountFromToken || p.clientPID != 0 {
		t.Fatal("remote login did not establish authenticated PID-zero session")
	}
	return p
}

func TestTagTournamentRemoteLoginKeepsSentRosterAcrossChannel(t *testing.T) {
	s, initial, g := newTagRosterBridgeTest(t)
	s.options.channelAdvertiseID = 1
	token, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	s.loginTokens.remember(token, "a", time.Now().Add(time.Hour))
	ordinary := initial.channel
	ordinary.ID, ordinary.Type = 10, 0
	first := tagRemoteRosterLogin(t, s, token, ordinary)
	// This is the compact snapshot retained only after the normal op2 write.
	// Its order (60,17,29) intentionally differs from repository Slot order.
	s.rememberCurrentRenameRoster(first, initial.currentRenameRoster)
	next := tagRemoteRosterLogin(t, s, token, initial.channel)
	next.selectedCharacterID, next.characterGeneration, next.sceneBootstrapTailSent = 29, 2, true
	s.gameSessions[29] = next
	if err = s.handleCurrentTagTournamentRosterList(next, nil); err != nil {
		t.Fatal(err)
	}
	wire := next.conn.(*bufferConn)
	if wire.write.Len() == 0 {
		t.Fatal("PID-zero authenticated channel switch lost the sent roster; 1467 produced no 1403")
	}
	counts, rest := splitCurrentGameServerUpperPacketAuto(t, wire.write.Bytes())
	if counts.Header.MsgID != 1403 || !bytes.Equal(counts.Body, []byte{0}) || len(rest) != 0 {
		t.Fatal("remote open response", counts, rest)
	}
	wire.write.Reset()
	request := []byte{0, 0, 0, 2}
	if err = s.handleCurrentTagTournamentSetRoster(next, request); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitCurrentGameServerUpperPacketAuto(t, wire.write.Bytes())
	if ack.Header.MsgID != 1457 || !bytes.Equal(ack.Body, upperSuccessBody(request)) || len(rest) != 0 {
		t.Fatal("remote selection lost compact order", ack, rest)
	}
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		t.Fatal(err)
	}
	view, err := owner.Lobby(context.Background(), "a")
	if err != nil || !view.Registered || view.Characters[0].CharacterID != "60" || view.Characters[1].CharacterID != "29" || view.Day.Paid {
		t.Fatal(view, err)
	}
	for _, id := range []string{"29", "60"} {
		ch, _, err := g.Character.Load(context.Background(), id)
		if err != nil || ch.Stats["gold"] != 1000000 {
			t.Fatal("team setup changed gold", id, err)
		}
	}
}

func TestTagTournamentRemoteRosterDoesNotBorrowAnotherLogin(t *testing.T) {
	for _, scenario := range []string{"same", "other-flow", "unverified", "missing-flow", "other-account", "other-server", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, _ := newTagRosterBridgeTest(t)
			p.clientPID, p.accountFromToken, p.loginChannelFlowKey = 12345, true, [32]byte{1}
			s.rememberCurrentRenameRoster(p, p.currentRenameRoster)
			next := &gameSession{accountID: p.accountID, loginPacketSeen: true, accountFromToken: true, clientPID: 12345, loginChannelFlowKey: p.loginChannelFlowKey}
			server := byte(1)
			switch scenario {
			case "other-flow":
				next.loginChannelFlowKey[0]++
			case "unverified":
				next.accountFromToken = false
			case "missing-flow":
				next.loginChannelFlowKey = [32]byte{}
			case "other-account":
				next.accountID = "b"
			case "other-server":
				server = 2
			case "revoked":
				next.loginRevoked.Store(true)
			}
			got := s.cachedCurrentTagTournamentSentRoster(next, server)
			if (len(got) == 3) != (scenario == "same") {
				t.Fatalf("scenario %s inherited %d rows", scenario, len(got))
			}
		})
	}
}

func TestTagTournamentRemoteRosterInvalidation(t *testing.T) {
	for _, invalid := range [][]repo.CharacterRecord{nil, {{CharacterID: "29", AccountID: "b", Slot: 8, Name: "wrong owner"}}} {
		s, p, _ := newTagRosterBridgeTest(t)
		p.accountFromToken, p.loginChannelFlowKey = true, [32]byte{1}
		s.rememberCurrentRenameRoster(p, p.currentRenameRoster)
		s.rememberCurrentRenameRoster(p, invalid)
		if got := s.cachedCurrentTagTournamentSentRoster(p, 1); len(got) != 0 {
			t.Fatal("invalid roster retained previous remote cache")
		}
	}
}
