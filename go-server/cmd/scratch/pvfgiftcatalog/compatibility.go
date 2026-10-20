package main

import (
	"encoding/json"
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

func giftTag(s string) platformpvf.ScriptToken    { return platformpvf.ScriptToken{Type: 3, Text: s} }
func giftNumber(v int32) platformpvf.ScriptToken  { return platformpvf.ScriptToken{Type: 0, Value: v} }
func giftString(s string) platformpvf.ScriptToken { return platformpvf.ScriptToken{Type: 6, Text: s} }
func giftBlock(tokens []platformpvf.ScriptToken, open string) (int, int) {
	start := -1
	close := "[/" + strings.TrimPrefix(open, "[")
	for i, t := range tokens {
		if t.Type != 3 {
			continue
		}
		if t.Text == open {
			if start >= 0 {
				panic("duplicate block " + open)
			}
			start = i
		}
		if start >= 0 && t.Text == close {
			return start, i + 1
		}
	}
	if start >= 0 {
		panic("unclosed block " + open)
	}
	return -1, -1
}
func giftSplice(tokens []platformpvf.ScriptToken, start, end int, replacement []platformpvf.ScriptToken) []platformpvf.ScriptToken {
	out := append([]platformpvf.ScriptToken(nil), tokens[:start]...)
	out = append(out, replacement...)
	return append(out, tokens[end:]...)
}
func giftPairs(tokens []platformpvf.ScriptToken, tag string) []platformpvf.ScriptToken {
	s, e := giftBlock(tokens, tag)
	if s < 0 {
		return nil
	}
	values := tokens[s+1 : e-1]
	if len(values)%2 != 0 {
		panic("uneven pairs " + tag)
	}
	for _, t := range values {
		if t.Type != 0 || t.Value <= 0 {
			panic("invalid positive pair " + tag)
		}
	}
	return append([]platformpvf.ScriptToken(nil), values...)
}
func makeLegacyGiftsCompatible(a *platformpvf.Archive, familyPath, graphPath, output, report string) {
	read := func(path string, target any) {
		raw, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		if err = json.Unmarshal(raw, target); err != nil {
			panic(err)
		}
	}
	var family struct {
		Items []giftAudit `json:"items"`
	}
	read(familyPath, &family)
	var graph struct {
		Nodes         []giftRewardNode `json:"nodes"`
		GrammarErrors int              `json:"grammar_errors"`
	}
	read(graphPath, &graph)
	if graph.GrammarErrors != 0 {
		panic("unaudited reward grammar")
	}
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	updated := map[string][]platformpvf.ScriptToken{}
	additions := []platformpvf.ScriptAddition{}
	choiceBoxes := []legacyGiftNode{}
	choicesByKey := map[string]uint32{}
	converted := []uint32{}
	periods := []uint32{}
	choiceDetails := []map[string]any{}
	nextID := uint32(490709900)
	scope := map[string]bool{}
	for _, node := range graph.Nodes {
		if !reflect.DeepEqual(scriptTokens(a, node.Path), node.Tokens) {
			panic("stale graph " + node.Path)
		}
		scope[node.Path] = true
		for _, row := range node.Rows {
			if ref, ok := refs[row.ItemID]; ok {
				scope[ref.Path] = true
			}
		}
	}
	for _, item := range family.Items {
		if item.Type != "[cera package]" {
			continue
		}
		tokens := scriptTokens(a, item.Path)
		if tokenSectionValue(tokens, "[stackable type]") != "[cera package]" {
			panic("stale legacy family")
		}
		for i, t := range tokens {
			if t.Type == 3 && t.Text == "[stackable type]" {
				if tokens[i+1].Type != 6 {
					panic("package type token")
				}
				tokens[i+1].Text = "[usable cera package]"
			}
		}
		if s, e := giftBlock(tokens, "[package data include usable period]"); s >= 0 {
			values := tokens[s+1 : e-1]
			if len(values)%3 != 0 {
				panic("period triple grammar")
			}
			pairs := []platformpvf.ScriptToken{}
			for i := 0; i < len(values); i += 3 {
				if values[i].Type != 0 || values[i+1].Type != 0 || values[i+2].Type != 0 || values[i].Value <= 0 || values[i+1].Value <= 0 || values[i+2].Value != 0 {
					panic(fmt.Sprintf("non-permanent legacy period in %d", item.ID))
				}
				pairs = append(pairs, values[i], values[i+1])
			}
			if p, _ := giftBlock(tokens, "[package data]"); p >= 0 {
				panic("mixed period and fixed rewards")
			}
			replacement := append([]platformpvf.ScriptToken{giftTag("[package data]")}, pairs...)
			replacement = append(replacement, giftTag("[/package data]"))
			tokens = giftSplice(tokens, s, e, replacement)
			periods = append(periods, item.ID)
		}
		for selected := giftPairs(tokens, "[package data selection]"); len(selected) > 0; selected = giftPairs(tokens, "[package data selection]") {
			fixed := giftPairs(tokens, "[package data]")
			delivered := selected
			if len(selected) > 2 {
				keyBytes, _ := json.Marshal(selected)
				key := string(keyBytes)
				id, exists := choicesByKey[key]
				if !exists {
					for refs[nextID].Path != "" || a.FindFileIndex(fmt.Sprintf("stackable/custom/ykcn_fate_reward_choice_%d.stk", nextID)) >= 0 {
						nextID++
					}
					id = nextID
					nextID++
					choicesByKey[key] = id
					choiceType := ""
					choiceKind := ""
					for i := 0; i < len(selected); i += 2 {
						r, ok := refs[uint32(selected[i].Value)]
						if !ok || (r.Kind != "equipment" && r.Kind != "stackable") || (choiceKind != "" && choiceKind != r.Kind) {
							panic("unresolved choice dependency")
						}
						reward := scriptTokens(a, r.Path)
						typ := tokenSectionValue(reward, "[equipment type]")
						if r.Kind == "stackable" {
							typ = tokenSectionValue(reward, "[stackable type]")
						}
						if typ == "" || (choiceType != "" && choiceType != typ) || (r.Kind == "equipment" && typ != "[creature]" && typ != "[title name]") {
							panic("choice wrapper requires resolved homogeneous rewards: " + typ)
						}
						choiceType = typ
						choiceKind = r.Kind
					}
					icon := []platformpvf.ScriptToken{}
					for i, t := range tokens {
						if t.Type == 3 && t.Text == "[icon]" {
							icon = append(icon, tokens[i:i+3]...)
							break
						}
					}
					if len(icon) != 3 {
						panic("missing authentic package icon")
					}
					name := "Fate/Zero奖励自选礼盒"
					if choiceType == "[title name]" {
						name = "Fate/Zero称号自选礼盒"
					}
					if choiceType == "[creature]" || choiceType == "[creature egg]" {
						name = "Fate/Zero宠物自选礼盒"
					}
					if strings.Contains(item.Name, "酷爽") {
						name += " (酷爽)"
					} else {
						name += " (清爽)"
					}
					wrapper := []platformpvf.ScriptToken{giftTag("[name]"), giftString(name), giftTag("[explain]"), giftString("开启后，可从原礼包的可选奖励中选择获得一种。"), giftTag("[grade]"), giftNumber(1), giftTag("[rarity]"), giftNumber(2), giftTag("[usable job]"), giftString("[all]"), giftTag("[/usable job]"), giftTag("[attach type]"), giftString("[trade]"), giftTag("[minimum level]"), giftNumber(1)}
					wrapper = append(wrapper, icon...)
					wrapper = append(wrapper, giftTag("[stackable type]"), giftString("[booster selection]"), giftNumber(0), giftTag("[move wav]"), giftString("BONE_TOUCH"), giftTag("[booster category num]"), giftNumber(1), giftNumber(0), giftTag("[booster selection num]"), giftNumber(1), giftTag("[booster select category]"), giftNumber(0), giftNumber(0), giftTag("["+choiceKind+"]"))
					wrapper = append(wrapper, selected...)
					wrapper = append(wrapper, giftTag("[/"+choiceKind+"]"), giftTag("[/booster select category]"), giftTag("[booster category name]"), giftString(""), giftTag("[/booster category name]"))
					path := fmt.Sprintf("stackable/custom/ykcn_fate_reward_choice_%d.stk", id)
					choiceBoxes = append(choiceBoxes, legacyGiftNode{ID: id, Path: path, Name: name, Type: "[booster selection]", Tokens: wrapper})
					additions = append(additions, platformpvf.ScriptAddition{Path: path, TemplatePath: item.Path, ReplaceTokens: true, AppendTokens: wrapper})
				}
				delivered = []platformpvf.ScriptToken{giftNumber(int32(id)), giftNumber(1)}
				choiceDetails = append(choiceDetails, map[string]any{"source": item.ID, "choice_box": id, "original_alternatives": selected, "choose_one": true})
			}
			s, e := giftBlock(tokens, "[package data selection]")
			tokens = giftSplice(tokens, s, e, nil)
			s, e = giftBlock(tokens, "[package data]")
			pairs := append(fixed, delivered...)
			replacement := append([]platformpvf.ScriptToken{giftTag("[package data]")}, pairs...)
			replacement = append(replacement, giftTag("[/package data]"))
			if s < 0 {
				tokens = append(tokens, replacement...)
			} else {
				tokens = giftSplice(tokens, s, e, replacement)
			}
		}
		if pairs := giftPairs(tokens, "[package data]"); len(pairs) == 0 || len(pairs)/2 > 128 {
			panic("invalid converted package rewards")
		}
		updated[item.Path] = tokens
		converted = append(converted, item.ID)
		scope[item.Path] = true
	}
	expirationChanges := []map[string]string{}
	paths := []string{}
	for path := range scope {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		tokens, ok := updated[path]
		if !ok {
			tokens = scriptTokens(a, path)
		}
		changed := false
		for i, t := range tokens {
			if t.Type != 3 || t.Text != "[expiration date]" {
				continue
			}
			if i+1 >= len(tokens) || tokens[i+1].Type != 6 {
				panic("expiration token shape")
			}
			old := tokens[i+1].Text
			if old == "" {
				continue
			}
			date, e := time.ParseInLocation(expirationDateLayout, old, cst)
			if e != nil {
				panic("unsupported expiration " + old)
			}
			if date.After(time.Now()) {
				continue
			}
			tokens[i+1].Text = futureExpiration
			changed = true
			expirationChanges = append(expirationChanges, map[string]string{"path": path, "old": old, "new": futureExpiration})
		}
		if changed {
			updated[path] = tokens
		}
	}
	updatedPaths := []string{}
	for path := range updated {
		updatedPaths = append(updatedPaths, path)
	}
	sort.Strings(updatedPaths)
	for _, path := range updatedPaths {
		additions = append(additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: updated[path]})
	}
	if len(choiceBoxes) > 0 {
		list := []platformpvf.ScriptToken{}
		for _, box := range choiceBoxes {
			list = append(list, giftNumber(int32(box.ID)), giftString(strings.TrimPrefix(box.Path, "stackable/")))
		}
		additions = append(additions, platformpvf.ScriptAddition{Path: stackableListPath, AppendToExisting: true, AppendTokens: list})
	}
	blob, err := a.RepackScriptAdditions(additions)
	if err != nil {
		panic(err)
	}
	b, err := platformpvf.OpenBytes(blob)
	if err != nil {
		panic(err)
	}
	for path, tokens := range updated {
		if !reflect.DeepEqual(tokens, scriptTokens(b, path)) {
			panic("compatibility readback mismatch " + path)
		}
	}
	finalRefs, _, err := loadItemReferences(b)
	if err != nil {
		panic(err)
	}
	for _, box := range choiceBoxes {
		if finalRefs[box.ID].Path != box.Path || !reflect.DeepEqual(box.Tokens, scriptTokens(b, box.Path)) {
			panic("choice box readback mismatch")
		}
	}
	for _, item := range family.Items {
		if item.Type != "[cera package]" {
			continue
		}
		tokens := scriptTokens(b, item.Path)
		if tokenSectionValue(tokens, "[stackable type]") != "[usable cera package]" || len(giftPairs(tokens, "[package data selection]")) != 0 {
			panic(fmt.Sprintf("legacy package not fully converted: id=%d path=%s type=%q selected=%d", item.ID, item.Path, tokenSectionValue(tokens, "[stackable type]"), len(giftPairs(tokens, "[package data selection]"))))
		}
	}
	listBefore, _ := a.ReadRaw(stackableListPath)
	listAfter, _ := b.ReadRaw(stackableListPath)
	if string(listBefore) != string(listAfter[:len(listBefore)]) {
		panic("old item references changed")
	}
	unchanged := 0
	modifiedPaths := map[string]bool{}
	for path := range updated {
		modifiedPaths[strings.ToLower(path)] = true
	}
	for _, file := range a.Files() {
		if modifiedPaths[strings.ToLower(file.ArchivePath)] || strings.EqualFold(file.ArchivePath, stackableListPath) {
			continue
		}
		before, e := a.ReadRaw(file.ArchivePath)
		if e != nil {
			panic(e)
		}
		after, e := b.ReadRaw(file.ArchivePath)
		if e != nil || string(before) != string(after) {
			panic("unrelated script changed " + file.ArchivePath)
		}
		unchanged++
	}
	if err = os.WriteFile(output, blob, 0644); err != nil {
		panic(err)
	}
	result := map[string]any{"passed": true, "published": false, "output": output, "sha256": checksum(blob), "converted_ids": converted, "permanent_period_packages": periods, "choice_boxes": choiceBoxes, "choice_wrappers": choiceDetails, "expiry_changes": expirationChanges, "unrelated_files_unchanged": unchanged, "source_choices_preserved": true, "old_item_list_prefix_preserved": true}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(report, data, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("COMPAT_PASS converted=%d reward-choice-boxes=%d period=%d expiry=%d unchanged=%d\n", len(converted), len(choiceBoxes), len(periods), len(expirationChanges), unchanged)
}
