package dnfbridge

import (
	"bytes"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func onlineAntonTerminalFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, runtimeRaidState, time.Time) {
	t.Helper()
	f, _, now := actualFinalAntonWeeklyFixture(t)
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	f.state.Battle.ChannelID, f.state.Battle.ChannelType = session.channel.ID, session.channel.Type
	return f, session, cloneRuntimeRaidState(*f.state), now
}

func assertAntonResultStream(t *testing.T, stream []byte, option byte, failed bool) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, stream)
	if packet.Header.MsgID != 601 || len(packet.Body) != 10 || packet.Body[9] != option || (packet.Body[0] == 1) != failed {
		t.Fatal("incorrect authoritative result", packet)
	}
	if failed {
		if len(rest) != 0 {
			t.Fatal("timeout sent a successful phase transition")
		}
		return
	}
	packet, rest = splitGameServerUpperPacket(t, rest)
	if packet.Header.MsgID != 587 || !bytes.Equal(packet.Body, []byte{3, 0}) || len(rest) != 0 {
		t.Fatal("missing result-stage transition", packet, rest)
	}
}

func TestRealPVFAntonOnlineFinalResultWaitsForAtomicWeeklyCommitAndDoesNotReplay(t *testing.T) {
	f, session, state, now := onlineAntonTerminalFixture(t)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil || f.conn.write.Len() != 0 || session.raidProjection.resultSent {
		t.Fatal("uncommitted final success published", err)
	}
	completed, errs := f.service.commitCurrentRaidWeeklyIntents(now)
	if len(errs) != 0 || len(completed) != 1 {
		t.Fatal(completed, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("terminal result replay", err)
	}
	// A new authenticated connection/generation may restore its own result.
	replacement := f.session()
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
}

func TestRealPVFAntonOnlineResultWeeklyFailureCannotPublishSuccess(t *testing.T) {
	f, session, state, now := onlineAntonTerminalFixture(t)
	original := f.repos.CharacterTrade
	f.repos.CharacterTrade = raidFailGroupTransaction{CharacterTradeUnitOfWork: original, group: original.(repo.CharacterInventoryGroupUnitOfWork), commitFailure: true}
	if completed, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(completed) != 0 || len(errs) != 1 {
		t.Fatal("injected late transaction failure accepted", completed, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("failed weekly transaction published success", err)
	}
	f.repos.CharacterTrade = original
	if completed, errs := f.service.commitCurrentRaidWeeklyIntents(now.Add(2 * time.Second)); len(completed) != 1 || len(errs) != 0 {
		t.Fatal("retry failed", completed, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
}

func TestRealPVFAntonOnlineResultIneligibleObserverAndForeignOwners(t *testing.T) {
	f, _, state, now := onlineAntonTerminalFixture(t)
	if completed, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(completed) != 1 || len(errs) != 0 {
		t.Fatal(completed, errs)
	}
	observer := f.session()
	observer.selectedCharacterID, observer.accountID = 21, "account-21"
	observer.loginPacketSeen, observer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(observer, 21)
	if err := f.service.sendCurrentAntonTerminalResult(observer, state); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 1, false)
	f.conn.write.Reset()
	for _, name := range []string{"account", "login", "channel", "instance", "phase", "removed", "receipt_command"} {
		t.Run(name, func(t *testing.T) {
			g, session, captured, at := onlineAntonTerminalFixture(t)
			if completed, errs := g.service.commitCurrentRaidWeeklyIntents(at); len(completed) != 1 || len(errs) != 0 {
				t.Fatal(completed, errs)
			}
			switch name {
			case "account":
				session.accountID = "foreign"
			case "login":
				session.loginPacketSeen = false
			case "channel":
				session.channel.ID++
			case "instance":
				captured.Battle.InstanceID = "99112233445566778899aabbccddeeff"
			case "phase":
				captured.Battle.Phase = 0
			case "removed":
				g.state.Members = g.state.Members[1:]
			case "receipt_command":
				g.service.raidWeeklyIntents[g.state.Battle.InstanceID].Command.Limit++
			}
			_ = g.service.sendCurrentAntonTerminalResult(session, captured)
			if g.conn.write.Len() != 0 || session.raidProjection.resultSent {
				t.Fatal("foreign/mismatched result delivered", name)
			}
		})
	}
}

func TestRealPVFAntonOnlineResultTimeoutAndPartialTransport(t *testing.T) {
	f, session, start := realAntonProjectionFixture(t, 1)
	f.state.Battle.Deadline = start.Add(time.Duration(f.state.Battle.Configuration.PhaseLimitSeconds[1]) * time.Second)
	expired, errs := f.service.advanceCurrentRaidClocks(f.state.Battle.Deadline)
	if len(errs) != 0 || len(expired) != 1 {
		t.Fatal(expired, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, expired[0]); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 1, true)
	g, peer, state, at := onlineAntonTerminalFixture(t)
	if completed, errs := g.service.commitCurrentRaidWeeklyIntents(at); len(completed) != 1 || len(errs) != 0 {
		t.Fatal(completed, errs)
	}
	conn := &antonPartialWriteConn{Conn: peer.conn}
	peer.conn = conn
	if err := g.service.sendCurrentAntonTerminalResult(peer, state); err == nil || !conn.closed || !peer.raidProjection.failed || peer.raidProjection.resultSent {
		t.Fatal("partial terminal batch accepted", err)
	}
	writes := conn.calls
	if err := g.service.sendCurrentAntonTerminalResult(peer, state); err == nil || conn.calls != writes {
		t.Fatal("partial batch replayed", err)
	}
}
