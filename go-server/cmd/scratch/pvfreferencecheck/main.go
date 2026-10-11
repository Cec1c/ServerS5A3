// pvfreferencecheck reads real PVF references without changing any archive or runtime.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func main() {
	pvf := flag.String("pvf", "", "archive to inspect")
	out := flag.String("out", "", "new report directory outside the source tree")
	flag.Parse()
	if *pvf == "" || *out == "" {
		panic("pvf and out required")
	}
	if err := os.Mkdir(*out, 0755); err != nil {
		panic(err)
	}
	a, err := platformpvf.Open(*pvf)
	if err != nil {
		panic(err)
	}
	result := map[string]any{"pvf": *pvf, "archive": a.Snapshot(), "started": time.Now().Format(time.RFC3339)}
	mapList, err := a.ReadText("map/map.lst")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "map.lst.txt"), []byte(mapList), 0644); err != nil {
		panic(err)
	}
	var files []platformpvf.File
	for _, f := range a.Files() {
		if strings.Contains(f.ArchivePath, "59453") || strings.Contains(f.Name, "59453") {
			files = append(files, f)
		}
	}
	result["archive_files_containing_59453"] = files
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		result["load_error"] = err.Error()
	} else {
		result["worldmap"] = table.Snapshot()
		_, exists := table.FindMap(59453)
		result["map59453_loaded"] = exists
		var missing []map[string]any
		missingIDs := map[int64]bool{}
		for _, d := range table.Dungeons() {
			if d.ID == 310 {
				text, err := a.ReadText(d.Path)
				if err != nil {
					panic(err)
				}
				if err := os.WriteFile(filepath.Join(*out, "dungeon310.txt"), []byte(text), 0644); err != nil {
					panic(err)
				}
				result["dungeon310_path"] = d.Path
			}
			for _, maze := range d.Mazes {
				specs := append(append(append([]worldmap.MapSpecification{}, maze.MapSpecifications...), maze.BossSpecifications...), maze.LayeredSpecifications...)
				for _, s := range specs {
					for _, id := range s.MapIDs {
						if _, ok := table.FindMap(id); !ok {
							missingIDs[id] = true
							missing = append(missing, map[string]any{"dungeon": d.ID, "path": d.Path, "maze": maze.Index, "type": s.Type, "x": s.Coordinate.X, "y": s.Coordinate.Y, "map": id})
						}
					}
				}
			}
		}
		result["missing_specification_references"] = missing
		result["missing_specification_count"] = len(missing)
		ids := make([]int64, 0, len(missingIDs))
		for id := range missingIDs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		result["missing_unique_ids"] = ids
		candidates := map[string][]platformpvf.File{}
		numbers := regexp.MustCompile(`[0-9]+`)
		for _, f := range a.Files() {
			if !strings.HasPrefix(strings.ToLower(f.ArchivePath), "map/") || !strings.HasSuffix(strings.ToLower(f.ArchivePath), ".map") {
				continue
			}
			for _, token := range numbers.FindAllString(f.Name, -1) {
				id, err := strconv.ParseInt(token, 10, 64)
				if err == nil && missingIDs[id] {
					candidates[token] = append(candidates[token], f)
				}
			}
		}
		result["candidate_map_files"] = candidates
		pool := map[string]bool{}
		for _, id := range ids {
			key := strconv.FormatInt(id, 10)
			if len(candidates[key]) != 1 {
				continue
			}
			f := candidates[key][0]
			_, found := a.LookupStringMagic(strings.TrimPrefix(f.ArchivePath, "map/"))
			pool[key] = found
			text, err := a.FileText(f.Index)
			if err != nil {
				panic(err)
			}
			if err := os.WriteFile(filepath.Join(*out, "candidate-"+key+".txt"), []byte(text), 0644); err != nil {
				panic(err)
			}
			raw, err := a.FileRaw(f.Index)
			if err != nil {
				panic(err)
			}
			result["candidate_raw_sha256_"+key] = fmt.Sprintf("%x", sha256.Sum256(raw))
		}
		result["candidate_lowercase_path_in_ansi_pool"] = pool
		if _, err := worldmap.NewResolver(table); err != nil {
			result["resolver_error"] = err.Error()
		} else {
			result["resolver_ok"] = true
		}
	}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "report.json"), b, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("report=%s missing_references=%v unique_ids=%v resolver_error=%v resolver_ok=%v\n", filepath.Join(*out, "report.json"), result["missing_specification_count"], result["missing_unique_ids"], result["resolver_error"], result["resolver_ok"])
}
