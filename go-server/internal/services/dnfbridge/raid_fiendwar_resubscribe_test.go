package dnfbridge

import "testing"

func TestRealPVFFiendWarResubscribeRestoresFullBoardWithoutStartReplay(t *testing.T) {
	f, peers := r121CorpusFixture(t, 2)
	p := peers[0]
	if err := f.service.sendCurrentRaidBattleState(p, *f.state); err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := f.service.sendCurrentRaidProgressProjection(p, *f.state, false, true); err != nil {
		t.Fatal(err)
	}
	packets := totalTestPackets(t, p)
	init, users := 0, 0
	for _, packet := range packets {
		switch packet.Header.MsgID {
		case currentFiendWarInit:
			init++
		case currentFiendWarUser:
			users += int(packet.Body[0])
		case 587, currentFiendWarStateNotice:
			t.Fatalf("restore replayed start transition: %d %x", packet.Header.MsgID, packet.Body)
		}
	}
	if init != 1 || users != len(f.state.Members) {
		t.Fatalf("restore did not send full board: init=%d users=%d members=%d", init, users, len(f.state.Members))
	}
}

func TestRealPVFFiendWarRepeatedBattleStateDoesNotRestartUI(t *testing.T) {
	f, peers := r121CorpusFixture(t, 1)
	p := peers[0]
	if err := f.service.sendCurrentRaidBattleState(p, *f.state); err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := f.service.sendCurrentRaidBattleState(p, *f.state); err != nil {
		t.Fatal(err)
	}
	for _, packet := range totalTestPackets(t, p) {
		if packet.Header.MsgID == 587 || packet.Header.MsgID == currentFiendWarStateNotice || packet.Header.MsgID == currentFiendWarInit {
			t.Fatalf("same battle restarted UI: %d", packet.Header.MsgID)
		}
	}
}

func TestRealPVFFiendWarTerminalResubscribeDoesNotOpenActiveBoard(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f, peers := r121CorpusFixture(t, 1)
		f.state.Battle.TimedOut = failed
		f.state.Battle.PhaseCompleted = !failed
		p := peers[0]
		if err := f.service.sendCurrentRaidProgressProjection(p, *f.state, false, true); err != nil {
			t.Fatal(err)
		}
		for _, packet := range totalTestPackets(t, p) {
			if packet.Header.MsgID == currentFiendWarInit || packet.Header.MsgID == 587 || packet.Header.MsgID == currentFiendWarStateNotice {
				t.Fatalf("terminal snapshot reopened active UI: failed=%t opcode=%d", failed, packet.Header.MsgID)
			}
		}
	}
}
