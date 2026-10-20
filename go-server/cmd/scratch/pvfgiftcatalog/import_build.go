package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strings"
)

func scriptTokens(a *platformpvf.Archive, path string) []platformpvf.ScriptToken {
	raw, err := a.ReadRaw(path)
	if err != nil || len(raw)%5 != 0 {
		panic(fmt.Sprintf("raw %s: %v", path, err))
	}
	result := make([]platformpvf.ScriptToken, 0, len(raw)/5)
	for i := 0; i < len(raw); i += 5 {
		token := platformpvf.ScriptToken{Type: raw[i], Value: int32(binary.LittleEndian.Uint32(raw[i+1 : i+5]))}
		switch token.Type {
		case 0, 2:
		case 3, 5, 6, 7:
			token.Text = a.ResolveString(int(token.Value))
			token.Value = 0
		default:
			panic(fmt.Sprintf("unsupported token %d %s", token.Type, path))
		}
		result = append(result, token)
	}
	return result
}
func withoutCosmetic(tokens []platformpvf.ScriptToken) []platformpvf.ScriptToken {
	result := []platformpvf.ScriptToken{}
	skip := false
	for _, t := range tokens {
		if t.Type == 3 {
			skip = t.Text == "[name]" || t.Text == "[explain]"
		}
		if !skip {
			result = append(result, t)
		}
	}
	return result
}
func importLegacyGifts(a *platformpvf.Archive, source, output, report string) {
	data, err := os.ReadFile(source)
	if err != nil {
		panic(err)
	}
	var old legacyGiftExport
	if err = json.Unmarshal(data, &old); err != nil {
		panic(err)
	}
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	stackTemplate := "stackable/cash/chn_20130702_summer_fatezero/chn_2013_Summer_Fate_package_sm1.stk"
	equipmentTemplate := ""
	for _, r := range refs {
		if r.Kind == "equipment" && strings.Contains(r.Path, "/avatar/") {
			equipmentTemplate = r.Path
			break
		}
	}
	if equipmentTemplate == "" {
		panic("no real avatar template")
	}
	additions := []platformpvf.ScriptAddition{}
	lists := map[string][]platformpvf.ScriptToken{}
	imported := []legacyGiftNode{}
	reused := []uint32{}
	seen := map[uint32]bool{}
	for _, n := range old.Nodes {
		if n.ID == 0 || seen[n.ID] || len(n.Tokens) == 0 {
			panic(fmt.Sprintf("invalid exported node %d", n.ID))
		}
		seen[n.ID] = true
		if r, exists := refs[n.ID]; exists {
			if !strings.EqualFold(r.Path, n.Path) || !reflect.DeepEqual(withoutCosmetic(scriptTokens(a, r.Path)), withoutCosmetic(n.Tokens)) {
				panic(fmt.Sprintf("existing ID %d has different gameplay definition", n.ID))
			}
			reused = append(reused, n.ID)
			continue
		}
		if a.FindFileIndex(n.Path) >= 0 {
			panic(fmt.Sprintf("unreferenced existing path %s", n.Path))
		}
		prefix, listPath, template := "stackable/", stackableListPath, stackTemplate
		if strings.HasPrefix(n.Path, "equipment/") {
			prefix, listPath, template = "equipment/", equipmentListPath, equipmentTemplate
		}
		if !strings.HasPrefix(n.Path, prefix) {
			panic("unexpected import path")
		}
		additions = append(additions, platformpvf.ScriptAddition{Path: n.Path, TemplatePath: template, ReplaceTokens: true, AppendTokens: n.Tokens})
		lists[listPath] = append(lists[listPath], platformpvf.ScriptToken{Type: 0, Value: int32(n.ID)}, platformpvf.ScriptToken{Type: 6, Text: strings.TrimPrefix(n.Path, prefix)})
		imported = append(imported, n)
	}
	for _, path := range []string{stackableListPath, equipmentListPath} {
		if len(lists[path]) > 0 {
			additions = append(additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, AppendTokens: lists[path]})
		}
	}
	blob, err := a.RepackScriptAdditions(additions)
	if err != nil {
		panic(err)
	}
	patched, err := platformpvf.OpenBytes(blob)
	if err != nil {
		panic(err)
	}
	finalRefs, _, err := loadItemReferences(patched)
	if err != nil {
		panic(err)
	}
	for _, n := range imported {
		r, exists := finalRefs[n.ID]
		if !exists || r.Path != n.Path || !reflect.DeepEqual(scriptTokens(patched, n.Path), n.Tokens) {
			panic(fmt.Sprintf("readback mismatch %d", n.ID))
		}
	}
	for _, id := range old.Roots {
		if !seen[id] {
			panic(fmt.Sprintf("missing exported root %d", id))
		}
		if _, ok := finalRefs[id]; !ok {
			panic("missing root list entry")
		}
	}
	// Read the original list bytes back to prove append-only ID mapping. The
	// complete original encrypted body is preserved by RepackScriptAdditions.
	for _, path := range []string{stackableListPath, equipmentListPath} {
		before, _ := a.ReadRaw(path)
		after, _ := patched.ReadRaw(path)
		if len(after) < len(before) || string(before) != string(after[:len(before)]) {
			panic("original item list changed")
		}
	}
	if err = os.WriteFile(output, blob, 0600); err != nil {
		panic(err)
	}
	proof := map[string]any{"passed": true, "source": old.Input, "candidate": output, "roots": old.Roots, "imported_definitions": len(imported), "reused_identical_gameplay_ids": reused, "sha256": fmt.Sprintf("%x", sha256.Sum256(blob)), "bytes": len(blob), "token_readback_exact": true, "original_lists_prefix_exact": true, "published": false}
	b, _ := json.MarshalIndent(proof, "", "  ")
	if err = os.WriteFile(report, b, 0600); err != nil {
		panic(err)
	}
	fmt.Printf("IMPORT_PASS roots=%d new=%d reused=%d bytes=%d\n", len(old.Roots), len(imported), len(reused), len(blob))
}
