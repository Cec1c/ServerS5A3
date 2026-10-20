package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
)

func cleanGiftFatigue(a *platformpvf.Archive, graphFile, output, report string) {
	data, err := os.ReadFile(graphFile)
	if err != nil {
		panic(err)
	}
	var graph struct {
		Nodes         []giftRewardNode `json:"nodes"`
		FatigueIDs    []uint32         `json:"fatigue_ids"`
		GrammarErrors int              `json:"grammar_errors"`
	}
	if err = json.Unmarshal(data, &graph); err != nil {
		panic(err)
	}
	if graph.GrammarErrors != 0 || len(graph.FatigueIDs) == 0 {
		panic("fatigue graph is unproved or empty")
	}
	fatigue := map[uint32]bool{}
	for _, id := range graph.FatigueIDs {
		fatigue[id] = true
	}
	replacements := []platformpvf.RawReplacement{}
	changed := map[string][]platformpvf.ScriptToken{}
	removed := []map[string]any{}
	for _, node := range graph.Nodes {
		before := scriptTokens(a, node.Path)
		if !reflect.DeepEqual(before, node.Tokens) {
			panic("stale graph " + node.Path)
		}
		mask := make([]bool, len(before))
		remaining := 0
		for _, row := range node.Rows {
			if !fatigue[row.ItemID] {
				remaining++
				continue
			}
			if row.Start < 0 || row.End > len(before) || row.Start >= row.End || before[row.Start].Type != 0 || uint32(before[row.Start].Value) != row.ItemID {
				panic("stale reward row")
			}
			for i := row.Start; i < row.End; i++ {
				mask[i] = true
			}
			removed = append(removed, map[string]any{"parent_id": node.ID, "parent_path": node.Path, "fatigue_item_id": row.ItemID, "count": row.Count, "section": row.Section})
		}
		count := 0
		for _, m := range mask {
			if m {
				count++
			}
		}
		if count == 0 {
			continue
		}
		if remaining == 0 {
			panic("fatigue removal would leave empty box")
		}
		raw, err := a.ReadRaw(node.Path)
		if err != nil {
			panic(err)
		}
		clean := make([]byte, 0, len(raw)-count*5)
		tokens := []platformpvf.ScriptToken{}
		for i, m := range mask {
			if !m {
				clean = append(clean, raw[i*5:(i+1)*5]...)
				tokens = append(tokens, before[i])
			}
		}
		replacements = append(replacements, platformpvf.RawReplacement{Path: node.Path, Data: clean})
		changed[node.Path] = tokens
	}
	if len(replacements) == 0 {
		panic("no fatigue-bearing reward rows")
	}
	blob, err := a.RepackRaw(replacements)
	if err != nil {
		panic(err)
	}
	patched, err := platformpvf.OpenBytes(blob)
	if err != nil {
		panic(err)
	}
	if len(patched.Files()) != len(a.Files()) {
		panic("fatigue clean removed definitions")
	}
	for _, n := range graph.Nodes {
		before, err := a.ReadRaw(n.Path)
		if err != nil {
			panic(err)
		}
		after, err := patched.ReadRaw(n.Path)
		if err != nil {
			panic(err)
		}
		if expected, touched := changed[n.Path]; touched {
			actual := scriptTokens(patched, n.Path)
			if !reflect.DeepEqual(actual, expected) {
				panic("clean readback mismatch")
			}
			rows, problems := rewardRows(actual)
			if len(problems) > 0 {
				panic("clean reward grammar invalid")
			}
			for _, row := range rows {
				if fatigue[row.ItemID] {
					panic("fatigue remained in clean box")
				}
			}
		} else if !bytes.Equal(before, after) {
			panic("untargeted reward definition changed")
		}
	}
	for _, p := range []string{stackableListPath, equipmentListPath, ceraShopPath} {
		before, _ := a.ReadRaw(p)
		after, _ := patched.ReadRaw(p)
		if !bytes.Equal(before, after) {
			panic("fatigue clean modified catalog or item references")
		}
	}
	if err = os.WriteFile(output, blob, 0600); err != nil {
		panic(err)
	}
	proof := map[string]any{"passed": true, "candidate": output, "published": false, "sha256": fmt.Sprintf("%x", sha256.Sum256(blob)), "bytes": len(blob), "changed_boxes": len(replacements), "removed_reward_rows": removed, "fatigue_definitions_retained_exact": graph.FatigueIDs, "all_other_graph_definitions_retained_exact": true, "item_references_and_shop_unchanged": true, "nonfatigue_token_order_and_values_preserved": true}
	encoded, _ := json.MarshalIndent(proof, "", "  ")
	if err = os.WriteFile(report, encoded, 0600); err != nil {
		panic(err)
	}
	fmt.Printf("FATIGUE_CLEAN_PASS changed_boxes=%d removed_rows=%d retained_fatigue_definitions=%d\n", len(replacements), len(removed), len(graph.FatigueIDs))
}
