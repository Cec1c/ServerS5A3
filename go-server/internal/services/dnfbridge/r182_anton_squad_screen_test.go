package dnfbridge

import (
	"testing"
	"time"
)

// The v151 kind3 consumer switches the already-open reward panel itself.
// State4 belongs to the connection's first display, including reconnects.
func TestR182AntonPhase0SquadRetainsOpenScreenAndRestoresNewConnection(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	r182AssertRewardPresentation(t, f.conn.write.Bytes(), 1, 1)
	var movie time.Duration
	for _, seconds := range flow.Configuration.MovieEndSeconds {
		movie = max(movie, time.Duration(seconds)*time.Second)
	}
	for _, at := range []time.Time{flow.AutoSelectAt} {
		if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	if flow.SquadShownAt.IsZero() {
		t.Fatal("real phase0 squad rewards were not committed")
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304529) != 30 || antonGrantedItemCount(t, f, id, 10093974) != 19 {
			t.Fatal("actual PVF party/squad entitlement changed", id)
		}
	}
	f.conn.write.Reset()
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	r182AssertRewardPresentation(t, f.conn.write.Bytes(), 0, 3)
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	publicP14NoCardRestart(t, f.conn.write.Bytes(), "unchanged phase0 squad")

	replacement := f.session()
	conn := &bufferConn{}
	replacement.conn, replacement.loginPacketSeen, replacement.accountFromToken = conn, true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	r182AssertRewardPresentation(t, conn.write.Bytes(), 1, 3)
	conn.write.Reset()
	if err := f.service.handleCurrentAntonMovieCompletion(replacement, 659, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(movie), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	publicP14NoCardRestart(t, conn.write.Bytes(), "restored squad late659 and standby")
	if antonGrantedItemCount(t, f, "19", 10304529) != 30 || antonGrantedItemCount(t, f, "19", 10093974) != 19 {
		t.Fatal("reconnect duplicated rewards")
	}
}

func r182AssertRewardPresentation(t *testing.T, stream []byte, wantScreens int, kind byte) {
	t.Helper()
	screens, lists := 0, 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.Classification != 0 {
			continue
		}
		if packet.Header.MsgID == 587 && len(packet.Body) == 2 && packet.Body[0] == 4 && packet.Body[1] == 0 {
			screens++
		}
		if packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == kind {
			if screens != wantScreens {
				t.Fatalf("kind%d arrived after %d reward-screen opens; want %d", kind, screens, wantScreens)
			}
			lists++
		}
	}
	if screens != wantScreens || lists != 1 {
		t.Fatalf("reward presentation screens=%d lists(kind%d)=%d, want %d/1", screens, kind, lists, wantScreens)
	}
}
