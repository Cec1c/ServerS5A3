package worldmap

import (
	"errors"
	"testing"
)

func enableTowerCache(t *testing.T, table *Table, source textSource) {
	t.Helper()
	options := DefaultMapCacheOptions()
	options.PinnedBytes = 0
	if err := table.EnableMapCache(source, options); err != nil {
		t.Fatal(err)
	}
}

func TestTowerStartResourcePoolUsesCompleteCachedDefinition(t *testing.T) {
	table := towerFloorTable()
	resolver := towerFloorResolver(t, table)
	room := towerStartRoom(t, resolver)
	enableTowerCache(t, table, textSource{
		towerNormalMapPath: "[npc]\n22 `[right]` 187 200 0\n",
	})

	maps, ok, err := resolver.towerStartResourcePoolRead(towerFloorID, 0, room)
	if err != nil || !ok || len(maps) != 1 {
		t.Fatalf("cached tower fallback = %+v ok=%t err=%v", maps, ok, err)
	}
	if maps[0].ID != towerNormalMapID || len(maps[0].NPCs) != 1 || maps[0].NPCs[0].NPCID != 22 {
		t.Fatalf("tower fallback lost the complete cached map definition: %+v", maps[0])
	}
	if snap := table.MapCacheSnapshot(); snap.Loads != 1 || snap.Errors != 0 || snap.Entries != 1 {
		t.Fatalf("tower fallback did not use the configured map cache: %+v", snap)
	}
}

func TestTowerStartResourcePoolReadErrorPropagatesThroughLayout(t *testing.T) {
	table := towerFloorTable()
	resolver := towerFloorResolver(t, table)
	room := towerStartRoom(t, resolver)
	enableTowerCache(t, table, textSource{})

	_, _, err := dungeonLayoutPoolCandidates(resolver, towerFloorID, 0, MapFileStart, room)
	if !errors.Is(err, ErrMapDefinitionRead) {
		t.Fatalf("layout swallowed cached tower read failure: %v", err)
	}
}
