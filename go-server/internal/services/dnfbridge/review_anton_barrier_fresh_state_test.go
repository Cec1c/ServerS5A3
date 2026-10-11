package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"testing"
	"time"
)

// This is a domain-accepted barrier ledger plus actual projection sender test.
// It does not claim public death admission, native entity release, or physical UI.
func TestRealPVFReviewAntonBarrierStateSurvivesFreshAndReplacementProjection(t *testing.T) {
	for _, recharge := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("recharge%t-replace%t", recharge, replace), func(t *testing.T) {
				f, session, now := realAntonProjectionFixture(t, 1)
				b := f.state.Battle
				for n := 0; n < 4; n++ {
					next, replay, err := b.Anton.ApplyBarrierDamage(fmt.Sprintf("review-energy-%d", n), raid.AntonLargeTransmission, 218, b.Anton.Epoch(), now)
					if err != nil || replay {
						t.Fatal("accepted domain damage", n, replay, err)
					}
					b.Anton = next
				}
				if b.Anton.NativeSymbols()[110] != 0 || b.Anton.NativeSymbols()[126] != 1 {
					t.Fatal("depletion fixture")
				}
				if recharge {
					next, err := b.Anton.Tick(now.Add(time.Second))
					if err != nil {
						t.Fatal(err)
					}
					b.Anton = next
				}
				b.Dungeons = b.Anton.Dungeons()
				b.ProgressVersion++
				native := b.Anton.NativeSymbols()
				cursor := b.Anton.SymbolSequence()
				if recharge && native[110] != 17 {
					t.Fatal("charge fixture", native[110])
				}
				if replace {
					old := session
					session = f.session()
					session.loginPacketSeen, session.accountFromToken = true, true
					f.service.bindGameSessionCharacter(session, 19)
					f.conn.write.Reset()
					if err := f.service.sendCurrentRaidProgressProjection(old, cloneRuntimeRaidState(*f.state), false, true); err == nil || f.conn.write.Len() != 0 {
						t.Fatal("old owner published", err)
					}
				}
				f.conn.write.Reset()
				if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, true); err != nil {
					t.Fatal(err)
				}
				got := map[uint32]uint32{}
				reset := 0
				for _, packet := range splitAllCurrentGameServerUpperPackets(t, f.conn.write.Bytes()) {
					if packet.Header.MsgID == 587 {
						t.Fatal("restore replayed stage intro")
					}
					if packet.Header.MsgID != 583 {
						continue
					}
					for _, w := range antonProjectionWrites(t, packet.Body) {
						if w.ID == 0 {
							if w.Value != 0 {
								t.Fatal("reset malformed")
							}
							reset++
							continue
						}
						if w.ID >= 121 && w.ID <= 125 || w.ID == 128 {
							t.Fatal("AVI snapshot", w)
						}
						got[w.ID] = w.Value
					}
				}
				for _, id := range []uint32{110, 126, 127} {
					if v, ok := got[id]; !ok || v != native[id] {
						t.Fatalf("persistent symbol %d omitted/changed: got=%d present=%t want=%d", id, v, ok, native[id])
					}
				}
				if reset != 1 || b.Anton.SymbolSequence() != cursor || session.raidProjection.symbolCursor != cursor {
					t.Fatal("reset/journal/cursor", reset)
				}
				f.conn.write.Reset()
				if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil || f.conn.write.Len() != 0 {
					t.Fatal("duplicate history", err)
				}
			})
		}
	}
}
