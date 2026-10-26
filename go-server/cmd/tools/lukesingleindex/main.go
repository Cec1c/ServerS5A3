// lukesingleindex restores only index rows proven by a pinned upstream PVF.
// Its output is an offline research candidate, not a deployment-ready archive.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	script "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	archive "longheng.io/server/internal/platform/pvf"
)

const inputHash = "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06"

// Extracted from the locally retained Tencent V23.6.15.0 patch payload.
const referenceHash = "a15e3390a4db095d53e75b12843a46d854e46b7b12ce418286d20fe5c0e64cfb"

type row struct {
	Index      string `json:"index"`
	ID         int64  `json:"id"`
	Path       string `json:"path"`
	TextSHA256 string `json:"text_sha256"`
}
type plan struct {
	Rows             []row   `json:"rows"`
	MissingDoorRange []int64 `json:"missing_door_range"`
}

func digest(data []byte) string  { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func normalized(p string) string { return strings.ToLower(strings.ReplaceAll(p, "\\", "/")) }

func index(s script.Source, name string) (map[int64]string, error) {
	text, err := s.ReadText(name)
	if err != nil {
		return nil, err
	}
	doc, err := script.Parse(name, text)
	if err != nil {
		return nil, err
	}
	result := map[int64]string{}
	for _, r := range script.ParseList(doc) {
		if _, exists := result[r.ID]; r.ID <= 0 || exists {
			return nil, fmt.Errorf("invalid/duplicate index %s/%d", name, r.ID)
		}
		p := normalized(r.Path)
		if p == "" || strings.Contains("/"+p+"/", "/../") || strings.ContainsAny(p, ":\x00") || strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("invalid index path %q", p)
		}
		result[r.ID] = p
	}
	return result, nil
}

// No filename-number aliases: the ID/path binding, complete MAP text and
// DGN maze declarations must agree with the independently extracted upstream.
func buildPlan(current, upstream script.Source) (plan, error) {
	p := plan{}
	refs := map[int64]bool{}
	for _, kind := range []string{"dungeon", "map"} {
		name := kind + "/" + kind + ".lst"
		old, err := index(current, name)
		if err != nil {
			return p, err
		}
		source, err := index(upstream, name)
		if err != nil {
			return p, err
		}
		var ids []int64
		if kind == "dungeon" {
			for id := int64(5323); id <= 5334; id++ {
				ids = append(ids, id)
			}
		} else {
			if len(refs) != 58 {
				return p, fmt.Errorf("expected 58 explicit map references, got %d", len(refs))
			}
			for id := int64(95267); id <= 95324; id++ {
				if !refs[id] {
					return p, fmt.Errorf("missing DGN map reference %d", id)
				}
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			path := source[id]
			if !strings.HasPrefix(path, "luke_single/") {
				return p, fmt.Errorf("upstream binding absent/outside Luke family: %s/%d", name, id)
			}
			if existing, exists := old[id]; exists && existing != path {
				return p, fmt.Errorf("conflicting existing binding %s/%d", name, id)
			}
			full := kind + "/" + path
			text, err := current.ReadText(full)
			if err != nil {
				return p, err
			}
			original, err := upstream.ReadText(full)
			if err != nil {
				return p, err
			}
			if kind == "map" && text != original {
				return p, fmt.Errorf("upstream script differs: %s", full)
			}
			doc, err := script.Parse(full, text)
			if err != nil || len(doc.Tokens) == 0 {
				return p, fmt.Errorf("invalid script %s", full)
			}
			if kind == "dungeon" {
				d := worldmap.ParseDungeon(id, full, doc)
				upDoc, err := script.Parse(full, original)
				if err != nil {
					return p, err
				}
				upDungeon := worldmap.ParseDungeon(id, full, upDoc)
				mazeBytes, _ := json.Marshal(d.Mazes)
				upMazeBytes, _ := json.Marshal(upDungeon.Mazes)
				if string(mazeBytes) != string(upMazeBytes) {
					return p, fmt.Errorf("upstream maze differs: %s", full)
				}
				if len(d.Mazes) == 0 {
					return p, fmt.Errorf("no maze %d", id)
				}
				for _, maze := range d.Mazes {
					for _, specs := range [][]worldmap.MapSpecification{maze.MapSpecifications, maze.BossSpecifications, maze.LayeredSpecifications} {
						for _, spec := range specs {
							for _, mapID := range spec.MapIDs {
								refs[mapID] = true
							}
						}
					}
				}
			} else if len(doc.Ints("pathgate recognize range")) != 8 {
				p.MissingDoorRange = append(p.MissingDoorRange, id)
			}
			if _, exists := old[id]; !exists {
				p.Rows = append(p.Rows, row{name, id, path, digest([]byte(text))})
			}
		}
	}
	return p, nil
}

func additions(p plan) []archive.ScriptAddition {
	groups := map[string][]row{}
	for _, r := range p.Rows {
		groups[r.Index] = append(groups[r.Index], r)
	}
	var names []string
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []archive.ScriptAddition
	for _, name := range names {
		a := archive.ScriptAddition{Path: name, TemplatePath: name, AppendToExisting: true}
		for _, r := range groups[name] {
			a.AppendTokens = append(a.AppendTokens, archive.ScriptToken{Type: 0, Value: int32(r.ID)}, archive.ScriptToken{Type: 6, Text: r.Path})
		}
		out = append(out, a)
	}
	return out
}

func writeNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func generate(input, reference, output, report string) error {
	if input == "" || reference == "" || report == "" {
		return fmt.Errorf("input, reference and report required")
	}
	if output != "" {
		outPath, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		reportPath, err := filepath.Abs(report)
		if err != nil {
			return err
		}
		if strings.EqualFold(outPath, reportPath) {
			return fmt.Errorf("candidate and report must be separate")
		}
	}
	// Refuse existing destinations (including inputs and hard links) before work.
	for _, name := range []string{output, report} {
		if name != "" {
			if _, err := os.Lstat(name); !os.IsNotExist(err) {
				return fmt.Errorf("destination exists or unavailable: %s", name)
			}
		}
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	if digest(raw) != inputHash {
		return fmt.Errorf("input hash mismatch")
	}
	ref, err := os.ReadFile(reference)
	if err != nil {
		return err
	}
	if digest(ref) != referenceHash {
		return fmt.Errorf("reference hash mismatch")
	}
	a, err := archive.OpenBytes(raw)
	if err != nil {
		return err
	}
	b, err := archive.OpenBytes(ref)
	if err != nil {
		return err
	}
	p, err := buildPlan(a, b)
	if err != nil {
		return err
	}
	if len(p.Rows) != 70 {
		return fmt.Errorf("expected 12 DGN + 58 MAP additions, got %d", len(p.Rows))
	}
	r := map[string]any{"input_sha256": inputHash, "reference_sha256": referenceHash, "plan": p, "deployed": false, "native_ready": false, "scope": "index-only offline research candidate; native door traversal unverified"}
	if output != "" {
		data, err := a.RepackScriptAdditions(additions(p))
		if err != nil {
			return err
		}
		next, err := archive.OpenBytes(data)
		if err != nil {
			return err
		}
		unchanged, err := a.VerifyScriptReplacementPreservation(next, []string{"dungeon/dungeon.lst", "map/map.lst"})
		if err != nil {
			return err
		}
		again, err := buildPlan(next, b)
		if err != nil {
			return err
		}
		if len(again.Rows) != 0 {
			return fmt.Errorf("candidate still missing rows")
		}
		for route := byte(0); route < 2; route++ {
			if _, err := raid.ValidateSingleRaidDependenciesForRoute(next, route); err != nil {
				return err
			}
		}
		table, err := worldmap.LoadSource(context.Background(), next, worldmap.Options{})
		if err != nil {
			return err
		}
		resolver, err := worldmap.NewResolver(table)
		if err != nil {
			return err
		}
		rooms := 0
		for id := int64(5323); id <= 5334; id++ {
			layout, err := worldmap.BuildDungeonLayout(resolver, id, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
			if err != nil {
				return err
			}
			if len(layout.UnresolvedRooms()) != 0 {
				return fmt.Errorf("unresolved rooms in %d", id)
			}
			rooms += len(layout.Rooms())
		}
		r["unchanged_archive_files"] = unchanged
		r["layout_rooms"] = rooms
		r["dependency_routes_pass"] = []int{0, 1}
		r["output_sha256"] = digest(data)
		if err := writeNew(output, data); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(report, encoded)
}

func main() {
	input := flag.String("input", "", "pinned current PVF")
	reference := flag.String("reference", "", "pinned upstream V23.6.15.0 PVF")
	output := flag.String("output", "", "new offline index-only candidate; omit for audit")
	report := flag.String("report", "", "new JSON evidence report")
	flag.Parse()
	if err := generate(*input, *reference, *output, *report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
