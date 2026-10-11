// pvfmaplistrepair appends explicitly evidenced map references to a new PVF.
// It preserves all original tokens, requires real map files and hashes, and
// verifies every archive entry before accepting the output.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type entry struct {
	ID           int64  `json:"id"`
	Path         string `json:"path"`
	Evidence     string `json:"evidence"`
	MapRawSHA256 string `json:"map_raw_sha256"`
}
type plan struct {
	InputSHA256 string  `json:"input_sha256"`
	Entries     []entry `json:"entries"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func main() {
	input := flag.String("pvf", "", "original archive, never modified")
	planPath := flag.String("plan", "", "explicit ID/path/hash evidence plan")
	output := flag.String("out", "", "new archive; existing files are rejected")
	flag.Parse()
	if *input == "" || *planPath == "" || *output == "" {
		panic("pvf, plan and out required")
	}
	inputAbs, err := filepath.Abs(*input)
	must(err)
	outputAbs, err := filepath.Abs(*output)
	must(err)
	if strings.EqualFold(inputAbs, outputAbs) {
		panic("output must differ from input")
	}
	if _, err := os.Stat(outputAbs); !os.IsNotExist(err) {
		panic("output already exists or cannot be inspected")
	}
	planBytes, err := os.ReadFile(*planPath)
	must(err)
	var p plan
	must(json.Unmarshal(planBytes, &p))
	if len(p.InputSHA256) != 64 || len(p.Entries) == 0 {
		panic("invalid plan")
	}
	originalBytes, err := os.ReadFile(inputAbs)
	must(err)
	if digest(originalBytes) != p.InputSHA256 {
		panic("original archive differs from plan")
	}
	a, err := platformpvf.OpenBytes(originalBytes)
	must(err)
	text, err := a.ReadText("map/map.lst")
	must(err)
	doc, err := worldmap.ParseDocument("map/map.lst", text)
	must(err)
	registered := map[int64]bool{}
	for _, item := range dnfpvf.ParseList(doc) {
		registered[item.ID] = true
	}
	before, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	must(err)
	referencedMissing := map[int64]bool{}
	for _, dungeon := range before.Dungeons() {
		for _, maze := range dungeon.Mazes {
			specs := append(append(append([]worldmap.MapSpecification{}, maze.MapSpecifications...), maze.BossSpecifications...), maze.LayeredSpecifications...)
			for _, spec := range specs {
				for _, id := range spec.MapIDs {
					if _, ok := before.FindMap(id); !ok {
						referencedMissing[id] = true
					}
				}
			}
		}
	}
	if len(referencedMissing) != len(p.Entries) {
		panic("plan does not cover the exact missing specification IDs")
	}
	seen := map[int64]bool{}
	var tokens []platformpvf.ScriptToken
	for _, item := range p.Entries {
		if item.ID <= 0 || item.ID > 2147483647 || seen[item.ID] || registered[item.ID] || !referencedMissing[item.ID] || item.Evidence == "" {
			panic("unexpected or duplicate map ID")
		}
		seen[item.ID] = true
		if strings.Contains(item.Path, "..") || strings.HasPrefix(item.Path, "/") || !strings.HasSuffix(strings.ToLower(item.Path), ".map") {
			panic("invalid map reference")
		}
		f, ok := a.FindFile("map/" + item.Path)
		if !ok || f.DataType != 1 {
			panic("planned map file is absent or not a real script")
		}
		raw, err := a.FileRaw(f.Index)
		must(err)
		if digest(raw) != item.MapRawSHA256 {
			panic("map content differs from evidence")
		}
		tokens = append(tokens, platformpvf.ScriptToken{Type: 0, Value: int32(item.ID)}, platformpvf.ScriptToken{Type: 6, Text: item.Path})
	}
	patched, err := a.RepackScriptAdditions([]platformpvf.ScriptAddition{{Path: "map/map.lst", AppendToExisting: true, AppendTokens: tokens}})
	must(err)
	b, err := platformpvf.OpenBytes(patched)
	must(err)
	oldRaw, err := a.ReadRaw("map/map.lst")
	must(err)
	newRaw, err := b.ReadRaw("map/map.lst")
	must(err)
	if len(newRaw) != len(oldRaw)+len(tokens)*5 || !bytes.Equal(oldRaw, newRaw[:len(oldRaw)]) {
		panic("original map-list token prefix changed")
	}
	oldFiles, newFiles := a.Files(), b.Files()
	if len(oldFiles) != len(newFiles) {
		panic("archive file count changed")
	}
	checked := 0
	for i, old := range oldFiles {
		current := newFiles[i]
		if old.ArchivePath != current.ArchivePath || old.DataType != current.DataType {
			panic("unrelated archive metadata changed")
		}
		if old.ArchivePath == "map/map.lst" {
			continue
		}
		if old.Size != current.Size {
			panic("unrelated archive size changed")
		}
		x, err := a.FileRaw(i)
		must(err)
		y, err := b.FileRaw(i)
		must(err)
		if !bytes.Equal(x, y) {
			panic("unrelated archive content changed: " + old.ArchivePath)
		}
		checked++
	}
	after, err := worldmap.LoadSource(context.Background(), b, worldmap.Options{})
	must(err)
	_, err = worldmap.NewResolver(after)
	must(err)
	for _, item := range p.Entries {
		if m, ok := after.FindMap(item.ID); !ok || !strings.EqualFold(m.Path, "map/"+item.Path) {
			panic("map ID did not resolve to the evidenced file")
		}
	}
	must(os.MkdirAll(filepath.Dir(outputAbs), 0755))
	f, err := os.OpenFile(outputAbs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	must(err)
	_, err = f.Write(patched)
	must(err)
	must(f.Close())
	r := map[string]any{"checked_utc": time.Now().UTC().Format(time.RFC3339), "input": inputAbs, "input_sha256": digest(originalBytes), "output": outputAbs, "output_sha256": digest(patched), "output_bytes": len(patched), "entries_added": len(p.Entries), "files": len(oldFiles), "unrelated_files_compared": checked, "original_map_tokens_preserved": true, "worldmap_before": before.Snapshot(), "worldmap_after": after.Snapshot(), "resolver_ok": true, "passed": true}
	report, err := json.MarshalIndent(r, "", "  ")
	must(err)
	reportFile, err := os.OpenFile(outputAbs+".verification.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	must(err)
	_, err = reportFile.Write(report)
	must(err)
	must(reportFile.Close())
	fmt.Printf("PVF_MAP_INDEX_REPAIR_OK added=%d unchanged_files=%d sha256=%s bytes=%d\n", len(p.Entries), checked, digest(patched), len(patched))
}
