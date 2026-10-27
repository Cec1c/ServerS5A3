package dnfbridge

import (
	"testing"
	"time"
)

func phase0CardItemCounts(t *testing.T, body []byte) []int {
	t.Helper()
	if len(body) < 2 || body[0] != 1 {
		t.Fatalf("unexpected phase0 party body: %x", body)
	}
	pos, count := 2, int(body[1])
	counts := make([]int, 0, count)
	for i := 0; i < count; i++ {
		if pos+4 > len(body) {
			t.Fatalf("truncated card row %d: %x", i, body)
		}
		items := int(body[pos+3])
		pos += 4
		if pos+items*8 > len(body) {
			t.Fatalf("truncated card items %d: %x", i, body)
		}
		counts = append(counts, items)
		pos += items * 8
	}
	if pos != len(body) {
		t.Fatalf("card list trailing bytes: parsed=%d body=%x", pos, body)
	}
	return counts
}

func phase0AllCardsFaceDown(t *testing.T, body []byte) {
	t.Helper()
	for i, items := range phase0CardItemCounts(t, body) {
		if items != 0 {
			t.Fatalf("phase0 card %d was already revealed before its display window: %x", i, body)
		}
	}
}

// The actual card screen can arrive after the phase-completion clock. The
// automatic selection must not commit the party cards before that screen was
// delivered, and the first delivery must retain one real movie window.
func TestRealPVFAntonPhase0AutoSelectionWaitsForActualShow(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	flow.PartyShownAt = time.Time{}
	movie := currentAntonMovieWindow(flow)
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !flow.PartyShownAt.IsZero() || !flow.PartyCommittedAt.IsZero() || len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() {
		t.Fatal("automatic selection committed before any party card screen was delivered")
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("automatic selection granted a party reward before the card screen")
	}

	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	var partyBody []byte
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == 1 {
			partyBody = append([]byte(nil), packet.Body...)
		}
	}
	if partyBody == nil {
		t.Fatal("automatic party offer did not deliver a kind1 card list")
	}
	phase0AllCardsFaceDown(t, partyBody)
	if flow.PartyShownAt.IsZero() {
		t.Fatal("successful party list delivery did not anchor the selection clock")
	}
	if got, want := currentAntonPartySelectAt(flow, movie), flow.PartyShownAt.Add(movie); !got.Equal(want) {
		t.Fatalf("selection deadline=%v want actual-show window=%v", got, want)
	}

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.PartyShownAt.Add(movie-time.Nanosecond), zeroAntonRoll); len(errs) != 0 || antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("automatic party selection crossed the actual-show window", errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.PartyShownAt.Add(movie), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 30 || flow.PartyCommittedAt.IsZero() || len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() {
		t.Fatal("automatic party commit did not preserve the party stage")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.PartyCommittedAt.Add(movie), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if antonGrantedItemCount(t, f, "19", 10093974) != 19 || flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
		t.Fatal("squad stage did not follow the party display window")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(movie), zeroAntonRoll); len(errs) != 0 || flow.FinishedAt.IsZero() || !flow.BreakUntil.Equal(flow.FinishedAt.Add(time.Duration(flow.Configuration.PhaseBreakSeconds)*time.Second)) {
		t.Fatal("squad display window did not finish the phase", errs)
	}
}

// A late natural 659 is the real v151 movie-completion path. It must open the
// face-down party list first and must not surface an already granted card.
func TestRealPVFAntonPhase0LateNatural659ShowsBeforeAutomaticGrant(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	flow.PartyShownAt = time.Time{}
	movie := currentAntonMovieWindow(flow)
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 0 || !flow.PartyShownAt.IsZero() {
		t.Fatal("early automatic commit replaced the natural 659 show")
	}

	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	var partyBody []byte
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.MsgID == 600 && len(packet.Body) > 0 && packet.Body[0] == 1 {
			partyBody = append([]byte(nil), packet.Body...)
		}
	}
	if partyBody == nil {
		t.Fatal("natural 659 did not deliver a kind1 card list")
	}
	phase0AllCardsFaceDown(t, partyBody)
	if flow.PartyShownAt.IsZero() || !f.state.Battle.RewardCardReady[0][19] {
		t.Fatal("natural 659 did not anchor the actual show")
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("natural 659 granted before automatic selection")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.PartyShownAt.Add(movie-time.Nanosecond), zeroAntonRoll); len(errs) != 0 || antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("late show did not retain its full display window", errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.PartyShownAt.Add(movie), zeroAntonRoll); len(errs) != 0 || antonGrantedItemCount(t, f, "19", 10304529) != 30 {
		t.Fatal("late natural 659 did not commit after its actual-show window", errs)
	}
}

func TestRealPVFAntonPhase0UnattendedSelectionHasBoundedFallback(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	flow.PartyShownAt = time.Time{}
	movie := currentAntonMovieWindow(flow)
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("unattended fallback fired before the automatic offer window", errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(movie), zeroAntonRoll); len(errs) != 0 || antonGrantedItemCount(t, f, "19", 10304529) != 30 {
		t.Fatal("unattended party selection has no bounded fallback", errs)
	}
}
