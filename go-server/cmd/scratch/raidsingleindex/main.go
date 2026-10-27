// Offline index repair of existing formal single-raid assets. This does not
// install a runtime fallback or synthesize DGN/MAP contents.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type indexEntry struct {
	ID             int64  `json:"id"`
	Path           string `json:"path"`
	Evidence       string `json:"evidence"`
	DocumentSHA256 string `json:"document_sha256"`
}

func main() {
	input := flag.String("input", "", "formal PVF")
	output := flag.String("output", "", "separate candidate PVF; empty means audit only")
	report := flag.String("report", "", "manifest path")
	route := flag.Uint("route", 0, "actual route 0 Anton, 1 Luke; families remain independent")
	flag.Parse()
	if *route > 1 {
		fmt.Fprintln(os.Stderr, "invalid raid route")
		os.Exit(1)
	}
	if err := run(*input, *output, *report, byte(*route)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input, output, report string, route byte) error {
	if input == "" || report == "" {
		return fmt.Errorf("input and report required")
	}
	in, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	if output != "" {
		out, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		if strings.EqualFold(in, out) {
			return fmt.Errorf("input PVF must remain unchanged")
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			return fmt.Errorf("candidate output already exists or unavailable")
		}
	}
	a, err := platformpvf.Open(in)
	if err != nil {
		return err
	}
	loadList := func(name string) (map[int64]string, error) {
		text, err := a.ReadText(name)
		if err != nil {
			return nil, err
		}
		doc, err := dnfpvf.Parse(name, text)
		if err != nil {
			return nil, err
		}
		values := map[int64]string{}
		for _, e := range dnfpvf.ParseList(doc) {
			if e.ID <= 0 || values[e.ID] != "" {
				return nil, fmt.Errorf("invalid index %s/%d", name, e.ID)
			}
			values[e.ID] = strings.ToLower(strings.ReplaceAll(e.Path, "\\", "/"))
		}
		return values, nil
	}
	primary, err := loadList("dungeon/dungeon.lst")
	if err != nil {
		return err
	}
	tutorial, err := loadList("dungeon/tutorial/dungeon.lst")
	if err != nil {
		return err
	}
	maps, err := loadList("map/map.lst")
	if err != nil {
		return err
	}
	options, err := raid.SingleRaidWorldMapOptionsForRoute(a, route)
	if err != nil {
		return err
	}
	var dgnEntries, mapEntries []indexEntry
	var nativeDoorIssues []string
	refs := map[int64]bool{}
	for _, id := range options.SupplementalDungeonIDs {
		ref := primary[id]
		if ref == "" {
			ref = tutorial[id]
		}
		if ref == "" {
			return fmt.Errorf("actual single DGN %d absent", id)
		}
		full := ref
		if !strings.HasPrefix(full, "dungeon/") {
			full = path.Join("dungeon", full)
		}
		text, err := a.ReadText(full)
		if err != nil {
			return err
		}
		doc, err := dnfpvf.Parse(full, text)
		if err != nil {
			return err
		}
		dungeon := worldmap.ParseDungeon(id, full, doc)
		if len(dungeon.Mazes) == 0 {
			return fmt.Errorf("DGN %d missing maze", id)
		}
		if primary[id] == "" {
			sum := sha256.Sum256([]byte(text))
			dgnEntries = append(dgnEntries, indexEntry{id, strings.TrimPrefix(full, "dungeon/"), "Existing tutorial index, actual DGN file, root dungeon/", fmt.Sprintf("%x", sum)})
		}
		for _, maze := range dungeon.Mazes {
			for _, family := range [][]worldmap.MapSpecification{maze.MapSpecifications, maze.BossSpecifications, maze.LayeredSpecifications} {
				for _, spec := range family {
					for _, mapID := range spec.MapIDs {
						refs[mapID] = true
					}
				}
			}
		}
	}
	// Numeric filenames are accepted only inside the actual single families.
	// 93029 in unrelated families therefore cannot substitute AntonHeart.
	candidates := map[int64][]string{}
	for _, file := range a.Files() {
		ref := strings.ToLower(strings.ReplaceAll(file.ArchivePath, "\\", "/"))
		if !strings.HasPrefix(ref, "map/anton_new/") && !strings.HasPrefix(ref, "map/luke_single/") {
			continue
		}
		if !strings.HasSuffix(ref, ".map") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSuffix(path.Base(ref), ".map"), 10, 64)
		if err == nil {
			candidates[id] = append(candidates[id], ref)
		}
	}
	// These reviewed aliases are existing archive files. Their [dungeon]
	// ownership and role are checked below, rather than cloning another map.
	aliases := map[int64]struct {
		file  string
		owner int64
		role  string
	}{
		292101819: {"292101819_1s.map", 5321, "start"}, 292101820: {"292101820_1l.map", 5321, "ordinary"}, 292101821: {"292101811_1boos.map", 5321, "boss"},
		292101822: {"292101819_2s.map", 5322, "start"}, 292101823: {"292101820_2l.map", 5322, "ordinary"}, 292101824: {"292101811_2boos.map", 5322, "boss"},
	}
	var ids []int64
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("invalid map reference")
		}
		if maps[id] != "" {
			continue
		}
		paths := candidates[id]
		evidence := "Exact numeric filename inside actual single family"
		alias, isAlias := aliases[id]
		if isAlias {
			paths = []string{"map/anton_new/energycut/" + alias.file}
			evidence = fmt.Sprintf("Existing energy variant; owner %d, role %s; actual DGN references this map ID", alias.owner, alias.role)
		}
		lukeAlias, isLukeAlias := lukeSingleAliases()[id]
		if route == 1 && isLukeAlias {
			paths = []string{lukeAlias.file}
			evidence = fmt.Sprintf("Existing named Luke single room %d in DGN %d; explicit DGN map ID and numbered room order; original MAP owner %d retained", lukeAlias.room, lukeAlias.dungeon, lukeAlias.originalOwner)
		}
		if len(paths) != 1 {
			return fmt.Errorf("map %d has %d strict existing candidates", id, len(paths))
		}
		text, err := a.ReadText(paths[0])
		if err != nil {
			return err
		}
		doc, err := dnfpvf.Parse(paths[0], text)
		if err != nil || len(doc.Tokens) == 0 {
			return fmt.Errorf("map %d unreadable", id)
		}
		if isAlias {
			m := worldmap.ParseMap(id, paths[0], doc)
			if !m.DungeonID.Set || m.DungeonID.Value != alias.owner {
				return fmt.Errorf("map alias %d owner conflicts with original source", id)
			}
			boss := false
			for _, spawn := range m.Monsters {
				boss = boss || strings.Contains(strings.ToLower(spawn.Rank), "boss") || strings.Contains(strings.ToLower(spawn.SuffixMarker), "boss")
			}
			if (alias.role == "boss" && !boss) || (alias.role == "start" && len(m.Monsters) != 0) || (alias.role == "ordinary" && (len(m.Monsters) == 0 || boss)) {
				return fmt.Errorf("map alias %d role conflicts with actual source", id)
			}
		}
		if route == 1 && isLukeAlias {
			if err := validateLukeAlias(id, lukeAlias, doc); err != nil {
				nativeDoorIssues = append(nativeDoorIssues, err.Error())
			}
		}
		sum := sha256.Sum256([]byte(text))
		mapEntries = append(mapEntries, indexEntry{id, strings.TrimPrefix(paths[0], "map/"), evidence, fmt.Sprintf("%x", sum)})
	}
	result := map[string]any{"read_only": output == "", "deployed": false, "native_ready": false, "input": in, "route": route, "dungeon_entries": dgnEntries, "map_entries": mapEntries, "unchanged_asset_contents": true, "native_door_issues": nativeDoorIssues}
	if output != "" {
		if len(nativeDoorIssues) != 0 {
			return fmt.Errorf("native room defects require investigation before writing candidate: %v", nativeDoorIssues)
		}
		if len(dgnEntries) == 0 || len(mapEntries) == 0 {
			return fmt.Errorf("expected missing primary indexes absent; refuse redundant repair")
		}
		var additions []platformpvf.ScriptAddition
		for _, entry := range []struct {
			name   string
			values []indexEntry
		}{{"dungeon/dungeon.lst", dgnEntries}, {"map/map.lst", mapEntries}} {
			change := platformpvf.ScriptAddition{Path: entry.name, TemplatePath: entry.name, AppendToExisting: true}
			for _, value := range entry.values {
				change.AppendTokens = append(change.AppendTokens, platformpvf.ScriptToken{Type: 0, Value: int32(value.ID)}, platformpvf.ScriptToken{Type: 6, Text: value.Path})
			}
			additions = append(additions, change)
		}
		data, err := a.RepackScriptAdditions(additions)
		if err != nil {
			return err
		}
		if err := os.WriteFile(output, data, 0600); err != nil {
			return err
		}
		next, err := platformpvf.Open(output)
		if err != nil {
			return err
		}
		unchanged, err := a.VerifyScriptReplacementPreservation(next, []string{"dungeon/dungeon.lst", "map/map.lst"})
		if err != nil {
			return err
		}
		if _, err := raid.ValidateSingleRaidDependenciesForRoute(next, route); err != nil {
			return err
		}
		table, err := worldmap.LoadSource(context.Background(), next, worldmap.Options{})
		if err != nil {
			return err
		}
		resolver, err := worldmap.NewResolver(table)
		if err != nil {
			return err
		}
		for _, id := range options.SupplementalDungeonIDs {
			if _, err := worldmap.BuildDungeonLayout(resolver, id, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil }); err != nil {
				return fmt.Errorf("repaired actual DGN %d layout: %w", id, err)
			}
		}
		sum := sha256.Sum256(data)
		result["output"] = output
		result["output_sha256"] = fmt.Sprintf("%x", sum)
		result["unchanged_archive_files"] = unchanged
		result["strict_single_dependencies_and_layouts_pass"] = true
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(report, data, 0600); err != nil {
		return err
	}
	fmt.Printf("single index audit: %d DGN, %d MAP; candidate_written=%t\n", len(dgnEntries), len(mapEntries), output != "")
	return nil
}
