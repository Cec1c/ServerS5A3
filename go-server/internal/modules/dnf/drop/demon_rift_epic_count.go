package drop

// DemonRiftEpicCount is the operator's per-clear count distribution for
// dungeon 316: >=2 50%, >=3 20%, and 4 5%, with one guaranteed equipment.
func DemonRiftEpicCount(roll int) int {
	if roll < 0 || roll >= EpicClearProbabilityBound {
		return 0
	}
	switch {
	case roll < 50000:
		return 4
	case roll < 200000:
		return 3
	case roll < 500000:
		return 2
	default:
		return 1
	}
}
