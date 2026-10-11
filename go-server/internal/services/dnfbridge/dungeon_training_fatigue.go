package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The actual dungeon.lst binds 5000 to this training-room resource. Its DGN
// omits [no fatigue], so admission and room charging need the same explicit
// training policy. Keep the raw PVF metadata intact and require both identity
// fields; a renamed or reused ID must not exempt an ordinary dungeon.
func currentDungeonUsesNoFatigue(dungeon worldmap.Dungeon) bool {
	if dungeon.Metadata.NoFatigue {
		return true
	}
	return dungeon.ID == 5000 &&
		strings.EqualFold(strings.ReplaceAll(dungeon.Path, "\\", "/"), "dungeon/PoongjinTrainingRoom/PoongjinTrainingRoom.dgn")
}
