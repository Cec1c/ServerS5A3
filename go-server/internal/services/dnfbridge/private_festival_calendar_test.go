package dnfbridge

import (
	"bytes"
	"fmt"
	"longheng.io/server/internal/modules/dnf/festival"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestPrivateFestivalCalendarPreservesOtherActivities(t *testing.T) {
	c := &festival.Catalog{Periods: map[uint32]festival.Period{}}
	for _, id := range privateFestivalCalendarPeriods {
		c.Periods[id] = festival.Period{ID: id}
	}
	for _, native := range []bool{false, true} {
		old, e := buildCurrentActivityEventInfoBodyWithRuntimeActivitiesAndMarble([]uint32{100}, 10800, true, true, true, true, true, 2418, true, true, true, true, true, nil, true, native, true, true)
		if e != nil {
			t.Fatal(e)
		}
		got, e := privateFestivalCalendarBody(old, c)
		if e != nil {
			t.Fatal(e)
		}
		a, b := parseCurrentSpendTimeEventInfoBody(t, old), parseCurrentSpendTimeEventInfoBody(t, got)
		if b.baseCount != a.baseCount+6 || b.activeCount != a.activeCount+6 {
			t.Fatal("row counts")
		}
		for _, before := range a.baseRows {
			found := false
			for _, after := range b.baseRows {
				if before.eventID != after.eventID {
					continue
				}
				found = true
				if before.eventID == 228 || before.eventID == 2347 || before.eventID == 331 {
					if after.hasExtended || len(after.raw) != 15 {
						t.Fatal("legacy menu still visible")
					}
				} else if !bytes.Equal(before.raw, after.raw) {
					t.Fatalf("changed activity %d", before.eventID)
				}
			}
			if !found {
				t.Fatal("lost base activity")
			}
		}
		for _, before := range a.activeRows {
			found := false
			for _, after := range b.activeRows {
				if before.eventID == after.eventID {
					found = true
					if !bytes.Equal(before.raw, after.raw) {
						t.Fatal("active reward state changed")
					}
				}
			}
			if !found {
				t.Fatal("lost active state")
			}
		}
		for i, pid := range privateFestivalCalendarPeriods {
			found := false
			for _, row := range b.baseRows {
				if row.eventID != uint16(60001+i) {
					continue
				}
				found = true
				if !row.hasExtended || row.raw[len(row.raw)-1] != 1 || !bytes.Contains(row.raw, []byte(fmt.Sprintf("UI/Event/PrivateFestival005/%d.xui/0/0", pid))) {
					t.Fatal("wrong private route")
				}
			}
			if !found {
				t.Fatal("missing period")
			}
		}
		same, e := privateFestivalCalendarBody(old, nil)
		if e != nil || !bytes.Equal(same, old) {
			t.Fatal("upstream fallback changed")
		}
		for cut := 0; cut < len(old); cut++ {
			if _, e := privateFestivalCalendarBody(old[:cut], c); e == nil {
				t.Fatalf("accepted truncation %d", cut)
			}
		}
		if _, e := privateFestivalCalendarBody(got, c); e == nil {
			t.Fatal("accepted duplicate IDs")
		}
	}
	delete(c.Periods, 201601)
	if _, e := privateFestivalCalendarBody([]byte{0, 0, 0}, c); e == nil {
		t.Fatal("accepted incomplete private config")
	}
}
func TestPrivateFestivalCalendarActualPVF(t *testing.T) {
	path := os.Getenv("PRIVATE_FESTIVAL_PVF")
	if path == "" {
		t.Skip("real private PVF required")
	}
	archive, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	cat, e := newPVFCeraShopCatalog(archive)
	if e != nil {
		t.Fatal(e)
	}
	body, e := privateFestivalCalendarBody([]byte{0, 0, 0}, cat.festivals)
	if e != nil {
		t.Fatal(e)
	}
	s := parseCurrentSpendTimeEventInfoBody(t, body)
	if s.baseCount != 6 || s.activeCount != 6 {
		t.Fatal("real PVF period count")
	}
	// The source menu contains no fallback entries for the hidden server rows.
	text, e := archive.ReadText("event/eventlistwindow.evt")
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []int{228, 2347, 331} {
		if bytes.Contains([]byte(text), []byte(fmt.Sprintf("[event index]\n%d ", id))) {
			t.Fatal("legacy PVF fallback requires removal")
		}
	}
}
