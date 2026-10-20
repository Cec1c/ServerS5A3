package raid

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRealPVFAntonDungeonTimerNativeFixtures(t *testing.T) {
	p := realAntonProgram(t)
	now := time.Unix(1791329738, 0)
	s, err := p.BeginPhase(0, now, 42)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		s, _, err = s.ClearDungeon("timer-smoke-"+strconv.Itoa(i), 210, s.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Actual quake timer, smoke recovery timer, and phase1 hatchery opening
	// timer are configured by the public PVF; no invented live dungeon data.
	phase1, err := p.BeginPhase(1, now, 42)
	if err != nil {
		t.Fatal(err)
	}
	actual := []struct {
		key   AntonTimerKey
		timer AntonTimer
	}{}
	for _, candidate := range []struct {
		state *AntonProgression
		key   AntonTimerKey
	}{
		{s, AntonTimerKey{Kind: 1, DungeonID: 212}},
		{s, AntonTimerKey{Kind: 2, DungeonID: 211}},
		{phase1, AntonTimerKey{Kind: 3, DungeonID: 219}},
	} {
		timer, ok := candidate.state.Timers()[candidate.key]
		if !ok || timer.Duration == 0 {
			t.Fatal("actual configured timer missing", candidate.key)
		}
		actual = append(actual, struct {
			key   AntonTimerKey
			timer AntonTimer
		}{candidate.key, timer})
	}
	for _, v := range actual {
		start := BuildTimerBody(v.key.Kind, v.key.DungeonID, uint32(v.timer.End.Unix()), v.timer.Duration)
		reset := BuildTimerBody(v.key.Kind, v.key.DungeonID, 0, 0)
		if len(start) != 13 || binary.LittleEndian.Uint32(start[1:5]) != v.key.DungeonID || binary.LittleEndian.Uint32(reset[5:9]) != 0 || binary.LittleEndian.Uint32(reset[9:13]) != 0 {
			t.Fatal("native timer encoding")
		}
		if dir := os.Getenv("DNFBRIDGE_NATIVE_FIXTURE_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			for operation, body := range map[string][]byte{"start": start, "reset": reset} {
				if err := os.WriteFile(filepath.Join(dir, "anton-"+strconv.Itoa(int(v.key.Kind))+"-"+operation+"-timer.bin"), body, 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
