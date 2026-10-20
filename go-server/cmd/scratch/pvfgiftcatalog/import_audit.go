package main

import (
	"encoding/json"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
)

type legacyGiftNode struct {
	ID     uint32                    `json:"id"`
	Path   string                    `json:"path"`
	Name   string                    `json:"name"`
	Type   string                    `json:"type"`
	Text   string                    `json:"text"`
	Tokens []platformpvf.ScriptToken `json:"tokens"`
}
type legacyGiftExport struct {
	Input string           `json:"input"`
	Roots []uint32         `json:"roots"`
	Nodes []legacyGiftNode `json:"nodes"`
}

func auditLegacyImport(a *platformpvf.Archive, source, output string) {
	b, e := os.ReadFile(source)
	if e != nil {
		panic(e)
	}
	var old legacyGiftExport
	if e = json.Unmarshal(b, &old); e != nil {
		panic(e)
	}
	refs, _, e := loadItemReferences(a)
	if e != nil {
		panic(e)
	}
	rows := []map[string]any{}
	counts := map[string]int{}
	rootSet := map[uint32]bool{}
	for _, id := range old.Roots {
		rootSet[id] = true
	}
	for _, node := range old.Nodes {
		r, exists := refs[node.ID]
		row := map[string]any{"id": node.ID, "source_path": node.Path, "source_name": node.Name, "type": node.Type, "root": rootSet[node.ID]}
		state := "missing_id"
		if exists {
			row["target_path"] = r.Path
			text, err := a.ReadText(r.Path)
			if err != nil {
				row["error"] = err.Error()
				state = "target_read_error"
			} else {
				doc, err := dnfpvf.Parse(r.Path, text)
				if err != nil {
					panic(err)
				}
				name, _ := doc.Text("name")
				row["target_name"] = name
				state = "existing_same_name"
				if name != node.Name {
					state = "existing_different_name"
					row["target_text"] = text
					row["source_text"] = node.Text
				}
			}
		}
		row["state"] = state
		counts[state]++
		rows = append(rows, row)
	}
	result := map[string]any{"source": old.Input, "roots": old.Roots, "counts": counts, "items": rows}
	data, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(output, data, 0600); e != nil {
		panic(e)
	}
	fmt.Printf("IMPORT_AUDIT roots=%d nodes=%d counts=%v\n", len(old.Roots), len(old.Nodes), counts)
}
