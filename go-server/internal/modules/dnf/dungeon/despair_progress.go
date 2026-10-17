package dungeon

import "fmt"

// DespairHighestClearedStatKey is the character Stats key that stores the
// highest cleared Tower of Despair floor. A missing key (including a nil map)
// means no floor has been cleared yet and is read as 0.
const DespairHighestClearedStatKey = "tower_of_despair_highest_cleared"

// despairMaximumFloor is the Tower of Despair floor count. The real PVF tower
// sections (11008.. tower of despair) describe floors 1..100, so 100 is the
// only legal cap for both the persisted value and a clear request.
const despairMaximumFloor = 100

// DespairHighestCleared reads the persisted highest cleared floor. The value is
// the only authority: missing is 0, and anything outside 0..100 is a corrupt
// persisted state rather than a floor to be clamped.
func DespairHighestCleared(stats map[string]int64) (int, error) {
	highest, present := stats[DespairHighestClearedStatKey]
	if !present {
		return 0, nil
	}
	if highest < 0 || highest > despairMaximumFloor {
		return 0, fmt.Errorf(
			"%w: despair highest cleared floor=%d",
			ErrSettlementStateInvalid,
			highest,
		)
	}
	return int(highest), nil
}

// DespairNextFloor is the only floor the character may enter next. Clearing the
// last floor keeps the tower capped instead of overflowing past floor 100.
func DespairNextFloor(stats map[string]int64) (int, error) {
	highest, err := DespairHighestCleared(stats)
	if err != nil {
		return 0, err
	}
	return min(despairMaximumFloor, highest+1), nil
}

// ApplyDespairClear records one cleared floor. The request must be 1..100, the
// persisted highest must already be legal, and the floor may not skip ahead
// (floor <= highest+1). Replaying an already cleared floor is accepted and does
// not move the persisted high-water mark; no other Stats entry is touched.
func ApplyDespairClear(stats map[string]int64, floor int) error {
	if stats == nil {
		return fmt.Errorf("%w: despair clear has no persisted stats", ErrSettlementStateInvalid)
	}
	if floor < 1 || floor > despairMaximumFloor {
		return fmt.Errorf("%w: despair clear floor=%d", ErrSettlementStateInvalid, floor)
	}
	highest, err := DespairHighestCleared(stats)
	if err != nil {
		return err
	}
	if floor > highest+1 {
		return fmt.Errorf(
			"%w: despair clear skipped floors highest=%d floor=%d",
			ErrSettlementStateInvalid,
			highest,
			floor,
		)
	}
	if floor > highest {
		stats[DespairHighestClearedStatKey] = int64(floor)
	}
	return nil
}
