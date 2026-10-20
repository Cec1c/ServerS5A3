package main

import (
	"encoding/json"
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
)

type giftRewardRow struct {
	Section string `json:"section"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	ItemID  uint32 `json:"item_id"`
	Count   int32  `json:"count"`
}
type giftRewardNode struct {
	ID      uint32                    `json:"id"`
	Path    string                    `json:"path"`
	Name    string                    `json:"name"`
	Type    string                    `json:"type"`
	Fatigue bool                      `json:"fatigue"`
	Tokens  []platformpvf.ScriptToken `json:"tokens"`
	Rows    []giftRewardRow           `json:"rows"`
	Errors  []string                  `json:"errors"`
}

func tokenSectionValue(tokens []platformpvf.ScriptToken, tag string) string {
	for i, t := range tokens {
		if t.Type == 3 && strings.EqualFold(t.Text, tag) && i+1 < len(tokens) {
			return tokens[i+1].Text
		}
	}
	return ""
}
func rewardRows(tokens []platformpvf.ScriptToken) ([]giftRewardRow, []string) {
	rows := []giftRewardRow{}
	errors := []string{}
	inBooster, inSelection, inRandom := false, false, false
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Type != 3 {
			continue
		}
		tag := strings.ToLower(tokens[i].Text)
		switch tag {
		case "[booster info]":
			inBooster = true
			continue
		case "[/booster info]":
			inBooster = false
			continue
		case "[booster select category]":
			inSelection = true
			continue
		case "[/booster select category]":
			inSelection = false
			continue
		case "[randombox]":
			inRandom = true
			continue
		case "[/randombox]":
			inRandom = false
			continue
		}
		if strings.HasPrefix(tag, "[/") {
			continue
		}
		end := i + 1
		for end < len(tokens) && tokens[end].Type != 3 {
			end++
		}
		start, stride := i+1, 0
		switch {
		case tag == "[package data]" || tag == "[package data selection]":
			stride = 2
		case tag == "[package data include usable period]":
			stride = 3
		case inSelection:
			switch tag {
			case "[avatar]":
				stride = 4
			case "[stackable]", "[creature]", "[equipment]", "[etc]":
				stride = 2
			default:
				if end > start {
					errors = append(errors, "unknown selection tag "+tag)
				}
			}
		case inBooster:
			start++
			stride = 3
			switch tag {
			case "[avatar]", "[special avatar]":
				stride = 5
			case "[broadcast]":
				stride = 4
			case "[charactor]":
				start = i + 5
			}
		case inRandom && tag == "[int data]":
			if end-start >= 7 {
				start += 3
				stride = 4
			} else if end-start >= 2 {
				stride = end - start
			}
		}
		if stride == 0 || end <= start {
			continue
		}
		if (end-start)%stride != 0 {
			errors = append(errors, fmt.Sprintf("%s stride=%d tokens=%d", tag, stride, end-start))
			continue
		}
		for p := start; p < end; p += stride {
			if p+1 >= end || tokens[p].Type != 0 || tokens[p+1].Type != 0 || tokens[p].Value <= 0 || tokens[p+1].Value <= 0 {
				errors = append(errors, fmt.Sprintf("%s invalid item/count at %d", tag, p))
				continue
			}
			count := tokens[p+1].Value
			if inBooster || (inRandom && stride == 4) {
				count = tokens[p+2].Value
			}
			rows = append(rows, giftRewardRow{Section: tag, Start: p, End: p + stride, ItemID: uint32(tokens[p].Value), Count: count})
		}
	}
	return rows, errors
}
func auditRewardGraph(a *platformpvf.Archive, rootSource, output string) {
	b, err := os.ReadFile(rootSource)
	if err != nil {
		panic(err)
	}
	var family struct {
		Items []giftAudit `json:"items"`
	}
	if err = json.Unmarshal(b, &family); err != nil {
		panic(err)
	}
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	roots := []giftAudit{}
	queue := []uint32{}
	for _, r := range family.Items {
		if r.Type == "[usable cera package]" || r.Type == "[cera package]" {
			roots = append(roots, r)
			queue = append(queue, r.ID)
		}
	}
	nodes := []giftRewardNode{}
	seen := map[uint32]bool{}
	missing := []uint32{}
	fatigue := []uint32{}
	errorCount := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		r, ok := refs[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if r.Kind != "stackable" {
			continue
		}
		tokens := scriptTokens(a, r.Path)
		rows, problems := rewardRows(tokens)
		action := strings.ToLower(tokenSectionValue(tokens, "[action type]"))
		isFatigue := action == "[add fatigue]"
		if isFatigue {
			fatigue = append(fatigue, id)
		}
		for _, row := range rows {
			queue = append(queue, row.ItemID)
		}
		errorCount += len(problems)
		nodes = append(nodes, giftRewardNode{ID: id, Path: r.Path, Name: tokenSectionValue(tokens, "[name]"), Type: tokenSectionValue(tokens, "[stackable type]"), Fatigue: isFatigue, Tokens: tokens, Rows: rows, Errors: problems})
		if len(nodes) > 20000 {
			panic("reward graph expansion bound")
		}
	}
	payload := map[string]any{"read_only": true, "roots": roots, "nodes": nodes, "missing_ids": missing, "fatigue_ids": fatigue, "grammar_errors": errorCount, "published": false}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(output, data, 0600); err != nil {
		panic(err)
	}
	fmt.Printf("REWARD_GRAPH roots=%d stackable_nodes=%d fatigue=%d missing=%d grammar_errors=%d\n", len(roots), len(nodes), len(fatigue), len(missing), errorCount)
}
