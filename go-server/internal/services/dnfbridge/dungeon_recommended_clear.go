package dnfbridge

import "longheng.io/server/internal/modules/dnf/worldmap"

func currentDungeonMatchesRecommendedLevel(runtime *runtimeDungeonState) bool {
	if runtime == nil || runtime.Dungeon.ID <= 0 || runtime.Character.Level <= 0 {
		return false
	}
	minimum, maximum, ok := currentDungeonRecommendedLevelRange(runtime.Dungeon.Metadata)
	if !ok {
		return false
	}
	level := runtime.entryCharacterLevel
	if level == 0 {
		level = runtime.Character.Level
	}
	return int64(level) >= minimum && int64(level) <= maximum
}

func currentDungeonIsRecommendedClear(runtime *runtimeDungeonState) bool {
	return runtime != nil &&
		runtime.ordinaryFinalRoomClearAccepted &&
		currentDungeonMatchesRecommendedLevel(runtime)
}

// Older dungeon scripts omit [recommended level] and declare the suitable
// interval with [minimum required level] and [basis level]. Never fall back
// from a present but malformed explicit range.
func currentDungeonRecommendedLevelRange(metadata worldmap.DungeonMetadata) (int64, int64, bool) {
	if len(metadata.RecommendedLevels) != 0 {
		levels := metadata.RecommendedLevels
		if len(levels) < 2 || levels[0] <= 0 || levels[1] < levels[0] {
			return 0, 0, false
		}
		return levels[0], levels[1], true
	}
	if metadata.MinimumRequiredLevel.Set && metadata.BasisLevel.Set && metadata.MinimumRequiredLevel.Value > 0 && metadata.BasisLevel.Value >= metadata.MinimumRequiredLevel.Value {
		return metadata.MinimumRequiredLevel.Value, metadata.BasisLevel.Value, true
	}
	return 0, 0, false
}
