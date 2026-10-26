package guild

import (
	"fmt"
	"testing"
	"time"
)

func TestDungeonDefaultCalendarMatchesNativeSixAM(t *testing.T) {
	var policy *DungeonPolicy
	zone := time.FixedZone("UTC+8", 8*3600)
	for _, ym := range [][2]int{{2024, 3}, {2026, 11}, {2027, 1}} {
		boundary := time.Date(ym[0], time.Month(ym[1]), 1, 6, 0, 0, 0, zone)
		for _, delta := range []time.Duration{-time.Second, 0, time.Second} {
			at := boundary.Add(delta)
			cycle, err := policy.Cycle(at)
			if err != nil {
				t.Fatal(err)
			}
			want := boundary
			if delta < 0 {
				want = want.AddDate(0, -1, 0)
			}
			if int64(cycle.StartUnix) != want.Unix() || int64(cycle.EndUnix) != want.AddDate(0, 1, 0).Unix() {
				t.Fatalf("at=%v cycle=%+v", at, cycle)
			}
			if cycle.Units != 1 {
				t.Fatal(cycle)
			}
		}
	}
}

func TestDungeonExplicitLegacyMidnightPolicyRetainsKey(t *testing.T) {
	p := &DungeonPolicy{ResetDay: 1, UTCOffsetMinutes: 0, ClearUnitsPerMember: 1}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cycle, err := p.Cycle(start.Add(time.Hour))
	if err != nil || cycle.Key != fmt.Sprintf("v1:%d:1:0:1", start.Unix()) {
		t.Fatal(cycle, err)
	}
	for _, hour := range []int{-1, 24} {
		p.ResetHour = hour
		if _, err = p.Cycle(start); err == nil {
			t.Fatal("invalid hour accepted")
		}
	}
}
