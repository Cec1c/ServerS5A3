package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemusage"
	"longheng.io/server/internal/modules/dnf/premium"
)

func decodeItemUsagePages(t *testing.T, bodies [][]byte) (map[uint32]byte, map[uint32]byte) {
	t.Helper()
	daily, weekly := map[uint32]byte{}, map[uint32]byte{}
	for _, body := range bodies {
		at := 0
		for _, target := range []*map[uint32]byte{&daily, &weekly} {
			if at >= len(body) {
				t.Fatal("missing count")
			}
			n := int(body[at])
			at++
			if n == 0 {
				*target = map[uint32]byte{}
			}
			for i := 0; i < n; i++ {
				if at+5 > len(body) {
					t.Fatal("short row")
				}
				(*target)[binary.LittleEndian.Uint32(body[at:at+4])] = body[at+4]
				at += 5
			}
		}
		if at != len(body) {
			t.Fatal("trailing bytes")
		}
	}
	return daily, weekly
}

func TestItemUsagePagesPreserveShorterMap(t *testing.T) {
	for _, shape := range [][2]int{{0, 0}, {256, 1}, {1, 256}, {510, 0}, {0, 511}, {255, 255}} {
		d, w := map[uint32]byte{}, map[uint32]byte{}
		for i := 1; i <= shape[0]; i++ {
			d[uint32(i)] = byte(i % 256)
		}
		for i := 1; i <= shape[1]; i++ {
			w[uint32(i)] = byte(i % 7)
		}
		gotD, gotW := decodeItemUsagePages(t, encodeCurrentItemUsageBodies(d, w))
		if !reflect.DeepEqual(d, gotD) || !reflect.DeepEqual(w, gotW) {
			t.Fatalf("pages cleared prior rows: %v", shape)
		}
	}
}

func TestItemUsageCombinesFatigueDailyAndWeeklyBoxes(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	catalog, source := fatiguePotionUsageTestDefinitions(416, 500)
	source[catalog[500].PVFPath] = "[stackable type]\n`[booster]`\n[total usable count]\n1\n[total weekly usable count]\n2\n[total weekly usable count reset day]\n4\n"
	c, inv := fatiguePotionUsageTestPlayer(now, nil, nil)
	c.Stats["fatigue_potion_item_416"] = 1
	c.Stats["fatigue_potion_day_416"] = premium.ServiceDayID(now)
	c.Stats["fatigue_potion_used_416"] = 3
	d := itemusage.Definition{ItemID: 500, GroupID: -1, Daily: true, DailyLimit: 1, Weekly: true, WeeklyLimit: 2, WeeklyResetDay: time.Thursday}
	if err := itemusage.Consume(&c, d, now); err != nil {
		t.Fatal(err)
	}
	for _, hours := range []int{0, 24, 7 * 24} {
		bodies, ok := buildCurrentItemUsageBodies(c, *inv, now.Add(time.Duration(hours)*time.Hour), catalog, source)
		if !ok {
			t.Fatal("missing history")
		}
		gotD, gotW := decodeItemUsagePages(t, bodies)
		wantD := map[uint32]byte{416: 0, 500: 0}
		wantW := map[uint32]byte{500: 1}
		if hours == 0 {
			wantD[416] = 3
			wantD[500] = 1
		}
		if hours == 7*24 {
			wantW[500] = 0
		}
		if !reflect.DeepEqual(gotD, wantD) || !reflect.DeepEqual(gotW, wantW) {
			t.Fatalf("hours=%d daily=%v weekly=%v", hours, gotD, gotW)
		}
	}
}
