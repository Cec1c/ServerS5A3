package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Real PVF layouts and the existing owned Luke admission/reducer fixture. UI,
// socket and phase-start setup remain fixtures; return authorization and the
// packet handler, typed scene route and positive confirmation execute normally.
func TestR195RealLukeTableActiveReturn(t *testing.T) {
	for _, dungeonID := range []uint32{5054, 5311, 5063, 5066} {
		t.Run(fmt.Sprint(dungeonID), func(t *testing.T) {
			f, p, r, _, _ := reviewActualLukeCoinFixtureForDungeon(t, false, dungeonID)
			s := f.service
			s.recordCurrentRaidParticipation(p, r)
			if !s.currentRaidTableActiveReturnReady(p, r) {
				t.Fatal("real active Luke fixture has no accepted participation")
			}
			before, _, err := f.repos.Inventory.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			b := f.state.Battle
			clears := len(b.ClearedRuns)
			memberClears := len(b.MemberClears)
			phaseCoins := b.PhaseCoins
			for _, body := range [][]byte{{2, 2}, {1, 0}, {1, 1}, {1, 3}} {
				f.conn.write.Reset()
				if err := s.handleGameCommand(p, 1, 72, body); err != nil {
					t.Fatal(err)
				}
				if r.townReturnPending || f.conn.write.Len() != 0 {
					t.Fatalf("non-town action admitted: %x", body)
				}
			}
			// A stale epoch must still fail before any ACK or transition.
			r.raidEntry.Attempt++
			if err := s.handleGameCommand(p, 1, 72, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			if r.townReturnPending || f.conn.write.Len() != 0 {
				t.Fatal("stale Luke entry admitted")
			}
			r.raidEntry.Attempt--
			if dungeonID == 5066 {
				// The actual graph hides energy-save when its throne owner
				// withdraws, but this already-entered energy run remains owned.
				next, err := (&raid.LukeProgression{AntonProgression: b.Anton}).GiveUpDungeon("r195-history-throne", 5061, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				b.Anton, b.Dungeons = next.AntonProgression, next.Dungeons()
				if err := s.validateCurrentRaidDungeonEntry(p, r); err == nil {
					t.Fatal("hidden energy map must still reject new admission")
				}
				if !s.currentRaidTableActiveReturnReady(p, r) {
					t.Fatal("owned hidden energy run cannot withdraw after throne returns")
				}
			}
			for repeat := 0; repeat < 2; repeat++ {
				if err := s.handleGameCommand(p, 1, 72, []byte{1, 2}); err != nil {
					t.Fatal(err)
				}
			}
			ack, scene := 0, 0
			for stream := f.conn.write.Bytes(); len(stream) > 0; {
				packet, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				if packet.Header.MsgID == 72 && packet.Header.Classification == 1 {
					ack++
					if !reflect.DeepEqual(packet.Body, []byte{1, 1, 2}) {
						t.Fatalf("wrong table ACK: %x", packet.Body)
					}
				}
				if packet.Header.MsgID == 24 && packet.Header.Classification == 0 {
					scene++
				}
			}
			if ack != 1 || scene != 1 || !r.townReturnPending || !r.townReturnOp24Sent || r.townReturnRequestMsgID != 72 || p.dungeon.runtime != r || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
				t.Fatalf("Luke route missing/replayed/retired early: ack=%d scene=%d pending=%v typed=%v same=%v status=%v", ack, scene, r.townReturnPending, r.townReturnOp24Sent, p.dungeon.runtime == r, r.Session.Snapshot().Run.Status)
			}
			if err := s.sendEnterSelectDungeonState(p, "upper_15", false, true); err != nil {
				t.Fatal(err)
			}
			if p.dungeon.runtime != nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
				t.Fatal("positive town confirmation did not retire owned run")
			}
			after, _, err := f.repos.Inventory.Load(context.Background(), "19")
			if err != nil || !reflect.DeepEqual(before, after) || len(b.ClearedRuns) != clears || len(b.MemberClears) != memberClears || b.PhaseCoins != phaseCoins {
				t.Fatal("active retreat changed reward, clear journal, inventory or coin quota", err)
			}
		})
	}
}
