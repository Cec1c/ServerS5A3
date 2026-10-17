package dungeon

import "fmt"

// GriefHighestClearedStatKey is the character Stats key that stores the
// highest cleared Tower of Grief floor in the current circuit. A missing key (including a nil map)
// means no floor has been cleared yet and is read as 0.
const GriefHighestClearedStatKey = "tower_of_grief_highest_cleared"

// griefMaximumFloor is the Tower of Grief floor count. The real PVF tower
// section describes floors 1..100, so 100 is the only legal cap for both the
// persisted value and a clear request.
const griefMaximumFloor = 100

// GriefHighestCleared reads the persisted highest cleared floor. The value is
// the only authority: missing is 0, and anything outside 0..100 is a corrupt
// persisted state rather than a floor to be clamped.
func GriefHighestCleared(stats map[string]int64) (int, error) {
	highest, present := stats[GriefHighestClearedStatKey]
	if !present {
		return 0, nil
	}
	if highest < 0 || highest > griefMaximumFloor {
		return 0, fmt.Errorf(
			"%w: grief highest cleared floor=%d",
			ErrSettlementStateInvalid,
			highest,
		)
	}
	return int(highest), nil
}

// GriefNextFloor is the only floor the character may enter next. The local
// reference TowerClear policy restarts Grief at floor one after floor 100.
func GriefNextFloor(stats map[string]int64) (int, error) {
	highest, err := GriefHighestCleared(stats)
	if err != nil {
		return 0, err
	}
	return highest%griefMaximumFloor + 1, nil
}

// ApplyGriefClear records one cleared floor. The request must be 1..100, the
// persisted highest must already be legal, and the floor may not skip ahead
// (floor <= highest+1). Replaying an already cleared floor is accepted and does
// not move the persisted high-water mark; no other Stats entry is touched.
func ApplyGriefClear(stats map[string]int64, floor int) error {
	if stats == nil {
		return fmt.Errorf("%w: grief clear has no persisted stats", ErrSettlementStateInvalid)
	}
	if floor < 1 || floor > griefMaximumFloor {
		return fmt.Errorf("%w: grief clear floor=%d", ErrSettlementStateInvalid, floor)
	}
	highest, err := GriefHighestCleared(stats)
	if err != nil {
		return err
	}
	if highest == griefMaximumFloor && floor != griefMaximumFloor {
		if floor != 1 {
			return fmt.Errorf("%w: grief circuit must restart at floor one", ErrSettlementStateInvalid)
		}
		// The completion receipt above this function prevents a replay of
		// the last circuit from becoming a new floor-one clear.
		highest = 0
	}
	if floor > highest+1 {
		return fmt.Errorf(
			"%w: grief clear skipped floors highest=%d floor=%d",
			ErrSettlementStateInvalid,
			highest,
			floor,
		)
	}
	if floor > highest {
		stats[GriefHighestClearedStatKey] = int64(floor)
	}
	return nil
}
