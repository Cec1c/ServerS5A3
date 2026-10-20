package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"math"
	"os"
	"strings"
)

func appendGiftShopPlan(a *platformpvf.Archive, planPath, output, reportPath string) {
	data, err := os.ReadFile(planPath)
	if err != nil {
		panic(err)
	}
	var plan struct {
		RemoveItems []uint32 `json:"remove_items"`
		Rows        []struct {
			ID    uint32 `json:"item_id"`
			Price uint32 `json:"price"`
			Job   int32  `json:"job_code"`
			Name  string `json:"name"`
			Group string `json:"group"`
			Path  string `json:"path"`
		} `json:"rows"`
	}
	if err = json.Unmarshal(data, &plan); err != nil {
		panic(err)
	}
	if (len(plan.Rows) == 0 && len(plan.RemoveItems) == 0) || len(plan.Rows) > 10000 || len(plan.RemoveItems) > 10000 {
		panic("shop plan row bound")
	}
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
	existing, _, err := parseExistingPackageRows(raw[(begin+1)*5 : end*5])
	if err != nil {
		panic(err)
	}
	remove := map[uint32]bool{}
	for _, id := range plan.RemoveItems {
		if id == 0 || remove[id] {
			panic("invalid/duplicate removed shop item")
		}
		if _, ok := existing[id]; !ok {
			panic("removed shop item not present")
		}
		remove[id] = true
	}
	retained := []byte{}
	interior := raw[(begin+1)*5 : end*5]
	for offset := 0; offset < len(interior); offset += packageRowWidth * 5 {
		id, ok := intToken(interior[offset+5 : offset+10])
		if !ok || id <= 0 {
			panic("invalid existing shop item")
		}
		if !remove[uint32(id)] {
			retained = append(retained, interior[offset:offset+packageRowWidth*5]...)
		}
	}
	used := allRawIntegerValues(raw)
	next := uint32(900000)
	seen := map[uint32]bool{}
	rows := []packageRow{}
	for _, item := range plan.Rows {
		if item.ID == 0 || item.Price == 0 || item.Price > math.MaxInt32 || item.Job < -1 || item.Job > 15 || seen[item.ID] {
			panic("invalid/duplicate shop row")
		}
		seen[item.ID] = true
		ref, ok := refs[item.ID]
		if !ok || ref.Kind != "stackable" || !strings.EqualFold(ref.Path, item.Path) {
			panic("shop definition ownership mismatch")
		}
		doc, _, e := readDocument(a, ref.Path)
		if e != nil {
			panic(e)
		}
		name, _ := doc.Text("name")
		typ, _ := doc.Text("stackable type")
		if name != item.Name || !strings.EqualFold(typ, "[usable cera package]") {
			panic("shop name/type mismatch")
		}
		if _, ok = existing[item.ID]; ok && !remove[item.ID] {
			panic("planned item already in package catalog")
		}
		magic, e := sectionStringMagic(a, ref.Path, "[name]")
		if e != nil {
			panic(e)
		}
		for {
			if next > math.MaxInt32 {
				panic("commodity space exhausted")
			}
			if _, collision := used[next]; !collision {
				break
			}
			next++
		}
		used[next] = struct{}{}
		rows = append(rows, packageRow{CommodityID: next, ItemID: item.ID, Price: item.Price, NameMagic: magic, JobCode: item.Job, Name: name, Group: item.Group, Path: ref.Path})
		next++
	}
	addition := encodePackageRows(rows)
	newRaw := append([]byte{}, raw[:(begin+1)*5]...)
	newRaw = append(newRaw, retained...)
	newRaw = append(newRaw, addition...)
	newRaw = append(newRaw, raw[end*5:]...)
	result, err := a.RepackRaw([]platformpvf.RawReplacement{{Path: ceraShopPath, Data: newRaw}})
	if err != nil {
		panic(err)
	}
	b, err := platformpvf.OpenBytes(result)
	if err != nil {
		panic(err)
	}
	got, err := b.ReadRaw(ceraShopPath)
	if err != nil || !bytes.Equal(got, newRaw) {
		panic("shop exact readback mismatch")
	}
	start, finish, err := findRawSection(b, got, "[package]", "[/package]")
	if err != nil {
		panic(err)
	}
	gotRows, _, err := parseExistingPackageRows(got[(start+1)*5 : finish*5])
	if err != nil || len(gotRows) != len(existing)-len(remove)+len(rows) {
		panic("shop row readback mismatch")
	}
	for id := range remove {
		if _, ok := gotRows[id]; ok && !seen[id] {
			panic("removed item remained on shelf")
		}
	}
	// The pools are archive sections rather than named files. Verify every
	// distinct string offset used by the original scripts against the reopened
	// result, in addition to comparing the unrelated definition bytes.
	checkedStrings := map[int]bool{}
	checkStrings := func(script []byte) {
		for off := 0; off+5 <= len(script); off += 5 {
			if script[off] != 3 && script[off] != 6 {
				continue
			}
			magic := int(int32(binary.LittleEndian.Uint32(script[off+1 : off+5])))
			if checkedStrings[magic] {
				continue
			}
			if a.ResolveString(magic) != b.ResolveString(magic) {
				panic(fmt.Sprintf("shop changed string value at %d", magic))
			}
			checkedStrings[magic] = true
		}
	}
	checkStrings(raw)
	oldFiles, newFiles := a.Files(), b.Files()
	if len(oldFiles) != len(newFiles) {
		panic("shop changed file count")
	}
	unchanged := 0
	for _, f := range oldFiles {
		if strings.EqualFold(f.ArchivePath, ceraShopPath) {
			continue
		}
		original, e := a.ReadRaw(f.ArchivePath)
		if e != nil {
			panic(e)
		}
		if f.DataType == 1 {
			checkStrings(original)
		}
		rebuilt, e := b.ReadRaw(f.ArchivePath)
		if e != nil || !bytes.Equal(original, rebuilt) {
			panic(fmt.Sprintf("unrelated definition changed %s", f.ArchivePath))
		}
		unchanged++
	}
	if err = os.WriteFile(output, result, 0644); err != nil {
		panic(err)
	}
	report := map[string]any{"passed": true, "published": false, "output": output, "output_sha256": checksum(result), "rows": rows, "added_rows": len(rows), "removed_items": plan.RemoveItems, "removed_rows": len(remove), "retained_shop_rows_exact": true, "unchanged_definitions_exact": unchanged, "original_string_values_unchanged": len(checkedStrings), "original_shop_prefix_suffix_preserved": true}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(reportPath, encoded, 0600); err != nil {
		panic(err)
	}
	fmt.Printf("GIFT_SHOP_PASS rows=%d unchanged_files=%d\n", len(rows), unchanged)
}
