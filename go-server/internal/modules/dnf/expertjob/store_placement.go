package expertjob

import (
	"errors"
	"math"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

var (
	ErrStorePointUnavailable = errors.New("expert job store point is outside valid map placement data")
	ErrStoreCommercialZone   = errors.New("expert job store point is inside an NPC commercial restriction")
)

// ValidateStorePlacement uses PVF [virtual movable area] x/y/width/height
// rectangles and the reference ExpertJobStorePlacementValidator NPC exclusion:
// strict X +/-80, Y +/-150. Map rectangle edges are inclusive.
func ValidateStorePlacement(m worldmap.Map, x, y int64) error {
	if len(m.VirtualMovableArea) == 0 || len(m.VirtualMovableArea)%4 != 0 {
		return ErrStorePointUnavailable
	}
	inside := false
	for i := 0; i < len(m.VirtualMovableArea); i += 4 {
		r := m.VirtualMovableArea[i : i+4]
		if r[2] < 0 || r[3] < 0 || r[0] > math.MaxInt64-r[2] || r[1] > math.MaxInt64-r[3] {
			return ErrStorePointUnavailable
		}
		if x >= r[0] && x <= r[0]+r[2] && y >= r[1] && y <= r[1]+r[3] {
			inside = true
		}
	}
	if !inside {
		return ErrStorePointUnavailable
	}
	for _, npc := range m.NPCs {
		// Real map positions are signed 32-bit integers. Bound malformed data before
		// subtracting so an overflow cannot turn a restricted point into an allowed one.
		if npc.Position.X < math.MinInt32 || npc.Position.X > math.MaxInt32 || npc.Position.Y < math.MinInt32 || npc.Position.Y > math.MaxInt32 {
			return ErrStorePointUnavailable
		}
		if x > npc.Position.X-80 && x < npc.Position.X+80 && y > npc.Position.Y-150 && y < npc.Position.Y+150 {
			return ErrStoreCommercialZone
		}
	}
	return nil
}
