// raiddependencies is a read-only audit of the actual raid configuration and
// its dungeon/map references. It never modifies PVF, accounts or game state.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type audit struct {
	Mode       string                            `json:"mode"`
	DungeonID  uint32                            `json:"dungeon_id"`
	Path       string                            `json:"path,omitempty"`
	Name       string                            `json:"name,omitempty"`
	MapCount   int                               `json:"map_count"`
	Maps       []int64                           `json:"maps,omitempty"`
	Randomized []worldmap.RandomizedObjectScript `json:"randomized,omitempty"`
	Error      string                            `json:"error,omitempty"`
}

func main() {
	pvfPath := flag.String("pvf", "", "actual Script.pvf (read only)")
	flag.Parse()
	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	loadList := func(name string) map[int64]string {
		text, err := archive.ReadText(name)
		if err != nil {
			panic(err)
		}
		doc, err := dnfpvf.Parse(name, text)
		if err != nil {
			panic(err)
		}
		out := map[int64]string{}
		for _, entry := range dnfpvf.ParseList(doc) {
			ref := strings.ToLower(strings.ReplaceAll(entry.Path, "\\", "/"))
			if archive.FindFileIndex(ref) < 0 {
				ref = path.Join(path.Dir(name), ref)
			}
			out[entry.ID] = ref
		}
		return out
	}
	dungeons, maps := loadList("dungeon/dungeon.lst"), loadList("map/map.lst")
	var results []audit
	check := func(mode string, id uint32) {
		row := audit{Mode: mode, DungeonID: id, Path: dungeons[int64(id)]}
		if row.Path == "" {
			row.Error = "configured ID is absent from actual dungeon/dungeon.lst"
			results = append(results, row)
			return
		}
		text, err := archive.ReadText(row.Path)
		if err != nil {
			row.Error = err.Error()
			results = append(results, row)
			return
		}
		doc, err := dnfpvf.Parse(row.Path, text)
		if err != nil {
			row.Error = err.Error()
			results = append(results, row)
			return
		}
		dungeon := worldmap.ParseDungeon(int64(id), row.Path, doc)
		row.Name = dungeon.Metadata.Name
		seen := map[int64]bool{}
		for _, maze := range dungeon.Mazes {
			for _, spec := range maze.MapSpecifications {
				for _, mapID := range spec.MapIDs {
					seen[mapID] = true
				}
			}
			for _, spec := range maze.BossSpecifications {
				for _, mapID := range spec.MapIDs {
					seen[mapID] = true
				}
			}
			for _, spec := range maze.LayeredSpecifications {
				for _, mapID := range spec.MapIDs {
					seen[mapID] = true
				}
			}
			row.Randomized = append(row.Randomized, maze.RandomizedObjects...)
		}
		for id := range seen {
			row.Maps = append(row.Maps, id)
			if ref := maps[id]; ref == "" || archive.FindFileIndex(ref) < 0 {
				row.Error += fmt.Sprintf(" missing map %d;", id)
			}
		}
		sort.Slice(row.Maps, func(i, j int) bool { return row.Maps[i] < row.Maps[j] })
		row.MapCount = len(row.Maps)
		if len(row.Maps) == 0 {
			row.Error += " no parsed dungeon maps"
		}
		results = append(results, row)
	}
	config, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		panic(err)
	}
	for phase, entries := range config.PhaseDungeons {
		for _, d := range entries {
			check(fmt.Sprintf("normal-phase%d", phase), d.ID)
		}
	}
	for route := byte(0); route < 2; route++ {
		for weekly := 0; weekly < 2; weekly++ {
			sequence, err := raid.LoadSingleRaidSequence(archive, route, weekly)
			if err != nil {
				panic(err)
			}
			for _, id := range sequence {
				check(fmt.Sprintf("single-route%d-weekly%d", route, weekly), id)
			}
		}
	}
	var singlePaths []string
	var mapIndexes []string
	for _, file := range archive.Files() {
		p := strings.ToLower(file.ArchivePath)
		if strings.HasPrefix(p, "map/") && strings.HasSuffix(p, ".lst") {
			mapIndexes = append(mapIndexes, p)
		}
		if (strings.HasPrefix(p, "dungeon/") && (strings.Contains(p, "anton_new") || strings.Contains(p, "single") || strings.Contains(p, "solo") || strings.Contains(p, "5314"))) || (strings.HasPrefix(p, "map/anton_new/") && strings.HasSuffix(p, ".map")) {
			singlePaths = append(singlePaths, p)
		}
	}
	json.NewEncoder(os.Stdout).Encode(struct {
		ReadOnly    bool                        `json:"read_only"`
		Snapshot    platformpvf.ArchiveSnapshot `json:"actual_pvf"`
		Dungeons    []audit                     `json:"dungeons"`
		SinglePaths []string                    `json:"single_paths"`
		MapIndexes  []string                    `json:"map_indexes"`
	}{true, archive.Snapshot(), results, singlePaths, mapIndexes})
}
