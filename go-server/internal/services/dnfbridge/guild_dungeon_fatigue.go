package dnfbridge

import (
	"fmt"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var guildDungeonEntryIDs = map[int64]struct{}{
	8500: {},
	8504: {},
	8507: {},
	8505: {},
	8503: {},
	8508: {},
}

func currentGuildDungeonEntryID(id int64) bool {
	_, ok := guildDungeonEntryIDs[id]
	return ok
}

// currentDungeonStartFatigueRule returns the admission cost and whether it is
// charged once for the run. The six guild dungeons fail closed if their real
// DGN integer declaration is missing or malformed.
func currentDungeonStartFatigueRule(dungeon worldmap.Dungeon) (int64, bool, error) {
	if !currentGuildDungeonEntryID(dungeon.ID) {
		return 1, false, nil
	}
	cost, declared := dungeon.Metadata.IntegerValues[worldmap.DungeonUseFatigueOnlyStartKey]
	if !declared {
		return 0, true, fmt.Errorf("guild dungeon %d has no %q integer declaration", dungeon.ID, worldmap.DungeonUseFatigueOnlyStartKey)
	}
	if cost <= 0 {
		return 0, true, fmt.Errorf("guild dungeon %d declares invalid start fatigue cost %d", dungeon.ID, cost)
	}
	return cost, true, nil
}

func debitCurrentDungeonFatigueAmount(character *dnfrepo.CharacterRecord, amount int64) error {
	if character == nil || amount <= 0 {
		return nil
	}
	remaining, known := dnfdungeon.CharacterStat(character.Stats, "fatigue", "fp", "疲劳")
	if !known {
		return dnfdungeon.ErrEntryFatigueUnknown
	}
	if remaining < amount {
		return fmt.Errorf("%w: fatigue=%d required=%d", dnfdungeon.ErrEntryFatigueExhausted, remaining, amount)
	}
	character.Stats["fatigue"] = remaining - amount
	character.Stats["fatigue_used"] = character.Stats["fatigue_limit"] - character.Stats["fatigue"]
	character.Stats["fatigue_display_update"] = character.Stats["fatigue_used"]
	return nil
}
