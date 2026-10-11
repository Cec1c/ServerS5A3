package dnfbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

func TestRealPVFAntonFreshAndRestoredProjectionAvoidsZeroMovieCallbacks(t *testing.T) {
	for _, phase := range []int{0, 1} {
		for _, restore := range []bool{false, true} {
			t.Run(fmt.Sprintf("phase%d-restore%t", phase, restore), func(t *testing.T) {
				f, session, now := realAntonProjectionFixture(t, phase)
				b := f.state.Battle
				if restore && phase == 1 {
					next, err := b.Anton.Tick(now.Add(180 * time.Second))
					if err != nil {
						t.Fatal(err)
					}
					b.Anton, b.Dungeons, b.ProgressVersion = next, next.Dungeons(), 1
				}
				var err error
				if restore {
					err = f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false)
				} else {
					err = f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state))
				}
				if err != nil {
					t.Fatal(err)
				}
				stream := append([]byte(nil), f.conn.write.Bytes()...)
				var writes []raid.SymbolValue
				var starts [][]byte
				for len(stream) > 0 {
					packet, rest := splitGameServerUpperPacket(t, stream)
					stream = rest
					if packet.Header.MsgID == 583 {
						writes = append(writes, antonProjectionWrites(t, packet.Body)...)
					}
					if packet.Header.MsgID == 587 {
						starts = append(starts, append([]byte(nil), packet.Body...))
					}
				}
				if dir := os.Getenv("DNF90_RAID_MOVIE_NATIVE_DIR"); dir != "" {
					payload, _ := json.MarshalIndent(map[string]any{"phase": phase, "restore": restore, "symbols": writes, "starts": starts}, "", "  ")
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("phase%d-restore%t.json", phase, restore)), payload, 0600); err != nil {
						t.Fatal(err)
					}
				}
				for _, w := range writes {
					// Actual82e0 dispatch ignores value for these movie instructions;
					// writing zero is still a video-loader request. Live nonzero writes
					// remain permitted and are tested independently through the journal.
					if (w.ID >= 121 && w.ID <= 125 || w.ID == 128) && (restore || w.Value == 0) {
						t.Errorf("snapshot emits AVI callback id%d value%d", w.ID, w.Value)
					}
				}
				if restore && len(starts) != 0 {
					t.Fatal("restoration replayed stage intro", starts)
				}
				if !restore && (len(starts) != 1 || len(starts[0]) != 2 || starts[0][0] != 2 || starts[0][1] != byte(phase)) {
					t.Fatal("correct owned phase intro changed", starts)
				}
			})
		}
	}
}

func TestRealPVFAntonOwnedHatcheryMovieStillProjectsExactlyOnce(t *testing.T) {
	f, session, now := realAntonProjectionFixture(t, 1)
	if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	b := f.state.Battle
	next, err := b.Anton.Tick(now.Add(180 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b.Anton, b.Dungeons = next, next.Dungeons()
	b.ProgressVersion++
	if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
		t.Fatal(err)
	}
	stream := append([]byte(nil), f.conn.write.Bytes()...)
	count := 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.MsgID != 583 {
			continue
		}
		for _, w := range antonProjectionWrites(t, packet.Body) {
			if w.ID == 128 {
				if w.Value != 1 {
					t.Fatal("owned live hatchery movie changed", w)
				}
				count++
			}
		}
	}
	if count != 1 {
		t.Fatal("owned live hatchery movie missing or duplicated", count)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("consumed live movie was replayed", err, f.conn.write.Len())
	}
}
