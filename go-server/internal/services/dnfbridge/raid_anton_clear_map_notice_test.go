package dnfbridge

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Use the authenticated sender and actual cumulative map snapshots. Native
// 01878400 announces any received state 3, including an unchanged old clear.
func TestAntonMapClearAnnouncementsDoNotReplayHistory(t *testing.T) {
	for _, phase := range []byte{0, 1} {
		t.Run(strconv.Itoa(int(phase)), func(t *testing.T) {
			f := newR195MapProjectionFixture(t, 1)
			b := f.state.Battle
			b.Phase = phase
			b.Dungeons = []raid.DungeonState{{ID: 210}, {ID: 213}, {ID: 215}}
			f.send(t)
			if maps := r195Map585(f.drain(t)); len(maps) != 1 || maps[0][1] != 3 {
				t.Fatal("missing initial full snapshot", maps)
			}
			for index := range b.Dungeons {
				b.Dungeons[index].State = 3
				b.ProgressVersion++
				f.project(t, false, false)
				maps := r195Map585(f.drain(t))
				for _, body := range maps {
					t.Logf("native585=%x", body)
				}
				if len(maps) != 1 {
					t.Fatal("clear update count", len(maps))
				}
				rows, _, err := currentRaidMapProjectionRows(maps[0])
				if err != nil || len(rows) != 1 || rows[0] != b.Dungeons[index] {
					t.Fatalf("new clear %d replayed history: rows=%v err=%v", index, rows, err)
				}
			}
			// Timers/counters may advance without a map edge.
			b.ProgressVersion++
			f.project(t, false, false)
			if maps := r195Map585(f.drain(t)); len(maps) != 0 {
				t.Fatal("unrelated update replayed clears", maps)
			}
			// A new run of the same dungeon must still get a new clear edge.
			for _, state := range []byte{0, 3} {
				b.Dungeons[0].State = state
				b.ProgressVersion++
				f.project(t, false, false)
				maps := r195Map585(f.drain(t))
				if len(maps) != 1 || maps[0][1] != 1 || maps[0][6] != state {
					t.Fatal("same dungeon new run lost its transition", maps)
				}
			}
			// Restore full state once, then seed the same subscriber's delta.
			f.project(t, false, true)
			if maps := r195Map585(f.drain(t)); len(maps) != 1 || maps[0][1] != 3 {
				t.Fatal("resubscribe lost full map", maps)
			}
			b.ProgressVersion++
			f.project(t, false, false)
			if maps := r195Map585(f.drain(t)); len(maps) != 0 {
				t.Fatal("post-restore update replayed clear history", maps)
			}
		})
	}
}

func TestRealPVFAntonMapClearAnnouncementsFollowOnlyChangedRows(t *testing.T) {
	f, session, now := realAntonProjectionFixture(t, 0)
	b := f.state.Battle
	if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	previous := map[uint32]byte{}
	for _, row := range b.Dungeons {
		previous[row.ID] = row.State
	}
	var frames []string
	sawClear := false
	for index, id := range []uint32{210, 210, 210, 210, 213, 213, 215, 215} {
		next, _, err := b.Anton.ClearDungeon("notice-"+strconv.Itoa(index), id, b.Anton.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
		b.Anton, b.Dungeons, b.ProgressVersion = next, next.Dungeons(), b.ProgressVersion+1
		if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
			t.Fatal(err)
		}
		stream := append([]byte(nil), f.conn.write.Bytes()...)
		f.conn.write.Reset()
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			stream = rest
			if packet.Header.MsgID != 585 {
				continue
			}
			frames = append(frames, hex.EncodeToString(packet.Body))
			t.Logf("native585=%x", packet.Body)
			rows, _, err := currentRaidMapProjectionRows(packet.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if previous[row.ID] == row.State {
					t.Fatalf("accepted clear %d replayed unchanged row %+v", index, row)
				}
			}
		}
		for _, row := range b.Dungeons {
			previous[row.ID] = row.State
			if row.State == 3 {
				sawClear = true
			}
		}
		// Actual PVF may hide completed rows when the whole phase finishes.
		// Exercise an unrelated version bump while a clear is still retained,
		// rather than assuming that final phase state must keep it forever.
		b.ProgressVersion++
		if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
			t.Fatal(err)
		}
		stream = append([]byte(nil), f.conn.write.Bytes()...)
		f.conn.write.Reset()
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			stream = rest
			if packet.Header.MsgID == 585 {
				t.Fatalf("counter-only update replayed map after accepted clear %d: %x", index, packet.Body)
			}
		}
	}
	if !sawClear || len(frames) == 0 {
		t.Fatalf("fixture never published an accepted clear: sawClear=%v frames=%d", sawClear, len(frames))
	}
	if dir := os.Getenv("DNF90_ANTON_NOTICE_NATIVE_DIR"); dir != "" {
		data, err := json.MarshalIndent(frames, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "actual-sender-585.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
