package dnfbridge

import (
	"bytes"
	"testing"
	"time"
)

func TestRealPVFLukeManualFlipFinishDoesNotRestartAnimation(t *testing.T) {
	f, session, at := normalLukeFixture(t)
	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, session, id, at)
			at = at.Add(time.Second)
		}
	}
	flow := f.state.Battle.RewardFlow[0]
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	sawCards := false
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		p, rest := splitGameServerUpperPacket(t, stream)
		sawCards = sawCards || p.Header.MsgID == 600
		stream = rest
	}
	if !sawCards {
		t.Fatal("manual flip omitted card list")
	}
	for _, now := range []time.Time{flow.AutoSelectAt, flow.AutoSelectAt.Add(65 * time.Second)} {
		if _, errs := f.service.advanceCurrentAntonRewardFlows(now, zeroAntonRoll); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	state = cloneRuntimeRaidState(*f.state)
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	sawBreak, sawCompletion := false, false
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		p, rest := splitGameServerUpperPacket(t, stream)
		if p.Header.MsgID == 587 && len(p.Body) == 2 && p.Body[0] == 4 {
			t.Fatal("finish restarted already delivered reward screen")
		}
		if p.Header.MsgID == 600 {
			if p.Header.Classification != 0 || !bytes.Equal(p.Body, []byte{3, 0}) || sawCompletion || sawBreak {
				t.Fatal("finish replayed cards or sent duplicate/late completion", p.Body)
			}
			sawCompletion = true
		}
		sawBreak = sawBreak || p.Header.MsgID == 587 && len(p.Body) == 2 && p.Body[0] == 5
		stream = rest
	}
	if !sawBreak || !sawCompletion {
		t.Fatal("finish omitted completion or standby transition", sawCompletion, sawBreak)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatalf("repeat transition bytes=%d err=%v", f.conn.write.Len(), err)
	}
}
