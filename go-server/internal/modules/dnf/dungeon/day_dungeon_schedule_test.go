package dungeon

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestDayDungeonWeekdayAndServiceDayBoundary(t *testing.T) {
	for offset, event := range []uint16{2418, 2419, 2420, 2421, 2422, 2423, 2424} {
		now := time.Date(2026, 10, 5+offset, 2, 0, 0, 0, time.UTC)
		if got := DayDungeonEvent(now); got != event {
			t.Fatalf("weekday %s: got %d want %d", now, got, event)
		}
	}
	boundary := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	if DayDungeonEvent(boundary.Add(-time.Nanosecond)) != 2421 || DayDungeonEvent(boundary) != 2422 {
		t.Fatal("Thursday to Friday changed outside 06:00 UTC+8 boundary")
	}
}

func TestDayDungeonRejectsClosedOrMissingScheduleAndPreservesOthers(t *testing.T) {
	for id, events := range DayDungeonSchedules() {
		path := fmt.Sprintf("dungeon/gcontents/171116_dayofdungeon/chn_daydungeon_%02d.dgn", id-4905)
		doc, err := pvf.Parse(path, fmt.Sprintf("[schedule]\n%d %d\n[/schedule]\n", events[0], events[1]))
		if err != nil {
			t.Fatal(err)
		}
		d := worldmap.ParseDungeon(id, path, doc)
		for weekday := 0; weekday < 7; weekday++ {
			now := time.Date(2026, 10, 5+weekday, 2, 0, 0, 0, time.UTC)
			err := ValidateDayDungeonSchedule(d, now)
			open := int64(DayDungeonEvent(now)) == events[0] || int64(DayDungeonEvent(now)) == events[1]
			if open && err != nil || !open && !errors.Is(err, ErrDayDungeonClosed) {
				t.Fatalf("%d %s: %v", id, now, err)
			}
		}
		d.SourceSections = nil
		if ValidateDayDungeonSchedule(d, time.Now()) == nil {
			t.Fatal("missing PVF schedule accepted")
		}
		d.ID = 316
		if err := ValidateDayDungeonSchedule(d, time.Now()); err != nil {
			t.Fatal("rift entry changed", err)
		}
	}
}
