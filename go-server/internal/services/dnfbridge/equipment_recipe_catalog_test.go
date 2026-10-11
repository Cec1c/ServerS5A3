package dnfbridge

// 纯解析用例的假 PVF：段结构与真实 Script.pvf 的设计图逐字同形（含反引号引号、
// 同一批段名），只把文档缩到最小。真实档核对见 TestRealPVF... 。

import (
	"errors"
	"math"
	"os"
	"reflect"
	"strconv"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	// 显式材料/产出 + 成功/失败产出 + 制作专业 + 制作等级（真实 10095704 同形）。
	equipmentRecipeTestMaterialOutputDoc = "" +
		"[name] `Solid Kunai Blueprint`\n" +
		"[grade] 84\n" +
		"[rarity] 6\n" +
		"[attach type] `[free]`\n" +
		"[stackable type]\n" +
		"`[recipe]` 1\n" +
		"[string data]\n" +
		"`[weaving]`\n" +
		"[/string data]\n" +
		"[input item]\n" +
		"109040072 1 10000204 15 3167 250 \n" +
		"[/input item]\n" +
		"[output item]\n" +
		"109040073 1 \n" +
		"[/output item]\n" +
		"[success rate]\n" +
		"1000 \n" +
		"[maintain ability]\n" +
		"`normal` \n" +
		"[broadcast type]\n" +
		"1 \n" +
		"[fail output]\n" +
		"109040072 1 10150748 1 \n" +
		"[/fail output]\n" +
		"[minimum itemmaking level]\n" +
		"`weapon` 8 \n"

	// [input item] 里 id==0 的一对是金币，不是材料（真实 5065 同形）。
	equipmentRecipeTestGoldDoc = "" +
		"[stackable type]\n" +
		"`[recipe]` 0\n" +
		"[string data]\n" +
		"`[craftmanship]`\n" +
		"[/string data]\n" +
		"[input item]\n" +
		"0 300000 100331147 1 \n" +
		"[/input item]\n" +
		"[output item]\n" +
		"10001351 1 \n" +
		"[/output item]\n" +
		"[item category]\n" +
		"`cash`\n" +
		"[/item category]\n" +
		"[impossible contents]\n" +
		"`gift`\n" +
		"[/impossible contents]\n"

	// [action type] 升级配方 + [need material]，没有 [output item]（真实 10090321 同形）。
	equipmentRecipeTestInPlaceDoc = "" +
		"[stackable type]\n" +
		"`[recipe]` 0\n" +
		"[action type]\n" +
		"`[infinite upgrade recipe]`\n" +
		"[/action type]\n" +
		"[maintain ability]\n" +
		"`infinite upgrade event`\n" +
		"[custom upgrade type]\n" +
		"`infinite_upgrade_event_20_level`\n" +
		"[item category]\n" +
		"`event`\n" +
		"[/item category]\n" +
		"[need material]\n" +
		"10090188 5 \n" +
		"[/need material]\n"

	// [int data] 严格形状：材料数+对、产出数+对、单个 0 结尾（真实 2683017 同形）。
	equipmentRecipeTestIntDataDoc = "" +
		"[stackable type]\n" +
		"`[recipe]` 1\n" +
		"[int data]\n" +
		"1 2683017 1 1 2683023 1 0 \n" +
		"[/int data]\n" +
		"[sub type]\n" +
		"1 \n"

	// [int data] 的拍平转储形状：结构未证，必须按 unsupported 处理。
	equipmentRecipeTestUnprovenIntDataDoc = "" +
		"[stackable type]\n" +
		"`[recipe]` 1\n" +
		"[int data]\n" +
		"2 3227 5 3166 20 191 1 2610039 3 1 \n" +
		"[/int data]\n"

	// 只有产出、没有材料：没有任何已证形状，必须 unsupported。
	equipmentRecipeTestOutputsOnlyDoc = "" +
		"[stackable type]\n" +
		"`[recipe]` 0\n" +
		"[output item]\n" +
		"10001351 1 \n" +
		"[/output item]\n"

	// 非设计图的普通 stackable。
	equipmentRecipeTestPlainStackableDoc = "" +
		"[name] `Magic Solvent`\n" +
		"[stackable type] `[material]`\n" +
		"[stack limit] 999\n"

	equipmentRecipeTestList = "" +
		"5065 `recipe/rcp_gold.stk`\n" +
		"10095704 `10095001/10095704.stk`\n" +
		"10090321 `10090001/10090321.stk`\n" +
		"2683017 `cash/fate.stk`\n" +
		"10091001 `10091001/unproven.stk`\n" +
		"10092001 `10092001/outputs_only.stk`\n" +
		"3227 `material/solvent.stk`\n"
)

func equipmentRecipeTestSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		equipmentRecipeStackableListPath:      equipmentRecipeTestList,
		"stackable/recipe/rcp_gold.stk":       equipmentRecipeTestGoldDoc,
		"stackable/10095001/10095704.stk":     equipmentRecipeTestMaterialOutputDoc,
		"stackable/10090001/10090321.stk":     equipmentRecipeTestInPlaceDoc,
		"stackable/cash/fate.stk":             equipmentRecipeTestIntDataDoc,
		"stackable/10091001/unproven.stk":     equipmentRecipeTestUnprovenIntDataDoc,
		"stackable/10092001/outputs_only.stk": equipmentRecipeTestOutputsOnlyDoc,
		"stackable/material/solvent.stk":      equipmentRecipeTestPlainStackableDoc,
	}
}

func equipmentRecipeTestStack(itemID, count int64) equipmentRecipeItemStack {
	return equipmentRecipeItemStack{ItemID: itemID, Count: count}
}

func TestEquipmentRecipeCatalogParsesProvenDesignDrawingShapes(t *testing.T) {
	catalog, err := newPVFEquipmentRecipeCatalog(equipmentRecipeTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := catalog.Snapshot(); snapshot.StackableEntries != 7 || snapshot.RecipeEntries != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}

	entry, found, err := catalog.Recipe(10095704)
	if err != nil || !found {
		t.Fatalf("material-output entry found=%t err=%v", found, err)
	}
	if entry.Kind != equipmentRecipeKindMaterialOutput || entry.PVFPath != "stackable/10095001/10095704.stk" {
		t.Fatalf("entry=%+v", entry)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(109040072, 1), equipmentRecipeTestStack(10000204, 15), equipmentRecipeTestStack(3167, 250)}; !reflect.DeepEqual(entry.Materials, want) {
		t.Fatalf("materials=%+v want=%+v", entry.Materials, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(109040073, 1)}; !reflect.DeepEqual(entry.Outputs, want) {
		t.Fatalf("outputs=%+v want=%+v", entry.Outputs, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(109040072, 1), equipmentRecipeTestStack(10150748, 1)}; !reflect.DeepEqual(entry.FailOutputs, want) {
		t.Fatalf("fail outputs=%+v want=%+v", entry.FailOutputs, want)
	}
	if entry.GoldCostFound || entry.GoldCost != 0 {
		t.Fatalf("material-output entry fabricated gold cost: %+v", entry)
	}
	if entry.SuccessRate != 1000 || !entry.SuccessRateFound || entry.MaintainAbility != "normal" || entry.MaintainSocketStat {
		t.Fatalf("success/maintain=%+v", entry)
	}
	if entry.BroadcastType != 1 || !entry.BroadcastFound || entry.CreationRateFound {
		t.Fatalf("broadcast/creation=%+v", entry)
	}
	if !reflect.DeepEqual(entry.Specialties, []string{"[weaving]"}) {
		t.Fatalf("specialties=%+v", entry.Specialties)
	}
	if entry.MinimumItemmakingField != "weapon" || entry.MinimumItemmakingLevel != 8 || !entry.MinimumItemmakingFound {
		t.Fatalf("minimum itemmaking=%+v", entry)
	}
	if entry.StackableTag != "[recipe]" || entry.StackableKind != 1 || !entry.StackableKindFound {
		t.Fatalf("stackable tag=%+v", entry)
	}
	if entry.Grade != 84 || entry.Rarity != 6 || entry.AttachType != "[free]" {
		t.Fatalf("audit fields=%+v", entry)
	}

	gold, found, err := catalog.Recipe(5065)
	if err != nil || !found {
		t.Fatalf("gold entry found=%t err=%v", found, err)
	}
	if gold.Kind != equipmentRecipeKindMaterialOutput || !gold.GoldCostFound || gold.GoldCost != 300000 {
		t.Fatalf("gold entry=%+v", gold)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(100331147, 1)}; !reflect.DeepEqual(gold.Materials, want) {
		t.Fatalf("gold-bearing materials=%+v want=%+v", gold.Materials, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(10001351, 1)}; !reflect.DeepEqual(gold.Outputs, want) {
		t.Fatalf("gold-bearing outputs=%+v want=%+v", gold.Outputs, want)
	}
	if !reflect.DeepEqual(gold.ItemCategories, []string{"cash"}) || !reflect.DeepEqual(gold.ImpossibleItems, []string{"gift"}) {
		t.Fatalf("gold categories=%+v/%+v", gold.ItemCategories, gold.ImpossibleItems)
	}

	inPlace, found, err := catalog.Recipe(10090321)
	if err != nil || !found {
		t.Fatalf("in-place entry found=%t err=%v", found, err)
	}
	if inPlace.Kind != equipmentRecipeKindInPlaceUpgrade || len(inPlace.Outputs) != 0 || len(inPlace.Materials) != 0 {
		t.Fatalf("in-place entry=%+v", inPlace)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(10090188, 5)}; !reflect.DeepEqual(inPlace.ExtraMaterials, want) {
		t.Fatalf("in-place materials=%+v want=%+v", inPlace.ExtraMaterials, want)
	}
	if inPlace.ActionType != "[infinite upgrade recipe]" || inPlace.CustomUpgradeType != "infinite_upgrade_event_20_level" || inPlace.MaintainAbility != "infinite upgrade event" {
		t.Fatalf("in-place action=%+v", inPlace)
	}
	if !reflect.DeepEqual(inPlace.ItemCategories, []string{"event"}) {
		t.Fatalf("in-place categories=%+v", inPlace.ItemCategories)
	}

	intData, found, err := catalog.Recipe(2683017)
	if err != nil || !found {
		t.Fatalf("int-data entry found=%t err=%v", found, err)
	}
	if intData.Kind != equipmentRecipeKindIntDataEncoded {
		t.Fatalf("int-data entry=%+v", intData)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(2683017, 1)}; !reflect.DeepEqual(intData.Materials, want) {
		t.Fatalf("int-data materials=%+v want=%+v", intData.Materials, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(2683023, 1)}; !reflect.DeepEqual(intData.Outputs, want) {
		t.Fatalf("int-data outputs=%+v want=%+v", intData.Outputs, want)
	}
	if want := []int64{1, 2683017, 1, 1, 2683023, 1, 0}; !reflect.DeepEqual(intData.IntData, want) {
		t.Fatalf("int-data raw=%+v want=%+v", intData.IntData, want)
	}
	if intData.SubType != 1 || !intData.SubTypeFound {
		t.Fatalf("int-data sub type=%+v", intData)
	}

	unproven, found, err := catalog.Recipe(10091001)
	if err != nil || !found {
		t.Fatalf("unproven entry found=%t err=%v", found, err)
	}
	if unproven.Kind != equipmentRecipeKindUnsupported || unproven.UnsupportedReason == "" {
		t.Fatalf("unproven entry=%+v", unproven)
	}
	if len(unproven.Materials) != 0 || len(unproven.Outputs) != 0 {
		t.Fatalf("unproven entry fabricated materials/outputs: %+v", unproven)
	}
	if len(unproven.IntDataRemainder) == 0 {
		t.Fatalf("unproven entry dropped raw remainder: %+v", unproven)
	}

	outputsOnly, found, err := catalog.Recipe(10092001)
	if err != nil || !found {
		t.Fatalf("outputs-only entry found=%t err=%v", found, err)
	}
	if outputsOnly.Kind != equipmentRecipeKindUnsupported || outputsOnly.UnsupportedReason == "" {
		t.Fatalf("outputs-only entry=%+v", outputsOnly)
	}

	if _, found, err := catalog.Recipe(3227); err != nil || found {
		t.Fatalf("plain stackable reported as recipe: found=%t err=%v", found, err)
	}
	if _, found, err := catalog.Recipe(999999); err != nil || found {
		t.Fatalf("unknown id reported as recipe: found=%t err=%v", found, err)
	}
	if snapshot := catalog.Snapshot(); snapshot.RecipeEntries != 6 {
		t.Fatalf("snapshot after lookups=%+v", snapshot)
	}
}

func TestEquipmentRecipeCatalogSkipsZeroCountPairsButKeepsThemAsEvidence(t *testing.T) {
	source := dungeonDropCatalogTestSource{
		equipmentRecipeStackableListPath: "2654516 `recipe/nrcp_2654516.stk`\n",
		"stackable/recipe/nrcp_2654516.stk": "" +
			"[stackable type]\n" +
			"`[recipe]` 1\n" +
			"[input item]\n" +
			"3037 53 3174 0 \n" +
			"[/input item]\n" +
			"[output item]\n" +
			"2212102 1 \n" +
			"[/output item]\n",
	}
	catalog, err := newPVFEquipmentRecipeCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := catalog.Recipe(2654516)
	if err != nil || !found {
		t.Fatalf("entry found=%t err=%v", found, err)
	}
	if entry.Kind != equipmentRecipeKindMaterialOutput {
		t.Fatalf("entry=%+v", entry)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(3037, 53)}; !reflect.DeepEqual(entry.Materials, want) {
		t.Fatalf("materials=%+v want=%+v", entry.Materials, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(3174, 0)}; !reflect.DeepEqual(entry.ZeroCountPairs, want) {
		t.Fatalf("zero-count pairs=%+v want=%+v", entry.ZeroCountPairs, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(2212102, 1)}; !reflect.DeepEqual(entry.Outputs, want) {
		t.Fatalf("outputs=%+v want=%+v", entry.Outputs, want)
	}
}

func TestEquipmentRecipeCatalogRejectsMalformedRecipeSections(t *testing.T) {
	if _, err := newPVFEquipmentRecipeCatalog(nil); !errors.Is(err, errEquipmentRecipePVFSourceRequired) {
		t.Fatalf("nil source error=%v", err)
	}
	if _, err := newPVFEquipmentRecipeCatalog(dungeonDropCatalogTestSource{equipmentRecipeStackableListPath: ""}); !errors.Is(err, errEquipmentRecipePVFDocumentShape) {
		t.Fatalf("empty list error=%v", err)
	}

	tests := []struct {
		name string
		doc  string
	}{
		{
			name: "odd material row width",
			doc:  "[stackable type]\n`[recipe]` 0\n[input item]\n3227 \n[/input item]\n[output item]\n10001351 1 \n[/output item]\n",
		},
		{
			name: "non-integer token in output row",
			doc:  "[stackable type]\n`[recipe]` 0\n[input item]\n3227 1 \n[/input item]\n[output item]\n`abc` 1 \n[/output item]\n",
		},
		{
			name: "duplicate gold pair",
			doc:  "[stackable type]\n`[recipe]` 0\n[input item]\n0 1 0 2 3227 1 \n[/input item]\n[output item]\n10001351 1 \n[/output item]\n",
		},
		{
			name: "negative material count",
			// 数量为 0 的对在真实档里存在（nrcp_2654516），按服务端参考实现 A21 跳过；
			// 负数则不是任何一种已证形状，必须 fail closed。
			doc: "[stackable type]\n`[recipe]` 0\n[input item]\n3227 -1 \n[/input item]\n[output item]\n10001351 1 \n[/output item]\n",
		},
		{
			name: "duplicate payload section",
			doc:  "[stackable type]\n`[recipe]` 1\n[int data]\n1 3227 1 1 10001351 1 0 \n[/int data]\n[int data]\n2 \n[/int data]\n",
		},
		{
			name: "duplicate scalar section",
			doc:  "[stackable type]\n`[recipe]` 1\n[input item]\n3227 1 \n[/input item]\n[output item]\n10001351 1 \n[/output item]\n[success rate]\n1000 2000 \n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := dungeonDropCatalogTestSource{
				equipmentRecipeStackableListPath:  "10095704 `10095001/10095704.stk`\n",
				"stackable/10095001/10095704.stk": test.doc,
			}
			catalog, err := newPVFEquipmentRecipeCatalog(source)
			if err != nil {
				t.Fatalf("build catalog: %v", err)
			}
			if _, _, err := catalog.Recipe(10095704); !errors.Is(err, errEquipmentRecipePVFDocumentShape) {
				t.Fatalf("error=%v want=%v", err, errEquipmentRecipePVFDocumentShape)
			}
		})
	}
}

func TestEquipmentRecipeCatalogScanFailsClosedOnUnreadableEntries(t *testing.T) {
	source := equipmentRecipeTestSource()
	source[equipmentRecipeStackableListPath] = equipmentRecipeTestList + "9001 `10009001/missing.stk`\n"
	catalog, err := newPVFEquipmentRecipeCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	entries, coverage, err := catalog.Scan()
	if err == nil {
		t.Fatalf("scan accepted an unreadable listed entry: coverage=%+v", coverage)
	}
	if coverage.StackableEntries != 8 || coverage.Failed != 1 || coverage.ReadEntries != 7 {
		t.Fatalf("coverage=%+v", coverage)
	}
	if coverage.RecipeEntries != 6 || coverage.Unsupported != 2 || len(entries) != 6 {
		t.Fatalf("coverage=%+v entries=%d", coverage, len(entries))
	}
	wantKinds := map[equipmentRecipeKind]int{
		equipmentRecipeKindMaterialOutput: 2,
		equipmentRecipeKindInPlaceUpgrade: 1,
		equipmentRecipeKindIntDataEncoded: 1,
		equipmentRecipeKindUnsupported:    2,
	}
	if !reflect.DeepEqual(coverage.ByKind, wantKinds) {
		t.Fatalf("by kind=%+v want=%+v", coverage.ByKind, wantKinds)
	}
	for index := 1; index < len(entries); index++ {
		if entries[index-1].ItemID >= entries[index].ItemID {
			t.Fatalf("scan order not deterministic: %+v", entries)
		}
	}
	if snapshot := catalog.Snapshot(); snapshot.RecipeEntries != 0 {
		t.Fatalf("failed scan must not be cached as complete: %+v", snapshot)
	}
}

func TestRealPVFEquipmentRecipeCatalogParsesDesignDrawings(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFEquipmentRecipeCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	entries, coverage, err := catalog.Scan()
	if err != nil {
		t.Fatalf("scan: %v coverage=%+v", err, coverage)
	}
	t.Logf("coverage=%+v entries=%d", coverage, len(entries))
	if coverage.Failed != 0 || coverage.ReadEntries != coverage.StackableEntries {
		t.Fatalf("coverage=%+v", coverage)
	}

	// 钉在本轮证据档（sha256 见 equipment_recipe_catalog.go 文件头）上的全档实测值。
	wantKinds := map[equipmentRecipeKind]int{
		equipmentRecipeKindMaterialOutput: 5104,
		equipmentRecipeKindInPlaceUpgrade: 4,
		equipmentRecipeKindIntDataEncoded: 4417,
		equipmentRecipeKindUnsupported:    73,
	}
	if coverage.StackableEntries != 57709 || coverage.RecipeEntries != 9598 || coverage.Unsupported != 73 || !reflect.DeepEqual(coverage.ByKind, wantKinds) {
		t.Fatalf("coverage=%+v want kinds=%+v", coverage, wantKinds)
	}

	material, found, err := catalog.Recipe(10095704)
	if err != nil || !found {
		t.Fatalf("10095704 found=%t err=%v", found, err)
	}
	if material.Kind != equipmentRecipeKindMaterialOutput || material.GoldCostFound {
		t.Fatalf("10095704=%+v", material)
	}
	wantMaterials := []equipmentRecipeItemStack{
		equipmentRecipeTestStack(109040072, 1), equipmentRecipeTestStack(10000204, 15),
		equipmentRecipeTestStack(10000327, 75), equipmentRecipeTestStack(3167, 250),
		equipmentRecipeTestStack(3037, 2800),
	}
	if !reflect.DeepEqual(material.Materials, wantMaterials) {
		t.Fatalf("10095704 materials=%+v want=%+v", material.Materials, wantMaterials)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(109040073, 1)}; !reflect.DeepEqual(material.Outputs, want) {
		t.Fatalf("10095704 outputs=%+v want=%+v", material.Outputs, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(109040072, 1), equipmentRecipeTestStack(10150748, 1)}; !reflect.DeepEqual(material.FailOutputs, want) {
		t.Fatalf("10095704 fail outputs=%+v want=%+v", material.FailOutputs, want)
	}
	if material.SuccessRate != 1000 || !material.SuccessRateFound || material.MaintainAbility != "normal" || material.BroadcastType != 1 || material.MaintainSocketStat {
		t.Fatalf("10095704 success/maintain=%+v", material)
	}
	// 10095704 只有 [string data]，没有 [minimum itemmaking level]；缺段必须保持缺省，不能补默认值。
	if !reflect.DeepEqual(material.Specialties, []string{"[weaving]"}) || material.MinimumItemmakingFound || material.Price != 100000 || material.Grade != 84 || material.Rarity != 6 {
		t.Fatalf("10095704 specialty/audit=%+v", material)
	}
	if material.StackableKind != 1 || !material.StackableKindFound {
		t.Fatalf("10095704 stackable kind=%+v", material)
	}

	making, found, err := catalog.Recipe(10097920)
	if err != nil || !found {
		t.Fatalf("10097920 found=%t err=%v", found, err)
	}
	if making.Kind != equipmentRecipeKindMaterialOutput || making.MinimumItemmakingField != "weapon" || making.MinimumItemmakingLevel != 8 || !making.MinimumItemmakingFound {
		t.Fatalf("10097920=%+v", making)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(10000224, 5), equipmentRecipeTestStack(3167, 500), equipmentRecipeTestStack(109040206, 1), equipmentRecipeTestStack(10303569, 2500)}; !reflect.DeepEqual(making.Materials, want) {
		t.Fatalf("10097920 materials=%+v want=%+v", making.Materials, want)
	}
	if making.MonsterCardID != 0 || !making.MonsterCardFound || !reflect.DeepEqual(making.ItemCategories, []string{"use 2nd upgrade table"}) {
		t.Fatalf("10097920 card/category=%+v", making)
	}

	gold, found, err := catalog.Recipe(5065)
	if err != nil || !found {
		t.Fatalf("5065 found=%t err=%v", found, err)
	}
	if gold.Kind != equipmentRecipeKindMaterialOutput || !gold.GoldCostFound || gold.GoldCost != 50 {
		t.Fatalf("5065=%+v", gold)
	}

	inPlace, found, err := catalog.Recipe(10090321)
	if err != nil || !found {
		t.Fatalf("10090321 found=%t err=%v", found, err)
	}
	if inPlace.Kind != equipmentRecipeKindInPlaceUpgrade || len(inPlace.Outputs) != 0 || inPlace.ActionType != "[infinite upgrade recipe]" {
		t.Fatalf("10090321=%+v", inPlace)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(10090188, 5)}; !reflect.DeepEqual(inPlace.ExtraMaterials, want) {
		t.Fatalf("10090321 materials=%+v want=%+v", inPlace.ExtraMaterials, want)
	}
	if inPlace.CustomUpgradeType != "infinite_upgrade_event_20_level" {
		t.Fatalf("10090321 custom upgrade=%+v", inPlace)
	}

	intData, found, err := catalog.Recipe(2683031)
	if err != nil || !found {
		t.Fatalf("2683031 found=%t err=%v", found, err)
	}
	if intData.Kind != equipmentRecipeKindIntDataEncoded {
		t.Fatalf("2683031=%+v", intData)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(2683017, 1)}; !reflect.DeepEqual(intData.Materials, want) {
		t.Fatalf("2683031 materials=%+v want=%+v", intData.Materials, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(2683023, 1)}; !reflect.DeepEqual(intData.Outputs, want) {
		t.Fatalf("2683031 outputs=%+v want=%+v", intData.Outputs, want)
	}

	// 10005001 的 [int data] 是 "2 400320025 1 0 500000 1 400320026 1 0"：材料块里混着
	// id==0（疑似金币）的对。int data 里的 id==0 语义未证，所以整条按 unsupported 处理，
	// 既不把 0 当材料，也不从 int data 里认金币。
	unproven, found, err := catalog.Recipe(10005001)
	if err != nil || !found {
		t.Fatalf("10005001 found=%t err=%v", found, err)
	}
	if unproven.Kind != equipmentRecipeKindUnsupported || unproven.UnsupportedReason == "" || len(unproven.IntDataRemainder) == 0 || len(unproven.Materials) != 0 || len(unproven.Outputs) != 0 {
		t.Fatalf("10005001=%+v", unproven)
	}
	if want := []int64{2, 400320025, 1, 0, 500000, 1, 400320026, 1, 0}; !reflect.DeepEqual(unproven.IntData, want) {
		t.Fatalf("10005001 raw int data=%+v want=%+v", unproven.IntData, want)
	}

	// 10005320 的 [int data] 是严格形状（材料 1 对 + 产出 1 对 + 单个 0 结尾），可解。
	strict, found, err := catalog.Recipe(10005320)
	if err != nil || !found {
		t.Fatalf("10005320 found=%t err=%v", found, err)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(400350042, 1)}; strict.Kind != equipmentRecipeKindIntDataEncoded || !reflect.DeepEqual(strict.Materials, want) {
		t.Fatalf("10005320=%+v want materials=%+v", strict, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(400350043, 1)}; !reflect.DeepEqual(strict.Outputs, want) {
		t.Fatalf("10005320 outputs=%+v want=%+v", strict.Outputs, want)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(10005327, 100)}; !reflect.DeepEqual(strict.ExtraMaterials, want) {
		t.Fatalf("10005320 need material=%+v want=%+v", strict.ExtraMaterials, want)
	}

	if _, found, err := catalog.Recipe(999999999); err != nil || found {
		t.Fatalf("unknown id found=%t err=%v", found, err)
	}

	// 真实档里有 (id,0) 的零数量对（nrcp_2654516 的 "3174 0"）：不计入材料，但留证。
	zeroCount, found, err := catalog.Recipe(2654516)
	if err != nil || !found {
		t.Fatalf("2654516 found=%t err=%v", found, err)
	}
	if want := []equipmentRecipeItemStack{equipmentRecipeTestStack(3174, 0)}; !reflect.DeepEqual(zeroCount.ZeroCountPairs, want) {
		t.Fatalf("2654516 zero-count pairs=%+v want=%+v", zeroCount.ZeroCountPairs, want)
	}
	for _, pair := range zeroCount.Materials {
		if pair.Count == 0 {
			t.Fatalf("2654516 counted a zero-count pair as material: %+v", zeroCount.Materials)
		}
	}
}

func TestEquipmentRecipeCatalogUnsupportedEntriesExposeNoTransactionalPayload(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		wantInt  []int64
		wantZero []equipmentRecipeItemStack
	}{
		{
			name: "explicit input item without output or upgrade",
			doc: "[stackable type]\n`[recipe]` 0\n" +
				"[input item]\n3600 1 3227 8 \n[/input item]\n" +
				"[output item]\n[/output item]\n" +
				"[need skill]\n191 4 \n",
		},
		{
			name: "need material fail output and bead item without output",
			doc: "[stackable type]\n`[recipe]` 0\n" +
				"[need material]\n10090188 5 \n[/need material]\n" +
				"[fail output]\n10150748 1 \n[/fail output]\n" +
				"[bead item]\n3037 1 \n",
		},
		{
			name: "unproven int data keeps raw evidence",
			doc: "[stackable type]\n`[recipe]` 1\n" +
				"[int data]\n2 400320025 1 0 500000 1 400320026 1 0 \n[/int data]\n",
			wantInt: []int64{2, 400320025, 1, 0, 500000, 1, 400320026, 1, 0},
		},
		{
			name: "zero count pair preserved but not transactional",
			doc: "[stackable type]\n`[recipe]` 0\n" +
				"[input item]\n3174 0 \n[/input item]\n",
			wantZero: []equipmentRecipeItemStack{equipmentRecipeTestStack(3174, 0)},
		},
		{
			name: "gold pair without output is not transactional",
			doc: "[stackable type]\n`[recipe]` 0\n" +
				"[input item]\n0 500000 \n[/input item]\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := dungeonDropCatalogTestSource{
				equipmentRecipeStackableListPath:     "42 `recipe/unsupported_x.stk`\n",
				"stackable/recipe/unsupported_x.stk": test.doc,
			}
			catalog, err := newPVFEquipmentRecipeCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			entry, found, err := catalog.Recipe(42)
			if err != nil || !found {
				t.Fatalf("entry found=%t err=%v", found, err)
			}
			if entry.Kind != equipmentRecipeKindUnsupported {
				t.Fatalf("kind=%s want=%s", entry.Kind, equipmentRecipeKindUnsupported)
			}
			if entry.UnsupportedReason == "" {
				t.Fatalf("unsupported reason is empty: %+v", entry)
			}
			if len(entry.Materials) != 0 || len(entry.ExtraMaterials) != 0 || len(entry.Outputs) != 0 ||
				len(entry.FailOutputs) != 0 || len(entry.BeadItems) != 0 {
				t.Fatalf("unsupported entry exposed transactional payload: %+v", entry)
			}
			if entry.GoldCostFound || entry.GoldCost != 0 {
				t.Fatalf("unsupported entry exposed gold cost: %+v", entry)
			}
			if !reflect.DeepEqual(entry.IntData, test.wantInt) {
				t.Fatalf("int data=%+v want=%+v", entry.IntData, test.wantInt)
			}
			if !reflect.DeepEqual(entry.ZeroCountPairs, test.wantZero) {
				t.Fatalf("zero count pairs=%+v want=%+v", entry.ZeroCountPairs, test.wantZero)
			}
		})
	}
}

func TestEquipmentRecipeCatalogIntDataCountOverflowFailsClosed(t *testing.T) {
	maxInt64 := int64(math.MaxInt64)
	cases := []struct {
		name   string
		values []int64
	}{
		{name: "material count is max int64", values: []int64{maxInt64}},
		{name: "material count with trailing values", values: []int64{maxInt64, 1, 1}},
		{name: "output count is max int64", values: []int64{1, 3227, 1, maxInt64}},
		{name: "both counts are max int64", values: []int64{maxInt64, maxInt64}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("decode panicked on overflow input: %v", recovered)
				}
			}()
			materials, outputs, _, ok := decodeEquipmentRecipeIntData(test.values)
			if ok || len(materials) != 0 || len(outputs) != 0 {
				t.Fatalf("overflow input decoded ok=%t materials=%+v outputs=%+v", ok, materials, outputs)
			}
		})
	}

	source := dungeonDropCatalogTestSource{
		equipmentRecipeStackableListPath: "43 `recipe/overflow.stk`\n",
		"stackable/recipe/overflow.stk": "[stackable type]\n`[recipe]` 1\n" +
			"[int data]\n" + strconv.FormatInt(maxInt64, 10) + " \n[/int data]\n",
	}
	catalog, err := newPVFEquipmentRecipeCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := catalog.Recipe(43)
	if err != nil || !found {
		t.Fatalf("entry found=%t err=%v", found, err)
	}
	if entry.Kind != equipmentRecipeKindUnsupported || len(entry.Materials) != 0 || len(entry.Outputs) != 0 || entry.UnsupportedReason == "" {
		t.Fatalf("overflow document=%+v", entry)
	}
}
