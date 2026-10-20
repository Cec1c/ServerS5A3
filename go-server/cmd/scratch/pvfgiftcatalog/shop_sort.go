package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

var giftReleaseYearPattern = regexp.MustCompile(`20(?:0[0-9]|1[0-9]|2[0-6])`)

func giftShelfJob(jobs []string, previous int32) int32 {
	if len(jobs) != 2 {
		return previous
	}
	a, b := strings.Trim(normalizeJob(jobs[0]), "[]"), strings.Trim(normalizeJob(jobs[1]), "[]")
	if (a == "swordman" && b == "demonic swordman") || (b == "swordman" && a == "demonic swordman") {
		return 0
	}
	if (a == "mage" && b == "creator mage") || (b == "mage" && a == "creator mage") {
		return 3
	}
	return previous
}

func sortGiftShop(a *platformpvf.Archive, output, reportPath string) {
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	raw, err := a.ReadRaw(ceraShopPath)
	if err != nil {
		panic(err)
	}
	begin, end, err := findRawSection(a, raw, "[package]", "[/package]")
	if err != nil {
		panic(err)
	}
	interior := raw[(begin+1)*5 : end*5]
	if len(interior)%(packageRowWidth*5) != 0 {
		panic("package row width")
	}
	type row struct {
		Raw            []byte
		ID             uint32
		Commodity      uint32
		Year, Original int
		Name           string
	}
	rows := []row{}
	changed := []map[string]any{}
	for off := 0; off < len(interior); off += packageRowWidth * 5 {
		b := append([]byte(nil), interior[off:off+packageRowWidth*5]...)
		id, ok := intToken(b[5:10])
		if !ok || id <= 0 {
			panic("invalid package id")
		}
		item := row{Raw: b, ID: uint32(id), Original: len(rows)}
		commodity, ok := intToken(b[:5])
		if !ok || commodity <= 0 {
			panic("invalid package commodity")
		}
		item.Commodity = uint32(commodity)
		if ref, found := refs[uint32(id)]; found {
			doc, _, e := readDocument(a, ref.Path)
			if e != nil {
				panic(e)
			}
			item.Name, _ = doc.Text("name")
			if year := giftReleaseYearPattern.FindString(item.Name); year != "" {
				item.Year, _ = strconv.Atoi(year)
			}
			previous, ok := intToken(b[50:55])
			if !ok {
				panic("invalid package job")
			}
			next := giftShelfJob(doc.Texts("suitable job"), previous)
			if next != previous {
				copy(b[50:55], appendInt(nil, next))
				changed = append(changed, map[string]any{"item_id": id, "name": item.Name, "old_job": previous, "new_job": next, "suitable_jobs": doc.Texts("suitable job")})
			}
		}
		rows = append(rows, item)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Year != rows[j].Year {
			return rows[i].Year > rows[j].Year
		}
		if rows[i].Year != 0 && rows[i].ID != rows[j].ID {
			return rows[i].ID > rows[j].ID
		}
		// The audited undated shelf originally follows commodity ascending.
		// Use that existing order explicitly so repeated runs remain stable
		// after storing the raw rows backwards for the native reader.
		if rows[i].Year == 0 && rows[i].Commodity != rows[j].Commodity {
			return rows[i].Commodity < rows[j].Commodity
		}
		return rows[i].Original < rows[j].Original
	})
	// Current EXE 01976930 builds the displayed package vectors by traversing
	// the PVF shelf from count-1 down to zero. Store the intended display order
	// backwards, retaining the complete commodity/price/job record per row.
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	next := append([]byte(nil), raw[:(begin+1)*5]...)
	for _, item := range rows {
		next = append(next, item.Raw...)
	}
	next = append(next, raw[end*5:]...)
	result, err := a.RepackRaw([]platformpvf.RawReplacement{{Path: ceraShopPath, Data: next}})
	if err != nil {
		panic(err)
	}
	b, err := platformpvf.OpenBytes(result)
	if err != nil {
		panic(err)
	}
	readback, err := b.ReadRaw(ceraShopPath)
	if err != nil || !bytes.Equal(readback, next) {
		panic("shop exact readback")
	}
	unchanged := 0
	for _, f := range a.Files() {
		if strings.EqualFold(f.ArchivePath, ceraShopPath) {
			continue
		}
		x, e := a.ReadRaw(f.ArchivePath)
		if e != nil {
			panic(e)
		}
		y, e := b.ReadRaw(f.ArchivePath)
		if e != nil || !bytes.Equal(x, y) {
			panic(fmt.Sprintf("definition changed: %s", f.ArchivePath))
		}
		unchanged++
	}
	if err = os.WriteFile(output, result, 0644); err != nil {
		panic(err)
	}
	first := []map[string]any{}
	for i := len(rows) - 1; i >= max(0, len(rows)-20); i-- {
		item := rows[i]
		first = append(first, map[string]any{"item_id": item.ID, "name": item.Name, "year": item.Year})
	}
	report := map[string]any{"passed": true, "published": false, "output_sha256": checksum(result), "rows": len(rows), "changed_job_rows": changed, "first_rows": first, "unchanged_definitions_exact": unchanged, "commodity_ids_and_prices_preserved": true, "ordering": "display release year descending; same year item id descending; undated original order; raw shelf reversed for native 01976930 backwards traversal"}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(reportPath, encoded, 0600); err != nil {
		panic(err)
	}
	fmt.Printf("SHOP_SORT_PASS rows=%d job_fixes=%d unchanged=%d\n", len(rows), len(changed), unchanged)
}
