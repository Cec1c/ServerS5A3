package raid

import (
	"fmt"
	"path"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func actualRaidIndex(source dnfpvf.Source, name string) (map[int64]string, error) {
	text, err := source.ReadText(name)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(name, text)
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, entry := range dnfpvf.ParseList(doc) {
		if entry.ID <= 0 || out[entry.ID] != "" {
			return nil, fmt.Errorf("invalid/repeated raid index %s/%d", name, entry.ID)
		}
		out[entry.ID] = strings.ToLower(strings.ReplaceAll(entry.Path, "\\", "/"))
	}
	return out, nil
}

func actualRaidDocument(source dnfpvf.Source, root, ref string) (string, *dnfpvf.Document, error) {
	if !strings.HasPrefix(ref, root+"/") {
		ref = path.Join(root, ref)
	}
	text, err := source.ReadText(ref)
	if err != nil {
		return ref, nil, err
	}
	doc, err := dnfpvf.Parse(ref, text)
	if err == nil && len(doc.Tokens) == 0 {
		err = fmt.Errorf("empty actual raid dependency %s", ref)
	}
	return ref, doc, err
}

// Match the primary map index and the actual selectively loaded tutorial
// dungeon index. Raw unindexed files cannot authorize entry: the current
// client also requires the map index. Missing dependencies fail before cost.
func ValidateDungeonDependencies(source dnfpvf.Source, ids []uint32) error {
	if source == nil || len(ids) == 0 {
		return fmt.Errorf("raid dungeon dependencies unavailable")
	}
	dungeons, err := actualRaidIndex(source, "dungeon/dungeon.lst")
	if err != nil {
		return err
	}
	maps, err := actualRaidIndex(source, "map/map.lst")
	if err != nil {
		return err
	}
	var supplemental map[int64]string
	seen := map[uint32]bool{}
	for _, id := range ids {
		if id == 0 {
			return fmt.Errorf("raid dungeon zero is invalid")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ref := dungeons[int64(id)]
		if ref == "" {
			if supplemental == nil {
				supplemental, err = actualRaidIndex(source, "dungeon/tutorial/dungeon.lst")
				if err != nil {
					return err
				}
			}
			ref = supplemental[int64(id)]
		}
		if ref == "" {
			return fmt.Errorf("raid dungeon %d absent from actual dungeon indexes", id)
		}
		ref, doc, err := actualRaidDocument(source, "dungeon", ref)
		if err != nil {
			return fmt.Errorf("raid dungeon %d: %w", id, err)
		}
		dungeon := worldmap.ParseDungeon(int64(id), ref, doc)
		if len(dungeon.Mazes) == 0 {
			return fmt.Errorf("raid dungeon %d has no parsed maze", id)
		}
		for n, maze := range dungeon.Mazes {
			refs := map[int64]bool{}
			for _, family := range [][]worldmap.MapSpecification{maze.MapSpecifications, maze.BossSpecifications, maze.LayeredSpecifications} {
				for _, spec := range family {
					for _, mapID := range spec.MapIDs {
						refs[mapID] = true
					}
				}
			}
			if len(refs) == 0 {
				return fmt.Errorf("raid dungeon %d maze %d has no explicit map dependency", id, n)
			}
			var sorted []int64
			for mapID := range refs {
				sorted = append(sorted, mapID)
			}
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
			for _, mapID := range sorted {
				ref := maps[mapID]
				if mapID <= 0 || ref == "" {
					return fmt.Errorf("raid dungeon %d references absent indexed map %d", id, mapID)
				}
				if _, _, err := actualRaidDocument(source, "map", ref); err != nil {
					return fmt.Errorf("raid dungeon %d map %d: %w", id, mapID, err)
				}
			}
		}
	}
	return nil
}

// The tutorial list refers to paths under archive-root dungeon/, and includes
// unrelated obsolete entries. Load only IDs used by the formal raid links.
func SingleRaidWorldMapOptions(source dnfpvf.Source) (worldmap.Options, error) {
	return singleRaidWorldMapOptions(source, []byte{0, 1})
}

// Each raid family owns its dependency failure. An unavailable Luke single
// family cannot disable valid Anton assets, and vice versa.
func SingleRaidWorldMapOptionsForRoute(source dnfpvf.Source, route byte) (worldmap.Options, error) {
	if route > 1 {
		return worldmap.Options{}, fmt.Errorf("single raid route invalid")
	}
	return singleRaidWorldMapOptions(source, []byte{route})
}

func singleRaidWorldMapOptions(source dnfpvf.Source, routes []byte) (worldmap.Options, error) {
	seen := map[uint32]bool{}
	for _, route := range routes {
		for weekly := 0; weekly < 2; weekly++ {
			sequence, err := LoadSingleRaidSequence(source, route, weekly)
			if err != nil {
				return worldmap.Options{}, err
			}
			for _, id := range sequence {
				seen[id] = true
			}
		}
	}
	options := worldmap.Options{SupplementalDungeonListPath: "dungeon/tutorial/dungeon.lst", SupplementalDungeonReferenceRoot: "dungeon"}
	for id := range seen {
		options.SupplementalDungeonIDs = append(options.SupplementalDungeonIDs, int64(id))
	}
	sort.Slice(options.SupplementalDungeonIDs, func(i, j int) bool { return options.SupplementalDungeonIDs[i] < options.SupplementalDungeonIDs[j] })
	return options, nil
}

func ValidateSingleRaidDependencies(source dnfpvf.Source) (worldmap.Options, error) {
	options, err := SingleRaidWorldMapOptions(source)
	return validateSingleRaidOptions(source, options, err)
}

func ValidateSingleRaidDependenciesForRoute(source dnfpvf.Source, route byte) (worldmap.Options, error) {
	options, err := SingleRaidWorldMapOptionsForRoute(source, route)
	return validateSingleRaidOptions(source, options, err)
}

func validateSingleRaidOptions(source dnfpvf.Source, options worldmap.Options, err error) (worldmap.Options, error) {
	if err != nil {
		return worldmap.Options{}, err
	}
	var ids []uint32
	for _, id := range options.SupplementalDungeonIDs {
		ids = append(ids, uint32(id))
	}
	if err := ValidateDungeonDependencies(source, ids); err != nil {
		return worldmap.Options{}, err
	}
	return options, nil
}
