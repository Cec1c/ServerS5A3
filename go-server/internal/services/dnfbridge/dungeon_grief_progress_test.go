package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestGriefFloorIdentityAndCanonicalPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		id      int64
		marker  int64
		want    int
		invalid bool
	}{
		{name: "first floor", path: "dungeon/towers/grief/5115_001.dgn", id: 5115, marker: 1, want: 1},
		{name: "last floor", path: "Dungeon\\Towers\\Grief\\5214_100.dgn", id: 5214, marker: 1, want: 100},
		{name: "middle floor", path: "dungeon/towers/grief/5164_050.dgn", id: 5164, marker: 1, want: 50},
		{name: "no marker", path: "dungeon/towers/grief/5115_001.dgn", id: 5115},
		{name: "zero marker", path: "dungeon/towers/grief/5115_001.dgn", id: 5115, marker: 0},
		{name: "other tower marker", path: "dungeon/towers/grief/5115_001.dgn", id: 5115, marker: 2},
		{name: "empty path", id: 5115, marker: 1, invalid: true},
		{name: "wrong directory", path: "dungeon/towers/despair/5115_001.dgn", id: 5115, marker: 1, invalid: true},
		{name: "directory prefix only", path: "dungeon/towers/griefish/5115_001.dgn", id: 5115, marker: 1, invalid: true},
		{name: "extra path segment", path: "dungeon/towers/grief/5115_001.dgn/extra", id: 5115, marker: 1, invalid: true},
		{name: "object ID mismatch", path: "dungeon/towers/grief/5115_001.dgn", id: 5116, marker: 1, invalid: true},
		{name: "unknown ID mapping", path: "dungeon/towers/grief/5116_001.dgn", id: 5116, marker: 1, invalid: true},
		{name: "zero floor", path: "dungeon/towers/grief/5114_000.dgn", id: 5114, marker: 1, invalid: true},
		{name: "floor above cap", path: "dungeon/towers/grief/5215_101.dgn", id: 5215, marker: 1, invalid: true},
		{name: "short floor", path: "dungeon/towers/grief/5115_1.dgn", id: 5115, marker: 1, invalid: true},
		{name: "unpadded floor", path: "dungeon/towers/grief/5115_01.dgn", id: 5115, marker: 1, invalid: true},
		{name: "missing floor extension", path: "dungeon/towers/grief/5115_001", id: 5115, marker: 1, invalid: true},
		{name: "wrong extension", path: "dungeon/towers/grief/5115_001.txt", id: 5115, marker: 1, invalid: true},
		{name: "missing object ID", id: 0, marker: 1, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := worldmap.Dungeon{ID: tc.id, Path: tc.path}
			d.Metadata.IntegerValues = map[string]int64{"tower of grief": tc.marker}
			floor, err := currentGriefFloor(d)
			if tc.invalid {
				if !errors.Is(err, dnfdungeon.ErrSettlementStateInvalid) || floor != 0 {
					t.Fatalf("floor=%d err=%v, want settlement state invalid", floor, err)
				}
				return
			}
			if err != nil || floor != tc.want {
				t.Fatalf("floor=%d err=%v, want %d", floor, err, tc.want)
			}
		})
	}
}

func TestGriefFloorMapsAllKnownFloorIDs(t *testing.T) {
	for floor := 1; floor <= 100; floor++ {
		id := int64(griefFirstFloorID + floor)
		d := worldmap.Dungeon{
			ID:   id,
			Path: fmt.Sprintf("dungeon/towers/grief/%d_%03d.dgn", id, floor),
		}
		d.Metadata.IntegerValues = map[string]int64{"tower of grief": 1}
		got, err := currentGriefFloor(d)
		if err != nil || got != floor {
			t.Fatalf("floor %d id %d: got=%d err=%v", floor, id, got, err)
		}
	}
}

func TestRealPVFGriefFloorAndTargetDirectoryMappings(t *testing.T) {
	table := loadRealPVFGriefTable(t)
	for floor := 1; floor <= 100; floor++ {
		id := int64(griefFirstFloorID + floor)
		dungeon, found := table.FindDungeon(id)
		if !found {
			t.Fatalf("real PVF missing floor %d dungeon %d", floor, id)
		}
		got, err := currentGriefFloor(dungeon)
		if err != nil || got != floor {
			t.Fatalf("real PVF floor %d dungeon %d: got=%d err=%v path=%s", floor, id, got, err, dungeon.Path)
		}
		targetPath := fmt.Sprintf("%s%d_%03d.dgn", griefFloorDirectory, id, floor)
		if !strings.EqualFold(strings.ReplaceAll(dungeon.Path, "\\", "/"), targetPath) {
			t.Fatalf("real PVF floor %d path=%q want=%q", floor, dungeon.Path, targetPath)
		}
		mapped, found := table.FindDungeonPath(targetPath)
		if !found || mapped.ID != id {
			t.Fatalf("real PVF floor %d path lookup found=%t dungeon=%+v", floor, found, mapped)
		}
		if mappedFloor, err := currentGriefFloor(mapped); err != nil || mappedFloor != floor {
			t.Fatalf("real PVF floor %d target got=%d err=%v", floor, mappedFloor, err)
		}
	}
}

func TestRealPVFGriefSelectionUsesSavedFloor(t *testing.T) {
	table := loadRealPVFGriefTable(t)
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	repos := memory.NewMemoryGroup()
	s := &Service{
		worldMapTable:      table,
		worldMapResolver:   resolver,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	session := &gameSession{selectedCharacterID: 99, accountID: "grief-account"}
	for _, tc := range []struct {
		highest int64
		want    uint32
	}{
		{highest: 0, want: 5115},
		{highest: 1, want: 5116},
		{highest: 2, want: 5117},
		{highest: 98, want: 5213},
		{highest: 99, want: 5214},
		{highest: 100, want: 5115},
	} {
		character := dnfrepo.CharacterRecord{
			CharacterID: "99",
			AccountID:   session.accountID,
			Stats:       map[string]int64{dnfdungeon.GriefHighestClearedStatKey: tc.highest},
		}
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		for _, requested := range []uint32{5115, 5164, 5214} {
			got, err := s.resolveCurrentGriefSelection(session, requested)
			if err != nil || got != tc.want {
				t.Fatalf("highest=%d request=%d got=%d err=%v want=%d", tc.highest, requested, got, err, tc.want)
			}
		}
	}
	if got, err := s.resolveCurrentGriefSelection(session, 3); err != nil || got != 3 {
		t.Fatalf("ordinary dungeon redirected: got=%d err=%v", got, err)
	}
	corrupt := dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   session.accountID,
		Stats:       map[string]int64{dnfdungeon.GriefHighestClearedStatKey: 101},
	}
	if err := repos.Character.Save(context.Background(), corrupt); err != nil {
		t.Fatal(err)
	}
	if got, err := s.resolveCurrentGriefSelection(session, 5115); !errors.Is(err, dnfdungeon.ErrSettlementStateInvalid) || got != 0 {
		t.Fatalf("corrupt progress got=%d err=%v", got, err)
	}
}

func loadRealPVFGriefTable(t *testing.T) *worldmap.Table {
	t.Helper()
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires fixed published PVF")
	}
	archive, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return table
}
