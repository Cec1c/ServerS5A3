package dungeon

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"time"
)

// CycleEntryUsed is distinct from the historical successful-clear counter.
// Old successful clears prove a minimum number of entries; absent historical
// abandoned entries cannot be reconstructed. No historical record is rewritten.
func CycleEntryUsed(character dnfrepo.CharacterRecord, dungeonID int64, now time.Time) (int64, error) {
	if dungeonID <= 0 || now.IsZero() {
		return 0, ErrEntryItemConsumeInvalid
	}
	cleared, err := DailyEntryUsed(character, dungeonID, now)
	if err != nil {
		return 0, err
	}
	dayKey, usedKey, _, _ := cycleEntryKeys(dungeonID)
	used := int64(0)
	if character.Stats[dayKey] == premium.ServiceDayID(now) {
		used = character.Stats[usedKey]
		if used < 0 {
			return 0, ErrEntryItemConsumeInvalid
		}
	}
	if cleared > used {
		used = cleared
	}
	return used, nil
}

func cycleEntryKeys(id int64) (string, string, string, string) {
	prefix := fmt.Sprintf("dungeon_cycle_entry_%d_", id)
	return prefix + "day", prefix + "used", prefix + "nonce_hi", prefix + "nonce_lo"
}

type CycleEntryAllowance struct {
	DungeonIDs             []int64
	Limit                  int64
	PrerequisiteDungeonIDs []int64
	PrerequisiteCount      int64
}

func CycleCountSum(character dnfrepo.CharacterRecord, ids []int64, now time.Time, entries bool) (int64, error) {
	seen := make(map[int64]bool)
	total := int64(0)
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return 0, ErrEntryItemConsumeInvalid
		}
		seen[id] = true
		value, err := DailyEntryUsed(character, id, now)
		if entries {
			value, err = CycleEntryUsed(character, id, now)
		}
		if err != nil {
			return 0, err
		}
		if value < 0 || value > math.MaxInt64-total {
			return 0, ErrEntryItemConsumeInvalid
		}
		total += value
	}
	return total, nil
}

// ValidateCycleEntryAllowance is run both before preparing the dungeon and
// against the locked character in the ticket/fatigue/counter transaction.
func ValidateCycleEntryAllowance(character dnfrepo.CharacterRecord, rule CycleEntryAllowance, now time.Time) error {
	if len(rule.DungeonIDs) == 0 || rule.Limit <= 0 {
		return ErrEntryItemConsumeInvalid
	}
	used, err := CycleCountSum(character, rule.DungeonIDs, now, true)
	if err != nil {
		return err
	}
	if used >= rule.Limit {
		return ErrDailyEntryLimit
	}
	if rule.PrerequisiteCount > 0 {
		if len(rule.PrerequisiteDungeonIDs) == 0 {
			return ErrEntryItemConsumeInvalid
		}
		cleared, err := CycleCountSum(character, rule.PrerequisiteDungeonIDs, now, false)
		if err != nil {
			return err
		}
		if cleared < rule.PrerequisiteCount {
			return ErrEntryPrerequisiteMissing
		}
	}
	return nil
}
