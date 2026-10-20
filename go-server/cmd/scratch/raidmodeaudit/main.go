// raidmodeaudit records real Luke/Fiend War dependencies without mutating assets.
package main

import (
	"crypto/sha256"
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

type dependency struct {
	ID      uint32  `json:"id"`
	Path    string  `json:"path"`
	Maps    []int64 `json:"maps"`
	Missing []int64 `json:"missing_maps"`
	Error   string  `json:"error,omitempty"`
}

func main() {
	in := flag.String("pvf", "", "existing PVF")
	out := flag.String("out", "", "external JSON report")
	flag.Parse()
	a, err := platformpvf.Open(*in)
	if err != nil {
		panic(err)
	}
	load := func(name string) map[int64]string {
		text, err := a.ReadText(name)
		if err != nil {
			panic(err)
		}
		doc, err := dnfpvf.Parse(name, text)
		if err != nil {
			panic(err)
		}
		m := map[int64]string{}
		for _, e := range dnfpvf.ParseList(doc) {
			m[e.ID] = e.Path
		}
		return m
	}
	dungeons, tutorial, maps, alternate := load("dungeon/dungeon.lst"), load("dungeon/tutorial/dungeon.lst"), load("map/map.lst"), load("map/map.jpn.lst")
	groups := map[string][]uint32{}
	for _, route := range []byte{1} {
		for count := 0; count < 2; count++ {
			ids, err := raid.LoadSingleRaidSequence(a, route, count)
			if err != nil {
				panic(err)
			}
			groups[fmt.Sprintf("luke_single_%d", count)] = ids
		}
	}
	for name, ref := range map[string]string{"luke_normal": "etc/raid/luke/luke.etc", "fiendwar": "etc/raid/fiendwar/fiendwar.etc"} {
		c, err := raid.LoadBattleConfiguration(a, ref)
		if err != nil {
			panic(err)
		}
		if name == "fiendwar" {
			text, err := a.ReadText(ref)
			if err != nil {
				panic(err)
			}
			doc, err := dnfpvf.Parse(ref, text)
			if err != nil {
				panic(err)
			}
			seen := map[uint32]bool{}
			for _, id := range groups[name] {
				seen[id] = true
			}
			for _, section := range doc.Sections {
				if !strings.EqualFold(section.Name, "DUNGEON INDEX") {
					continue
				}
				for _, token := range doc.Tokens[section.Start:section.End] {
					if token.Kind == dnfpvf.TokenInt && token.Int > 0 && token.Int <= int64(^uint32(0)) && !seen[uint32(token.Int)] {
						groups[name] = append(groups[name], uint32(token.Int))
						seen[uint32(token.Int)] = true
					}
				}
			}
		}
		for _, phase := range c.PhaseDungeons {
			for _, d := range phase {
				groups[name] = append(groups[name], d.ID)
			}
		}
	}
	missing := map[int64]bool{}
	mapAudit := map[int64]map[string]any{}
	result := map[string][]dependency{}
	for name, ids := range groups {
		for _, id := range ids {
			ref := dungeons[int64(id)]
			if ref == "" {
				ref = tutorial[int64(id)]
			}
			if !strings.HasPrefix(strings.ToLower(ref), "dungeon/") {
				ref = path.Join("dungeon", ref)
			}
			r := dependency{ID: id, Path: ref}
			text, err := a.ReadText(ref)
			if err != nil {
				r.Error = err.Error()
				result[name] = append(result[name], r)
				continue
			}
			doc, err := dnfpvf.Parse(ref, text)
			if err != nil {
				panic(err)
			}
			d := worldmap.ParseDungeon(int64(id), ref, doc)
			seen := map[int64]bool{}
			for _, maze := range d.Mazes {
				for _, family := range [][]worldmap.MapSpecification{maze.MapSpecifications, maze.BossSpecifications, maze.LayeredSpecifications} {
					for _, spec := range family {
						for _, mapID := range spec.MapIDs {
							seen[mapID] = true
						}
					}
				}
			}
			for mapID := range seen {
				r.Maps = append(r.Maps, mapID)
				if maps[mapID] == "" {
					r.Missing = append(r.Missing, mapID)
					missing[mapID] = true
				} else if mapAudit[mapID] == nil {
					ref := maps[mapID]
					if !strings.HasPrefix(strings.ToLower(ref), "map/") {
						ref = path.Join("map", ref)
					}
					text, err := a.ReadText(ref)
					if err != nil {
						panic(err)
					}
					doc, err := dnfpvf.Parse(ref, text)
					if err != nil {
						panic(err)
					}
					m := worldmap.ParseMap(mapID, ref, doc)
					mapAudit[mapID] = map[string]any{"path": ref, "pathgate_range_present": m.PathgateRecognizeRange.Set, "pathgate_range_values": doc.Ints("pathgate recognize range"), "pathgate_positions": len(m.PathgatePositions)}
				}
			}
			sort.Slice(r.Maps, func(i, j int) bool { return r.Maps[i] < r.Maps[j] })
			sort.Slice(r.Missing, func(i, j int) bool { return r.Missing[i] < r.Missing[j] })
			result[name] = append(result[name], r)
		}
	}
	candidates := map[int64][]string{}
	var singleMaps []map[string]any
	for _, file := range a.Files() {
		ref := strings.ToLower(strings.ReplaceAll(file.ArchivePath, "\\", "/"))
		if !strings.HasSuffix(ref, ".map") {
			continue
		}
		for id := range missing {
			if strings.Contains(path.Base(ref), fmt.Sprint(id)) {
				candidates[id] = append(candidates[id], ref)
			}
		}
		if !strings.HasPrefix(ref, "map/luke_single/") {
			continue
		}
		text, err := a.ReadText(ref)
		if err != nil {
			panic(err)
		}
		doc, err := dnfpvf.Parse(ref, text)
		if err != nil {
			panic(err)
		}
		d := worldmap.ParseMap(0, ref, doc)
		sum := sha256.Sum256([]byte(text))
		singleMaps = append(singleMaps, map[string]any{"path": ref, "dungeon": d.DungeonID, "sha256": fmt.Sprintf("%x", sum)})
	}
	alt := map[int64]string{}
	for id := range missing {
		if alternate[id] != "" {
			alt[id] = alternate[id]
		}
	}
	b, err := json.MarshalIndent(map[string]any{"read_only": true, "pvf": a.Snapshot(), "dependencies": result, "numeric_candidates": candidates, "alternate_index": alt, "actual_single_maps": singleMaps, "indexed_maps": mapAudit}, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(*out, b, 0600); err != nil {
		panic(err)
	}
	fmt.Printf("audit saved; missing map IDs=%d; actual Luke single MAP files=%d\n", len(missing), len(singleMaps))
}
