package dnfbridge

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// r202FinishedFirstWire records the display order of one projection so the
// test can prove that the first post-finish projection opens state4 exactly
// once, before any reward list, and never re-opens it after a list.
type r202FinishedFirstWire struct {
	sequence []string
	state4   int
	after600 int
}

func r202ReadFinishedFirstWire(t *testing.T, stream []byte) r202FinishedFirstWire {
	t.Helper()
	var got r202FinishedFirstWire
	seen600 := false
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.Classification != 0 {
			continue
		}
		switch packet.Header.MsgID {
		case 587:
			if len(packet.Body) == 2 && packet.Body[0] == 4 && packet.Body[1] == 0 {
				got.sequence = append(got.sequence, "state4")
				got.state4++
				if seen600 {
					got.after600++
				}
			}
		case 600:
			seen600 = true
			if len(packet.Body) > 0 {
				got.sequence = append(got.sequence, fmt.Sprintf("kind%d", packet.Body[0]))
			}
		}
	}
	return got
}

func r202SingleAssetCounts(t *testing.T, f *raidStartGuardFixture, items []uint32) map[uint32]int64 {
	t.Helper()
	out := make(map[uint32]int64, len(items))
	for _, id := range items {
		out[id] = antonGrantedItemCount(t, f, "19", id)
	}
	return out
}

func r202WantFinishedFirstProjection(t *testing.T, label string, stream []byte) {
	t.Helper()
	got := r202ReadFinishedFirstWire(t, stream)
	if got.state4 != 1 {
		t.Fatalf("%s: state4 count=%d sequence=%v, want exactly one", label, got.state4, got.sequence)
	}
	if len(got.sequence) < 2 || got.sequence[0] != "state4" {
		t.Fatalf("%s: first display is not state4: %v", label, got.sequence)
	}
	if got.after600 != 0 {
		t.Fatalf("%s: state4 reopened after a reward list: %v", label, got.sequence)
	}
	if !reflect.DeepEqual(got.sequence, []string{"state4", "kind3"}) {
		t.Fatalf("%s: single final display sequence=%v, want state4 followed by kind3 exactly once", label, got.sequence)
	}
}

// The receiver must not have opened the reward screen before the flow finished;
// otherwise the assertion would be satisfied by the already-sent guard rather
// than by the finished-first projection path.
func r202RequireUnopenedReceiver(t *testing.T, session *gameSession) {
	t.Helper()
	session.raidProjection.mu.Lock()
	sent := session.raidProjection.rewardScreenSent
	session.raidProjection.mu.Unlock()
	if sent {
		t.Fatal("receiver already saw state4 before the first finished projection")
	}
}

func TestR202SingleAntonFinishedFirstProjectionOpensState4Once(t *testing.T) {
	f, session, at := singleAntonCompletedRewardFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil || !flow.Single || flow.Mode != 1 {
		t.Fatalf("unexpected single Anton flow: %+v", flow)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatalf("result escaped before weekly: %v", err)
	}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatalf("weekly commit: done=%d errs=%v", len(done), errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	assets := []uint32{10304529, 10093974}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), func(int64) (int64, error) {
		t.Fatal("single Anton receipt rerolled")
		return 0, nil
	}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if flow.FinishedAt.IsZero() {
		t.Fatal("single Anton flow did not finish")
	}
	if !flow.BreakUntil.IsZero() {
		t.Fatal("single Anton entered the normal phase break")
	}
	before := r202SingleAssetCounts(t, f, assets)

	f.conn.write.Reset()
	r202RequireUnopenedReceiver(t, session)
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	r202WantFinishedFirstProjection(t, "single Anton", f.conn.write.Bytes())

	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatalf("repeat projection was not silent: err=%v bytes=%d", err, f.conn.write.Len())
	}
	if after := r202SingleAssetCounts(t, f, assets); !reflect.DeepEqual(before, after) {
		t.Fatalf("projection changed committed assets: before=%v after=%v", before, after)
	}
}

func TestR202SingleLukeFinishedFirstProjectionOpensState4Once(t *testing.T) {
	f, session, at := singleLukeCompletedRewardFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil || !flow.Single || flow.Mode != 2 {
		t.Fatalf("unexpected single Luke flow: %+v", flow)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatalf("result escaped before weekly: %v", err)
	}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatalf("weekly commit: done=%d errs=%v", len(done), errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	assets := []uint32{10304529, 10159143, 10159623}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Minute), func(int64) (int64, error) {
		t.Fatal("single Luke receipt rerolled")
		return 0, nil
	}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if flow.FinishedAt.IsZero() {
		t.Fatal("single Luke flow did not finish")
	}
	if !flow.BreakUntil.IsZero() {
		t.Fatal("single Luke entered the normal phase break")
	}
	before := r202SingleAssetCounts(t, f, assets)

	f.conn.write.Reset()
	r202RequireUnopenedReceiver(t, session)
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	r202WantFinishedFirstProjection(t, "single Luke", f.conn.write.Bytes())

	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatalf("repeat projection was not silent: err=%v bytes=%d", err, f.conn.write.Len())
	}
	if after := r202SingleAssetCounts(t, f, assets); !reflect.DeepEqual(before, after) {
		t.Fatalf("projection changed committed assets: before=%v after=%v", before, after)
	}
}
