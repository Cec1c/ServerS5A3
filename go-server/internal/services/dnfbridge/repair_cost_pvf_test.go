package dnfbridge

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestNewCurrentRepairCostCatalogReadsGlobalRatesAndItemEvidence(t *testing.T) {
	source := bridgePVFSource{
		"equipment/pricetable.tbl": "[rate]\n200 150 30\n[repair cost]\n0.08\n[quick repair cost rate]\n150\n",
		"etc/upgrade.etc":          "[repair cost rate by upgrade level]\n1 1 1 1 2\n[/repair cost rate by upgrade level]\n",
		"equipment/coat/test.equ":  "[equipment type]\n`[coat]` 0\n[durability]\n28\n[repair price]\n6400\n[grade]\n20\n",
	}
	catalog, err := newCurrentRepairCostCatalog(source, map[int64]string{10018: "coat/test.equ"})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.repairCostRate != 0.08 || catalog.quickRepairRate != 1.5 {
		t.Fatalf("global rates = %v/%v", catalog.repairCostRate, catalog.quickRepairRate)
	}
	if len(catalog.upgradeRates) != 5 || catalog.upgradeRates[4] != 2 {
		t.Fatalf("upgrade rates = %+v", catalog.upgradeRates)
	}
	evidence, err := catalog.resolve(10018)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.MaxDurability != 28 || evidence.RepairPrice != 6400 || evidence.Grade != 20 || evidence.EquipmentType != "[coat]" {
		t.Fatalf("evidence = %+v", evidence)
	}
	// Unknown item: non-repairable marker, no error.
	unknown, err := catalog.resolve(99999999)
	if err != nil || unknown.MaxDurability != -1 {
		t.Fatalf("unknown evidence = %+v err=%v", unknown, err)
	}
	// Cached second read returns the same evidence.
	again, err := catalog.resolve(10018)
	if err != nil || again.MaxDurability != evidence.MaxDurability || again.RepairPrice != evidence.RepairPrice || again.EquipmentType != evidence.EquipmentType {
		t.Fatalf("cached resolve mismatch: %+v vs %+v", again, evidence)
	}
}

func TestNewCurrentRepairCostCatalogFailsClosedOnMissingPriceTable(t *testing.T) {
	if _, err := newCurrentRepairCostCatalog(bridgePVFSource{}, map[int64]string{}); err == nil {
		t.Fatal("missing pricetable.tbl must fail the catalog load")
	}
}

// 耐久三键回退：`[durability]` → `[maximum durability]` → `[max durability]`，
// 与 dungeon_drop_catalog.go:244 / initial_equipment_metadata.go:50 同口径。
// changlog.md:3421 记录过缺口：只写备用键的装备过去得到 MaxDurability = -1，
// 消费方（equip/owner.go:250、inventory/owner.go:1089）一律判成"不可修理"。
func TestNewCurrentRepairCostCatalogReadsAlternateDurabilityKeys(t *testing.T) {
	const globalRates = "[repair cost]\n0.08\n[quick repair cost rate]\n150\n"
	const upgradeRates = "[repair cost rate by upgrade level]\n1 1 1 1 2\n[/repair cost rate by upgrade level]\n"
	const itemHeader = "[equipment type]\n`[coat]` 0\n[repair price]\n6400\n[grade]\n20\n"
	cases := []struct {
		name     string
		sections string
		want     int64
	}{
		// 既有已支持写法：正数 [durability] 必须逐字节保持原行为。
		{name: "durability only", sections: "[durability]\n28\n", want: 28},
		// 真档扫描命中的两类备用键写法（修复前都会得到 -1）。
		{name: "maximum durability only", sections: "[maximum durability]\n45\n", want: 45},
		{name: "max durability only", sections: "[max durability]\n46\n", want: 46},
		// 优先级钉子：同时存在时永远取 [durability]。
		{name: "durability beats maximum durability", sections: "[durability]\n28\n[maximum durability]\n45\n", want: 28},
		{name: "durability beats max durability", sections: "[durability]\n28\n[max durability]\n46\n", want: 28},
		// [durability] 非正时落到备用键。
		{name: "zero durability falls back to maximum durability", sections: "[durability]\n0\n[maximum durability]\n45\n", want: 45},
		{name: "negative durability falls back to maximum durability", sections: "[durability]\n-5\n[maximum durability]\n45\n", want: 45},
		{name: "zero durability falls back to max durability", sections: "[durability]\n0\n[max durability]\n46\n", want: 46},
		// 三键都没有 / 全部非正：仍是 -1 不可修哨兵，不得变成 0。
		{name: "no durability key", sections: "", want: -1},
		{name: "all durability keys non-positive", sections: "[durability]\n0\n[maximum durability]\n-3\n[max durability]\n0\n", want: -1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := bridgePVFSource{
				"equipment/pricetable.tbl": globalRates,
				"etc/upgrade.etc":          upgradeRates,
				"equipment/coat/test.equ":  itemHeader + testCase.sections,
			}
			catalog, err := newCurrentRepairCostCatalog(source, map[int64]string{10018: "coat/test.equ"})
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := catalog.resolve(10018)
			if err != nil {
				t.Fatalf("resolve 10018: %v", err)
			}
			if evidence.MaxDurability != testCase.want {
				t.Fatalf("MaxDurability = %d, want %d (evidence %+v)", evidence.MaxDurability, testCase.want, evidence)
			}
			if evidence.MaxDurability == 0 {
				t.Fatal("MaxDurability must never collapse to 0: the non-repairable sentinel is -1")
			}
			if evidence.RepairPrice != 6400 || evidence.Grade != 20 || evidence.EquipmentType != "[coat]" {
				t.Fatalf("non-durability evidence changed: %+v", evidence)
			}
		})
	}
	// 未知 itemID：仍是 -1 且不报错（86JP skip 规则，现有行为必须保住）。
	source := bridgePVFSource{
		"equipment/pricetable.tbl": globalRates,
		"etc/upgrade.etc":          upgradeRates,
		"equipment/coat/test.equ":  itemHeader + "[maximum durability]\n45\n",
	}
	catalog, err := newCurrentRepairCostCatalog(source, map[int64]string{10018: "coat/test.equ"})
	if err != nil {
		t.Fatal(err)
	}
	if unknown, err := catalog.resolve(99999999); err != nil || unknown.MaxDurability != -1 {
		t.Fatalf("unknown item evidence = %+v err=%v, want MaxDurability -1 and no error", unknown, err)
	}
}

func TestRealPVFRepairCostCatalogCoversStarterEquipment(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("DNFBRIDGE_REAL_PVF_SMOKE not set")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open PVF: %v", err)
	}
	paths, err := initialEquipmentPathMap(archive)
	if err != nil {
		t.Fatalf("equipment paths: %v", err)
	}
	catalog, err := newCurrentRepairCostCatalog(archive, paths)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	// Runtime pricetable.tbl: [repair cost] 0.08, [quick repair cost rate] 150.
	if catalog.repairCostRate != 0.08 || catalog.quickRepairRate != 1.5 {
		t.Fatalf("real rates = %v/%v, want 0.08/1.5", catalog.repairCostRate, catalog.quickRepairRate)
	}
	if len(catalog.upgradeRates) == 0 {
		t.Fatal("real upgrade.etc [repair cost rate by upgrade level] missing")
	}
	// vest_owool.equ (item 10018): [durability] 28, [repair price] 6400,
	// [grade] 20, [equipment type] [coat].
	evidence, err := catalog.resolve(10018)
	if err != nil {
		t.Fatalf("resolve 10018: %v", err)
	}
	if evidence.MaxDurability != 28 || evidence.RepairPrice != 6400 || evidence.Grade != 20 || evidence.EquipmentType != "[coat]" {
		t.Fatalf("vest_owool evidence = %+v", evidence)
	}
	assertRealRepairDurabilitySweep(t, archive, paths, catalog)
}

// assertRealRepairDurabilitySweep 用真档全量装备把修理定价的耐久口径钉死在
// "三键回退、取第一个 > 0"上，并如实报出真档里"只写备用键"的装备数量：
//
//   - 90CN 线上档（111,096,032 B / DA11CB5C…）实测 initialEquipmentPathMap
//     140,862 项里 [maximum durability] / [max durability] **一个都没有**
//     （全档 equipment/**.equ 161,254 个同样 0 个），所以本轮的键回退对这份
//     线上数据是**防御性**修复，真档无命中项。
//   - 一旦换成确实声明备用键的 PVF（A21/86JP 家系的 73 MB 档本平台解析器
//     不支持，无法验证），这段扫描会立刻把命中项报出来，且任何命中项在
//     修复前必然 FAIL（MaxDurability = -1）。
func assertRealRepairDurabilitySweep(t *testing.T, archive initialEquipmentTextSource, paths map[int64]string, catalog *currentRepairCostCatalog) {
	t.Helper()
	ids := make([]int64, 0, len(paths))
	for itemID := range paths {
		ids = append(ids, itemID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var usable, alternateOnly, unreadable, mismatched int
	alternateSamples := make([]string, 0, 5)
	for _, itemID := range ids {
		refPath := strings.TrimSpace(paths[itemID])
		text, actualPath, err := readInitialPVFText(archive, initialPVFPath("equipment", refPath), refPath)
		if err != nil {
			unreadable++
			continue
		}
		document, err := dnfpvf.Parse(actualPath, text)
		if err != nil {
			unreadable++
			continue
		}
		want := int64(-1)
		if value, found := firstPositivePVFInt(document, "durability", "maximum durability", "max durability"); found {
			want = value
		}
		if want > 0 {
			usable++
		}
		if want > 0 {
			if _, primaryPositive := document.Int("durability"); !primaryPositive {
				alternateOnly++
				if len(alternateSamples) < 5 {
					alternateSamples = append(alternateSamples, fmt.Sprintf("itemID=%d path=%s durability=%d", itemID, actualPath, want))
				}
			}
		}
		got, err := catalog.resolve(itemID)
		if err != nil {
			t.Fatalf("real PVF item %d at %s: resolve: %v", itemID, actualPath, err)
		}
		if got.MaxDurability != want {
			mismatched++
			if mismatched <= 5 {
				t.Errorf("real PVF item %d at %s: evidence MaxDurability=%d, three-key document read=%d", itemID, actualPath, got.MaxDurability, want)
			}
		}
	}
	t.Logf("real PVF repair durability sweep: items=%d usable=%d alternate-key-only=%d unreadable=%d mismatched=%d",
		len(ids), usable, alternateOnly, unreadable, mismatched)
	for _, sample := range alternateSamples {
		t.Logf("real PVF alternate-key-only equipment (repair was broken before this fix): %s", sample)
	}
}

// realDocumentOverlaySource 让真档里的某一条路径返回替换后的正文，其余全部
// 透传真档（rates 与 item 文档都来自线上 PVF）。
type realDocumentOverlaySource struct {
	base      initialEquipmentTextSource
	overrides map[string]string
}

func (s realDocumentOverlaySource) ReadText(relativePath string) (string, error) {
	key := strings.ToLower(cleanInitialPVFPath(relativePath))
	if text, ok := s.overrides[key]; ok {
		return text, nil
	}
	return s.base.ReadText(relativePath)
}

// TestRealPVFRepairCostCatalogHonoursAlternateDurabilityKeys 用**真档正文**做
// 三键回退回归：取线上档 item 10018
// (equipment/character/common/jacket/cloth/vest_owool.equ) 的原文，只改写
// [durability] 这一个 section（改名或改值），其余逐字节不动，再让真档目录解析。
//
// 为什么不是"真档里本来就只写备用键的装备"：90CN 线上档全档 161,254 个
// equipment/**.equ 里 [maximum durability] / [max durability] 一个都没有
// （一次性扫描数字见 assertRealRepairDurabilitySweep 的日志与 changlog），
// 真档本身提供不出这种样本。本用例保留真实 itemID、真实路径与真实文档正文，
// 只把那一处键名换成"操作者现场报障里的写法"。修复前这里返回 -1（不可修），
// 修复后必须拿到 PVF 声明的耐久。
func TestRealPVFRepairCostCatalogHonoursAlternateDurabilityKeys(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("DNFBRIDGE_REAL_PVF_SMOKE not set")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open PVF: %v", err)
	}
	paths, err := initialEquipmentPathMap(archive)
	if err != nil {
		t.Fatalf("equipment paths: %v", err)
	}
	const itemID = 10018
	const wantDurability = 28
	refPath := strings.TrimSpace(paths[itemID])
	text, actualPath, err := readInitialPVFText(archive, initialPVFPath("equipment", refPath), refPath)
	if err != nil {
		t.Fatalf("real document for item %d: %v", itemID, err)
	}
	document, err := dnfpvf.Parse(actualPath, text)
	if err != nil {
		t.Fatalf("parse %s: %v", actualPath, err)
	}
	// 真档现状先钉住：item 10018 只声明 [durability] 28。
	if durability, found := document.Int("durability"); !found || durability != wantDurability {
		t.Fatalf("real %s [durability] = %d (found=%t), want %d", actualPath, durability, found, wantDurability)
	}
	for _, name := range []string{"maximum durability", "max durability"} {
		if _, found := document.Int(name); found {
			t.Fatalf("real %s unexpectedly declares [%s]: pick another pin item", actualPath, name)
		}
	}
	renamedMaximum, ok := rewriteRepairDurabilitySection(text, "durability", "maximum durability", wantDurability)
	if !ok {
		t.Fatalf("%s has no [durability] section to rewrite", actualPath)
	}
	renamedMaximum200, ok := rewriteRepairDurabilitySection(text, "durability", "max durability", wantDurability)
	if !ok {
		t.Fatalf("%s has no [durability] section to rewrite", actualPath)
	}
	// [durability] 0 与 [maximum durability] 并存：非正的主键必须落到备用键。
	zeroPrimaryWithMaximum := strings.Replace(renamedMaximum,
		"[maximum durability]", "[durability]\n0\n[maximum durability]", 1)
	if zeroPrimaryWithMaximum == renamedMaximum {
		t.Fatalf("%s rewrite for the zero-durability case failed", actualPath)
	}
	cases := []struct {
		name string
		text string
		want int64
	}{
		{name: "durability renamed to maximum durability", text: renamedMaximum, want: wantDurability},
		{name: "durability renamed to max durability", text: renamedMaximum200, want: wantDurability},
		{name: "zero durability plus maximum durability", text: zeroPrimaryWithMaximum, want: wantDurability},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := realDocumentOverlaySource{
				base:      archive,
				overrides: map[string]string{strings.ToLower(actualPath): testCase.text},
			}
			catalog, err := newCurrentRepairCostCatalog(source, paths)
			if err != nil {
				t.Fatalf("catalog over real PVF: %v", err)
			}
			evidence, err := catalog.resolve(itemID)
			if err != nil {
				t.Fatalf("resolve %d: %v", itemID, err)
			}
			if evidence.MaxDurability != testCase.want {
				t.Fatalf("rewritten real document MaxDurability = %d, want %d", evidence.MaxDurability, testCase.want)
			}
			if evidence.RepairPrice != 6400 || evidence.Grade != 20 || evidence.EquipmentType != "[coat]" {
				t.Fatalf("non-durability evidence changed by the rewrite: %+v", evidence)
			}
		})
	}
}

// rewriteRepairDurabilitySection 把正文里 fromName 这个 section 改名成 toName，
// 并把它的第一个整数改成 value。只动 section 头与那个数值行，其余正文逐字节不动。
func rewriteRepairDurabilitySection(text, fromName, toName string, value int64) (string, bool) {
	lines := strings.Split(text, "\n")
	fromHeader := "[" + fromName + "]"
	for idx, line := range lines {
		if !strings.EqualFold(strings.TrimSpace(line), fromHeader) {
			continue
		}
		lines[idx] = "[" + toName + "]"
		for valueIdx := idx + 1; valueIdx < len(lines); valueIdx++ {
			trimmed := strings.TrimSpace(lines[valueIdx])
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if trimmed == "" {
				continue
			}
			lines[valueIdx] = strconv.FormatInt(value, 10)
			break
		}
		return strings.Join(lines, "\n"), true
	}
	return text, false
}
