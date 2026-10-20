package dnfbridge

import (
	"bytes"
	"testing"
	"time"

	dnfjoust "longheng.io/server/internal/modules/dnf/joust"
)

// Live evidence (packet_log 2026-09-24, conns game-000003 account dnf:10001 and
// game-000006 account dnf:10005): the class-0/op1240 body was byte-identical for
// both accounts in the same round. The current client raises its "betting has
// started" notification from this state, so the state push is a global timeline
// fact and must stay account-independent: the builder takes no session,
// character, party, channel or ledger input. This regression pins that property
// so no later change can turn the tournament state into a per-account flag that
// blocks one account's town operations.
func TestJoustOpeningStateIsTimelineDerivedAndAccountIndependent(t *testing.T) {
	// The body is exactly (u16 round little-endian, u8 state), so the expectation
	// is derived from the round instead of being copied from one live capture.
	for _, round := range []uint16{0, 1, 0x00ff, 0x0100, 0xcb47, 0xffff} {
		want := []byte{byte(round), byte(round >> 8), 1}
		if got := buildCurrentJoustOpeningState(round); !bytes.Equal(got, want) {
			t.Fatalf("op1240 opening state for round %d=%x want=%x", round, got, want)
		}
		if got := buildCurrentJoustState(dnfjoust.Timeline{Round: round, State: 1}); !bytes.Equal(got, want) {
			t.Fatalf("op1240 state for round %d=%x want=%x", round, got, want)
		}
	}
	// The captured 2026-09-24 body was `…0047cb01`: round 0xCB47 (= 52039), state
	// 1. Written in the hex form the packet log prints so the mapping to the
	// evidence file stays explicit.
	captured := buildCurrentJoustOpeningState(0xcb47)
	if !bytes.Equal(captured, []byte{0x47, 0xcb, 0x01}) {
		t.Fatalf("captured op1240 body=%x want=47cb01 (round 0xCB47, state 1)", captured)
	}
}

// The state byte follows the timeline phase (1 betting, 2 quarter/semi final,
// 3 final, 4 settled) and is never a fixed "in tournament" value: the push is
// (u16 round, u8 state) and nothing else.
func TestJoustStatePushCarriesTimelinePhaseOnly(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	for _, testCase := range []struct {
		name   string
		offset time.Duration
		phase  dnfjoust.Phase
		state  byte
	}{
		{name: "betting", offset: 0, phase: dnfjoust.PhaseBetting, state: 1},
		{name: "quarter_final", offset: dnfjoust.BettingDuration, phase: dnfjoust.PhaseQuarterFinal, state: 2},
		{name: "semi_final", offset: dnfjoust.QuarterFinalEnd, phase: dnfjoust.PhaseSemiFinal, state: 2},
		{name: "final", offset: dnfjoust.SemiFinalEnd, phase: dnfjoust.PhaseFinal, state: 3},
		{name: "settled", offset: dnfjoust.SettlementStart, phase: dnfjoust.PhaseSettled, state: 4},
	} {
		timeline := dnfjoust.TimelineAt(base.Add(testCase.offset))
		if timeline.Phase != testCase.phase || timeline.State != testCase.state {
			t.Fatalf("%s: timeline phase=%d state=%d want %d/%d",
				testCase.name, timeline.Phase, timeline.State, testCase.phase, testCase.state)
		}
		body := buildCurrentJoustState(timeline)
		if len(body) != 3 {
			t.Fatalf("%s: op1240 body=%x want 3 bytes (u16 round, u8 state)", testCase.name, body)
		}
		if !bytes.Equal(body, []byte{0x00, 0x00, testCase.state}) {
			t.Fatalf("%s: op1240 body=%x want round 0 state %d", testCase.name, body, testCase.state)
		}
		// Same timeline in, same bytes out: no hidden per-session input.
		if !bytes.Equal(body, buildCurrentJoustState(timeline)) {
			t.Fatalf("%s: op1240 body is not a pure function of the timeline", testCase.name)
		}
	}
}
