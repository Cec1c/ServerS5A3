package dnfbridge

import (
	"fmt"
	"sync"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// A zero-regeneration ridable belongs to its frozen run selection. The current
// client's self-destruction report retires that selection for subsequent full
// room projections. It does not authorize a replacement or change room clear,
// drops, rewards, or the immutable source signature.
type currentDungeonRidableTerminalOwner struct {
	mu        sync.RWMutex
	destroyed map[currentDungeonRidableSource]bool
}

type currentDungeonRidableSource struct {
	Group       int
	SourceIndex int
	Room        worldmap.RoomCoordinate
	Code        uint32
}

func currentDungeonRidableSourceFor(entry currentDungeonRandomizedEntry) currentDungeonRidableSource {
	return currentDungeonRidableSource{
		Group: entry.Source.Group, SourceIndex: entry.Source.SourceIndex,
		Room: entry.Source.Map, Code: uint32(entry.Source.ObjectID),
	}
}

func currentDungeonRidableTerminalEligible(entry currentDungeonRandomizedEntry) bool {
	return entry.Ridable && entry.SpawnMode == 0 && entry.Source.Regenerate.Set && entry.Source.Regenerate.Value == 0
}

func (o *currentDungeonRidableTerminalOwner) retired(entry currentDungeonRandomizedEntry) bool {
	if o == nil || !currentDungeonRidableTerminalEligible(entry) {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.destroyed[currentDungeonRidableSourceFor(entry)]
}

// The native report contains a definition key, not an instance or generation.
// Resolve exactly one matching frozen ridable in the current room. Ambiguous
// definitions and missing FF room bytes must never retire a different source.
// Release and mount remain local native actions; neither report revives a
// destroyed zero-regeneration source or changes another player's occupancy.
func (o *currentDungeonRandomizedOwner) applyRidableReport(room worldmap.RoomCoordinate, report currentDungeonRidableRequest) (bool, error) {
	if o == nil || o.ridableTerminals == nil || report.RoomX == 0xff || report.RoomY == 0xff ||
		room.X != int64(report.RoomX) || room.Y != int64(report.RoomY) {
		return false, fmt.Errorf("ridable report has no current room owner")
	}
	if report.State != currentDungeonRidableStateReleased && report.State != currentDungeonRidableStateMounted && report.State != currentDungeonRidableStateDestroyed {
		return false, errDungeonRidableRequestState
	}
	var selected currentDungeonRandomizedEntry
	count := 0
	for _, entry := range o.Entries {
		if entry.Source.Map == room && uint32(entry.Source.ObjectID) == report.DefinitionKeyRaw && entry.SpawnMode == 0 {
			selected = entry
			count++
		}
	}
	if count != 1 || !currentDungeonRidableTerminalEligible(selected) {
		return false, fmt.Errorf("ridable report source unavailable or ambiguous")
	}
	if report.State != currentDungeonRidableStateDestroyed {
		return false, nil
	}
	o.ridableTerminals.mu.Lock()
	defer o.ridableTerminals.mu.Unlock()
	key := currentDungeonRidableSourceFor(selected)
	if o.ridableTerminals.destroyed[key] {
		return false, nil
	}
	if o.ridableTerminals.destroyed == nil {
		o.ridableTerminals.destroyed = make(map[currentDungeonRidableSource]bool)
	}
	o.ridableTerminals.destroyed[key] = true
	return true, nil
}
