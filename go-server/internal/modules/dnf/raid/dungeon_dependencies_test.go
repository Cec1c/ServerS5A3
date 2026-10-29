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

// Keep real dungeon and map documents while removing their index authority.
// This makes the negative case independent of which maps a PVF version contains.
type missingRaidMapIndexSource struct {
	dnfpvf.Source
	index string
}

func (s missingRaidMapIndexSource) ReadText(path string) (string, error) {
	if strings.EqualFold(strings.ReplaceAll(path, "\\", "/"), "map/map.lst") {
		return s.index, nil
	}
	return s.Source.ReadText(path)
}

func withoutSingleRaidMapIndexes(t *testing.T, source dnfpvf.Source) missingRaidMapIndexSource {
	t.Helper()
	options, err := SingleRaidWorldMapOptions(source)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), source, options)
	if err != nil {
		t.Fatal(err)
	}
	removed := map[int64]bool{}
	for _, id := range options.SupplementalDungeonIDs {
		dungeon, ok := table.FindDungeon(id)
		if !ok || len(dungeon.Mazes) == 0 {
			t.Fatal("actual single dungeon unavailable", id)
		}
		for _, maze := range dungeon.Mazes {
			for _, family := range [][]worldmap.MapSpecification{maze.MapSpecifications, maze.BossSpecifications, maze.LayeredSpecifications} {
				for _, spec := range family {
					for _, mapID := range spec.MapIDs {
						removed[mapID] = true
					}
				}
			}
		}
	}
	text, err := source.ReadText("map/map.lst")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("map/map.lst", text)
	if err != nil {
		t.Fatal(err)
	}
	var index strings.Builder
	index.WriteString("#PVF_File\n")
	kept, omitted := 0, 0
	for _, entry := range dnfpvf.ParseList(doc) {
		if removed[entry.ID] {
			omitted++
			continue
		}
		fmt.Fprintf(&index, "%d\t`%s`\n", entry.ID, entry.Path)
		kept++
	}
	if omitted == 0 || kept == 0 {
		t.Fatal("fault fixture must omit actual single maps and retain real unrelated maps", omitted, kept)
	}
	return missingRaidMapIndexSource{Source: source, index: index.String()}
}

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
	missingIndex := withoutSingleRaidMapIndexes(t, a)
	for route := byte(0); route < 2; route++ {
		for week := 0; week < 2; week++ {
			seq, err := LoadSingleRaidSequence(a, route, week)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateDungeonDependencies(a, seq); err != nil {
				t.Fatal("actual indexed single rooms must be available", route, week, err)
			}
			err = ValidateDungeonDependencies(missingIndex, seq)
			if err == nil || !strings.Contains(err.Error(), "absent indexed map") {
				t.Fatal("unindexed real single room must fail before cost", route, week, err)
			}
			if err := ValidateDungeonDependencies(missingRaidMapSource{a}, seq); err == nil {
				t.Fatal("unreadable real single room accepted", route, week)
			}
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
	if _, err := worldmap.NewResolver(table); err != nil {
		t.Fatal("actual indexed rooms must build a resolver", err)
	}
	if _, err := ValidateSingleRaidDependencies(a); err != nil {
		t.Fatal("actual single compatibility catalog unavailable", err)
	}
	missingTable, err := worldmap.LoadSource(context.Background(), missingIndex, options)
	if err != nil {
		t.Fatal("index fault must retain the actual dungeon documents", err)
	}
	if _, err := worldmap.NewResolver(missingTable); !errors.Is(err, worldmap.ErrMapReferenceMissing) {
		t.Fatal("unindexed rooms should block resolver rather than fabricate maps", err)
	}
	if _, err := ValidateSingleRaidDependencies(missingIndex); err == nil {
		t.Fatal("injected incomplete single compatibility catalog admitted")
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
		if _, err := ValidateSingleRaidDependenciesForRoute(a, route); err != nil {
			t.Fatal("actual indexed family unavailable", route, err)
		}
		if _, err := ValidateSingleRaidDependenciesForRoute(missingIndex, route); err == nil {
			t.Fatal("injected unindexed family admitted", route)
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
