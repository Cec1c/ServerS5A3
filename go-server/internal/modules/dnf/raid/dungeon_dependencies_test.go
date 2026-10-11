package raid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type missingRaidMapSource struct{ dnfpvf.Source }

func (s missingRaidMapSource) ReadText(path string) (string, error) {
	if strings.HasPrefix(strings.ToLower(path), "map/") && strings.HasSuffix(strings.ToLower(path), ".map") {
		return "", fmt.Errorf("injected unreadable actual raid map")
	}
	return s.Source.ReadText(path)
}

func TestRealPVFRaidDungeonDependenciesBeforeCost(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual formal PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadBattleConfiguration(a, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint32
	for _, phase := range c.PhaseDungeons {
		for _, d := range phase {
			ids = append(ids, d.ID)
		}
	}
	if err := ValidateDungeonDependencies(a, ids); err != nil {
		t.Fatal("formal normal two phases", err)
	}
	if err := ValidateDungeonDependencies(missingRaidMapSource{a}, ids); err == nil {
		t.Fatal("unreadable formal room map accepted")
	}
	for route := byte(0); route < 2; route++ {
		for week := 0; week < 2; week++ {
			seq, err := LoadSingleRaidSequence(a, route, week)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateDungeonDependencies(a, seq)
			if err == nil || !strings.Contains(err.Error(), "absent indexed map") {
				t.Fatal("unindexed actual single room must fail before cost", route, week, err)
			}
			t.Logf("actual route=%d weekly=%d refuses unindexed room: %v", route, week, err)
		}
	}
	options, err := SingleRaidWorldMapOptions(a)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range options.SupplementalDungeonIDs {
		dungeon, ok := table.FindDungeon(id)
		if !ok || len(dungeon.Mazes) == 0 {
			t.Fatal("actual supplemental map not in gameplay table", id)
		}
	}
	if _, err := worldmap.NewResolver(table); !errors.Is(err, worldmap.ErrMapReferenceMissing) {
		t.Fatal("formal unindexed rooms should block resolver rather than fabricate maps", err)
	}
	if _, err := ValidateSingleRaidDependencies(a); err == nil {
		t.Fatal("incomplete single compatibility catalog admitted")
	}
	for route := byte(0); route < 2; route++ {
		options, err := SingleRaidWorldMapOptionsForRoute(a, route)
		if err != nil || len(options.SupplementalDungeonIDs) == 0 {
			t.Fatal("actual family options", route, err)
		}
		for _, id := range options.SupplementalDungeonIDs {
			if (route == 0 && id >= 5323) || (route == 1 && id < 5323) {
				t.Fatal("single family owns other route", route, id)
			}
		}
		if _, err := ValidateSingleRaidDependenciesForRoute(a, route); err == nil {
			t.Fatal("unindexed family admitted", route)
		}
	}
	if _, err := SingleRaidWorldMapOptionsForRoute(a, 2); err == nil {
		t.Fatal("unknown route admitted")
	}
	if err := ValidateDungeonDependencies(a, []uint32{0}); err == nil {
		t.Fatal("zero dungeon accepted")
	}
	if err := ValidateDungeonDependencies(a, nil); err == nil {
		t.Fatal("empty raid admitted")
	}
}
