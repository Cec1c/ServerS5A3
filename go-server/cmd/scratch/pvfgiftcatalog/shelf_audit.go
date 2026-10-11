package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

var shelfAuditOnly = flag.Bool("shelf-audit-only", false, "read all existing package categories without mutation")

func auditCurrentGiftShelves(a *platformpvf.Archive, output string) {
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	doc, text, err := readDocument(a, ceraShopPath)
	if err != nil {
		panic(err)
	}
	rows := []map[string]any{}
	counts := map[string]map[int]int{}
	for _, section := range []string{"package", "regular package", "community package"} {
		tokens, present := doc.Section(section)
		if !present {
			continue
		}
		values := []dnfpvf.Token{}
		for _, token := range tokens {
			if token.Kind == dnfpvf.TokenInt || token.Kind == dnfpvf.TokenString {
				values = append(values, token)
			}
		}
		if len(values)%11 != 0 {
			panic(fmt.Sprintf("section %s values=%d is not 11-wide", section, len(values)))
		}
		counts[section] = map[int]int{}
		for i := 0; i < len(values); i += 11 {
			itemID := values[i+1].Int
			name, path, year := "", "", 0
			if ref, ok := refs[uint32(itemID)]; ok {
				path = ref.Path
				item, _, err := readDocument(a, path)
				if err != nil {
					panic(err)
				}
				name, _ = item.Text("name")
				if match := giftReleaseYearPattern.FindString(name); match != "" {
					year, _ = strconv.Atoi(match)
				}
			}
			counts[section][year]++
			columns := make([]string, 11)
			for n, token := range values[i : i+11] {
				columns[n] = token.Raw
			}
			rows = append(rows, map[string]any{"section": section, "index": i / 11, "commodity_id": values[i].Int, "item_id": itemID, "name": name, "path": path, "year": year, "columns": columns})
		}
	}
	payload := map[string]any{"read_only": true, "counts": counts, "rows": rows, "shop_text": text}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(output, encoded, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("SHELF_AUDIT_READONLY rows=%d years=%v\n", len(rows), counts)
}
