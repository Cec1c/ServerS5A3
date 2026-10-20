package dungeon

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var ErrDayDungeonClosed = errors.New("day dungeon is closed on this service day")

// DayDungeonEvent uses the server's established 06:00 UTC+8 service day.
// The weekday/event correspondence is the published five dgn [schedule]
// pairs combined with the original growthguide's weekday descriptions.
// This is an availability flag; character/account entry counters are separate.
func DayDungeonEvent(now time.Time) uint16 {
	day := time.Unix(premium.ServiceDayID(now)*86400, 0).UTC().Weekday()
	if day == time.Sunday {
		return 2424
	}
	return 2417 + uint16(day)
}

func DayDungeonSchedules() map[int64][]int64 {
	return map[int64][]int64{4906: {2418, 2423}, 4907: {2421, 2424}, 4908: {2420, 2423}, 4909: {2422, 2424}, 4910: {2419, 2423}}
}

func ValidateDayDungeonSchedule(d worldmap.Dungeon, now time.Time) error {
	expected, isDayDungeon := DayDungeonSchedules()[d.ID]
	if !isDayDungeon {
		return nil
	}
	wantPath := fmt.Sprintf("dungeon/gcontents/171116_dayofdungeon/chn_daydungeon_%02d.dgn", d.ID-4905)
	if !strings.EqualFold(strings.ReplaceAll(d.Path, "\\", "/"), wantPath) {
		return fmt.Errorf("day dungeon %d has an unverified PVF path", d.ID)
	}
	var schedule []int64
	for _, section := range d.SourceSections {
		if section.Name != "schedule" || len(section.Scope) != 0 {
			continue
		}
		for _, token := range section.Tokens {
			if token.Kind != pvf.TokenInt {
				return fmt.Errorf("day dungeon %d has malformed schedule", d.ID)
			}
			schedule = append(schedule, token.Int)
		}
	}
	if len(schedule) != len(expected) {
		return fmt.Errorf("day dungeon %d lacks the published schedule", d.ID)
	}
	for i, event := range expected {
		if schedule[i] != event {
			return fmt.Errorf("day dungeon %d schedule differs from published PVF", d.ID)
		}
	}
	for _, event := range schedule {
		if event == int64(DayDungeonEvent(now)) {
			return nil
		}
	}
	return ErrDayDungeonClosed
}
