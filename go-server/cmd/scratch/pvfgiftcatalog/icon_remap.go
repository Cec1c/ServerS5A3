package main

import (
	"encoding/json"
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"sort"
	"strings"
)

type giftIconRemap struct {
	ID       uint32 `json:"id"`
	Path     string `json:"path"`
	Section  string `json:"section"`
	OldFrame int32  `json:"old_frame"`
	NewFrame int32  `json:"new_frame"`
	IMG      string `json:"img"`
}

func remapImportedGiftIcons(a *platformpvf.Archive, mapping, output, report string) {
	raw, err := os.ReadFile(mapping)
	if err != nil {
		panic(err)
	}
	var changes []giftIconRemap
	if err = json.Unmarshal(raw, &changes); err != nil {
		panic(err)
	}
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	updated := map[string][]platformpvf.ScriptToken{}
	seen := map[string]bool{}
	for _, change := range changes {
		if change.NewFrame < 0 || change.NewFrame > 50000 || change.OldFrame < 0 || (change.Section != "[icon]" && change.Section != "[icon mark]") {
			panic("invalid icon assignment")
		}
		ref, ok := refs[change.ID]
		if !ok || !strings.EqualFold(ref.Path, change.Path) {
			panic("icon owner mismatch")
		}
		key := strings.ToLower(ref.Path)
		unique := fmt.Sprintf("%s|%s", key, change.Section)
		if seen[unique] {
			panic("duplicate icon section assignment")
		}
		seen[unique] = true
		tokens, ok := updated[key]
		if !ok {
			tokens = scriptTokens(a, ref.Path)
		}
		matches := 0
		for i, t := range tokens {
			if t.Type != 3 || t.Text != change.Section {
				continue
			}
			if i+2 >= len(tokens) || tokens[i+1].Type != 6 || tokens[i+2].Type != 0 {
				panic("unsupported icon grammar")
			}
			img := strings.ToLower(strings.ReplaceAll(tokens[i+1].Text, "\\", "/"))
			if !strings.HasPrefix(img, "sprite/") {
				img = "sprite/" + img
			}
			if img != change.IMG || tokens[i+2].Value != change.OldFrame {
				panic("stale source icon assignment")
			}
			tokens[i+2].Value = change.NewFrame
			matches++
		}
		if matches != 1 {
			panic(fmt.Sprintf("icon section match count=%d item=%d", matches, change.ID))
		}
		updated[key] = tokens
	}
	paths := []string{}
	for path := range updated {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	additions := []platformpvf.ScriptAddition{}
	for _, path := range paths {
		additions = append(additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: updated[path]})
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
			panic("icon token readback mismatch")
		}
	}
	unchanged := 0
	for _, file := range a.Files() {
		if _, ok := updated[strings.ToLower(file.ArchivePath)]; ok {
			continue
		}
		before, e := a.ReadRaw(file.ArchivePath)
		if e != nil {
			panic(e)
		}
		after, e := b.ReadRaw(file.ArchivePath)
		if e != nil || string(before) != string(after) {
			panic("unrelated definition changed " + file.ArchivePath)
		}
		unchanged++
	}
	if err = os.WriteFile(output, blob, 0644); err != nil {
		panic(err)
	}
	result := map[string]any{"passed": true, "published": false, "output": output, "sha256": checksum(blob), "bytes": len(blob), "remapped_icon_fields": len(changes), "modified_definitions": len(updated), "other_definitions_exact": unchanged, "paths": paths}
	proof, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(report, proof, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("ICON_REMAP_PASS fields=%d definitions=%d unchanged=%d\n", len(changes), len(updated), unchanged)
}
