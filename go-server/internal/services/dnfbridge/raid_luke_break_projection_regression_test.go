package dnfbridge

import (
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func lukeDisplayAssetSnapshot(t *testing.T, s *Service, session *gameSession) []any {
	t.Helper()
	assets := rejoinAssetSnapshot(t, s, session)
	// Existing container refresh persists its normalization timestamp even
	// when contents are equal. Keep all item/quantity/receipt fields exact.
	inventory := assets[2].(repo.InventoryRecord)
	inventory.UpdatedAt = time.Time{}
	for key, item := range inventory.Slots {
		// RawEntry is the container projection cache regenerated for a fresh
		// connection. Semantic item identity/count/binding/expiry stay exact.
		item.RawEntry = nil
		inventory.Slots[key] = item
	}
	assets[2] = inventory
	return assets
}

// Actual PVF reducers and production sender; no GUI or physical admission claim.
// A changing remaining-duration clock must not replay an already delivered
// phase-break transition or refresh all inventory containers on every tick.
func TestRealPVFLukeFinishedBreakProjectionDoesNotReplayAsClockElapses(t *testing.T) {
	f, session, at := normalLukeFixture(t)
	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, session, id, at)
			at = at.Add(time.Second)
		}
	}
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil || flow.squadRequired() {
		t.Fatal("actual Luke first-phase flow missing")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(65*time.Second), zeroAntonRoll); len(errs) != 0 || flow.FinishedAt.IsZero() {
		t.Fatal("actual flow did not finish", errs)
	}
	// Keep the standby timer live at the projection boundary; accepted grants
	// and finish state above are supplied by actual reducers.
	flow.BreakUntil = time.Now().Add(5 * time.Minute)
	state := cloneRuntimeRaidState(*f.state)
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("reward screen bypassed result delivery", err)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	sawBreak, sawClock, sawCards := false, false, false
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		p, rest := splitGameServerUpperPacket(t, stream)
		if p.Header.MsgID == 587 && len(p.Body) == 2 && p.Body[0] == 5 {
			sawBreak = true
		}
		if p.Header.MsgID == 596 {
			sawClock = true
		}
		if p.Header.MsgID == 600 {
			sawCards = true
		}
		stream = rest
	}
	if !sawBreak || !sawClock || !sawCards {
		t.Fatal("first restore incomplete", sawBreak, sawClock, sawCards)
	}
	// The existing container sender initializes missing fixture containers on
	// its first send. Compare subsequent repeats/reconnects with that boundary.
	assets := lukeDisplayAssetSnapshot(t, f.service, session)
	f.conn.write.Reset()
	// First repeat exercises the display-filter signature; the elapsed repeat
	// independently exercises Luke's relative clock representation.
	for i := 0; i < 2; i++ {
		if i == 1 {
			time.Sleep(1100 * time.Millisecond)
		}
		if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
			t.Fatal(err)
		}
		if f.conn.write.Len() != 0 {
			t.Fatalf("repeat %d replayed finished break/cards/inventory: %d bytes", i, f.conn.write.Len())
		}
	}
	if after := lukeDisplayAssetSnapshot(t, f.service, session); !reflect.DeepEqual(assets, after) {
		t.Fatal("repeated display altered committed assets")
	}
	replacement := f.session()
	replacement.channel, replacement.loginPacketSeen, replacement.accountFromToken = session.channel, true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	// Binding has its own last-online-character bookkeeping. The display must
	// preserve the repository snapshot from after that independent boundary.
	assets = lukeDisplayAssetSnapshot(t, f.service, replacement)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil || f.conn.write.Len() == 0 {
		t.Fatal("replacement lost current restore", err)
	}
	if after := lukeDisplayAssetSnapshot(t, f.service, replacement); !reflect.DeepEqual(assets, after) {
		t.Fatal("display/reconnect altered committed asset contents or receipts")
	}
	f.conn.write.Reset()
	// A genuinely changed end remains observable; only elapsed duration was
	// normalized, not the authoritative clock identity.
	flow.BreakUntil = flow.BreakUntil.Add(time.Minute)
	state = cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	sawClock = false
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		p, rest := splitGameServerUpperPacket(t, stream)
		if p.Header.MsgID == 600 {
			t.Fatal("deadline change replayed committed cards")
		}
		if p.Header.MsgID == 596 {
			sawClock = true
		}
		stream = rest
	}
	if !sawClock {
		t.Fatal("changed deadline was suppressed")
	}
}
