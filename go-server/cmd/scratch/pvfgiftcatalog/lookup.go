package main

import (
	"encoding/json"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func lookupActualDefinitions(a *platformpvf.Archive, idsPath, input, output string) {
	data, err := os.ReadFile(idsPath)
	if err != nil {
		panic(err)
	}
	var ids []uint32
	if err = json.Unmarshal(data, &ids); err != nil {
		panic(err)
	}
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	wanted := map[uint32]bool{}
	paths := map[uint32][]string{}
	for _, id := range ids {
		wanted[id] = true
		if r, ok := refs[id]; ok && a.FindFileIndex(r.Path) >= 0 {
			paths[id] = []string{r.Path}
		}
	}
	numeric := regexp.MustCompile(`(?:^|[^0-9])([0-9]{7,10})(?:[^0-9]|$)`)
	for _, file := range a.Files() {
		if !strings.HasSuffix(strings.ToLower(file.ArchivePath), ".equ") {
			continue
		}
		for _, match := range numeric.FindAllStringSubmatch(file.ArchivePath, -1) {
			value, e := strconv.ParseUint(match[1], 10, 32)
			if e != nil {
				continue
			}
			id := uint32(value)
			if !wanted[id] {
				continue
			}
			if r, ok := refs[id]; ok && a.FindFileIndex(r.Path) >= 0 {
				continue
			}
			paths[id] = append(paths[id], file.ArchivePath)
		}
	}
	matches := []legacyGiftNode{}
	for _, id := range ids {
		for _, path := range paths[id] {
			text, e := a.ReadText(path)
			if e != nil {
				panic(e)
			}
			doc, e := dnfpvf.Parse(path, text)
			if e != nil {
				panic(e)
			}
			name, _ := doc.Text("name")
			typ, _ := doc.Text("stackable type")
			matches = append(matches, legacyGiftNode{ID: id, Path: path, Name: name, Type: typ, Text: text, Tokens: scriptTokens(a, path)})
		}
	}
	result := map[string]any{"input": input, "format": a.Format(), "wanted": ids, "matches": matches, "read_only": true}
	blob, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(output, blob, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("LOOKUP_GO matches=%d format=%s\n", len(matches), a.Format())
}
