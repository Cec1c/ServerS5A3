// Package itemusage owns persisted per-character item-use counts. Call Consume
// only inside the same character-asset transaction that applies the item effect
// and consumes its source. Inventory movement, sale and deletion are not uses.
package itemusage

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const ItemKeyPrefix = "item_usage_item_"

var (
	ErrDefinition = errors.New("invalid item usage definition")
	ErrLimit      = errors.New("item usage limit reached")
	ErrClock      = errors.New("item usage service period moved backwards")
)

type Definition struct {
	ItemID                  uint32
	GroupID                 int64 // -1 means item identity; unrelated to [cooltime group].
	Daily, Weekly           bool
	DailyLimit, WeeklyLimit int64
	WeeklyResetDay          time.Weekday
}

// FromPVF accepts explicit current-client daily/weekly fields only. Other
// event/account/server-wide restrictions are not per-character daily counts.
func FromPVF(itemID uint32, document *pvf.Document) (*Definition, error) {
	if document == nil || itemID == 0 {
		return nil, ErrDefinition
	}
	d := &Definition{ItemID: itemID, GroupID: -1}
	read := func(key string) (int64, bool, error) {
		if _, present := document.Section(key); !present {
			return 0, false, nil
		}
		values := document.Ints(key)
		if len(values) == 0 {
			return 0, true, fmt.Errorf("%w: %s", ErrDefinition, key)
		}
		for _, value := range values {
			if value != values[0] {
				return 0, true, fmt.Errorf("%w: conflicting %s", ErrDefinition, key)
			}
		}
		return values[0], true, nil
	}
	for _, field := range []struct {
		key     string
		enabled *bool
		limit   *int64
	}{
		{"total usable count", &d.Daily, &d.DailyLimit},
		{"total weekly usable count", &d.Weekly, &d.WeeklyLimit},
	} {
		value, present, err := read(field.key)
		if err != nil {
			return nil, err
		}
		if present && (value < -1 || value > 255) {
			return nil, ErrDefinition
		}
		*field.enabled, *field.limit = present && value >= 0, value
	}
	if !d.Daily && !d.Weekly {
		return nil, nil
	}
	if group, present, err := read("usable count group"); err != nil {
		return nil, err
	} else if present {
		if group < -1 || group > int64(^uint32(0)) {
			return nil, ErrDefinition
		}
		d.GroupID = group
	}
	if d.Weekly {
		day, present, err := read("total weekly usable count reset day")
		if err != nil || !present || day < 0 || day > 6 {
			return nil, ErrDefinition
		}
		d.WeeklyResetDay = time.Weekday(day)
	}
	return d, nil
}

func (d Definition) valid() bool {
	return d.ItemID != 0 && d.GroupID >= -1 && d.GroupID <= int64(^uint32(0)) && (d.Daily || d.Weekly) &&
		(!d.Daily || d.DailyLimit >= 0 && d.DailyLimit <= 255) &&
		(!d.Weekly || d.WeeklyLimit >= 0 && d.WeeklyLimit <= 255 && d.WeeklyResetDay >= 0 && d.WeeklyResetDay <= 6)
}

func (d Definition) group() string {
	if d.GroupID >= 0 {
		return "group_" + strconv.FormatInt(d.GroupID, 10)
	}
	return "item_" + strconv.FormatUint(uint64(d.ItemID), 10)
}

func (d Definition) periods(now time.Time) (int64, int64) {
	day := premium.ServiceDayID(now)
	weekday := time.Unix(day*86400, 0).UTC().Weekday()
	return day, day - int64((int(weekday)-int(d.WeeklyResetDay)+7)%7)
}

// Counts is read-only. Old periods project zero without rewriting history;
// future periods retain their counts until the clock catches up.
func Counts(character repo.CharacterRecord, d Definition, now time.Time) (daily, weekly byte) {
	day, week := d.periods(now)
	read := func(kind string, period int64) byte {
		base := "item_usage_" + kind + "_" + d.group()
		if character.Stats[base+"_period"] < period {
			return 0
		}
		n := character.Stats[base+"_used"]
		if n < 0 {
			return 0
		}
		if n > 255 {
			return 255
		}
		return byte(n)
	}
	if d.Daily {
		daily = read("daily", day)
	}
	if d.Weekly {
		weekly = read("weekly", week)
	}
	return
}

func Consume(character *repo.CharacterRecord, d Definition, now time.Time) error {
	if character == nil || character.CharacterID == "" || !d.valid() || now.IsZero() {
		return ErrDefinition
	}
	day, week := d.periods(now)
	type change struct {
		base         string
		period, used int64
	}
	var changes []change
	for _, row := range []struct {
		kind          string
		enabled       bool
		period, limit int64
	}{
		{"daily", d.Daily, day, d.DailyLimit}, {"weekly", d.Weekly, week, d.WeeklyLimit},
	} {
		if !row.enabled {
			continue
		}
		base := "item_usage_" + row.kind + "_" + d.group()
		stored := character.Stats[base+"_period"]
		if stored > row.period {
			return ErrClock
		}
		used := int64(0)
		if stored == row.period {
			used = character.Stats[base+"_used"]
			if used < 0 {
				used = 0
			}
		}
		if used >= row.limit {
			return ErrLimit
		}
		changes = append(changes, change{base, row.period, used + 1})
	}
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	for _, change := range changes {
		character.Stats[change.base+"_period"] = change.period
		character.Stats[change.base+"_used"] = change.used
	}
	character.Stats[ItemKeyPrefix+strconv.FormatUint(uint64(d.ItemID), 10)] = 1
	return nil
}
