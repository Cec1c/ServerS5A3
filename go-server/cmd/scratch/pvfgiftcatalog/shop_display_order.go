package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

var shopDisplaySortOutput = flag.String("shop-display-sort-to", "", "reverse the verified newest-first raw package shelf for the current native backwards reader")

func correctNativeGiftDisplayOrder(a *platformpvf.Archive, output, reportPath string) {
	raw, err := a.ReadRaw(ceraShopPath)
	if err != nil {
		panic(err)
	}
	begin, end, err := findRawSection(a, raw, "[package]", "[/package]")
	if err != nil {
		panic(err)
	}
	start, finish, width := (begin+1)*5, end*5, packageRowWidth*5
	if (finish-start)%width != 0 {
		panic("package row width")
	}
	count := (finish - start) / width
	if count != 1312 {
		panic(fmt.Sprintf("unexpected audited shelf count %d", count))
	}
	next := append([]byte(nil), raw...)
	beforeRows, afterRows := []string{}, []string{}
	for i := 0; i < count; i++ {
		previous := raw[start+i*width : start+(i+1)*width]
		beforeRows = append(beforeRows, string(previous))
		copy(next[start+(count-1-i)*width:start+(count-i)*width], previous)
	}
	for i := 0; i < count; i++ {
		afterRows = append(afterRows, string(next[start+i*width:start+(i+1)*width]))
	}
	sort.Strings(beforeRows)
	sort.Strings(afterRows)
	for i := range beforeRows {
		if beforeRows[i] != afterRows[i] {
			panic("raw row multiset changed")
		}
	}
	if !bytes.Equal(raw[:start], next[:start]) || !bytes.Equal(raw[finish:], next[finish:]) {
		panic("other shop fields changed")
	}
	result, err := a.RepackRaw([]platformpvf.RawReplacement{{Path: ceraShopPath, Data: next}})
	if err != nil {
		panic(err)
	}
	readback, err := platformpvf.OpenBytes(result)
	if err != nil {
		panic(err)
	}
	verified, err := readback.ReadRaw(ceraShopPath)
	if err != nil || !bytes.Equal(verified, next) {
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
		y, e := readback.ReadRaw(f.ArchivePath)
		if e != nil || !bytes.Equal(x, y) {
			panic("other definition changed: " + f.ArchivePath)
		}
		unchanged++
	}
	if err := os.WriteFile(output, result, 0644); err != nil {
		panic(err)
	}
	report := map[string]any{"passed": true, "published": false, "physical_tested": false, "input_shop_sha256": checksum(raw), "output_shop_sha256": checksum(next), "output_sha256": checksum(result), "rows": count, "all_11_columns_and_multiplicity_preserved": true, "other_shop_bytes_identical": true, "other_definitions_identical": unchanged, "ordering": "complete row reversal; native 01976930 reverse traversal yields intended release year descending and original same-year order"}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(reportPath, encoded, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("SHOP_DISPLAY_ORDER_PASS rows=%d unchanged=%d\n", count, unchanged)
}
