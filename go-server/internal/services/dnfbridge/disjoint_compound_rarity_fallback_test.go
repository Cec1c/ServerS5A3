package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"sort"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 装备分解（op26）"来源被判非法"兜底的聚焦回归。
//
// 操作者现场看到的是"目前无法分解"（= 失败码 19，来源不合法）。本轮按 86JP 参考
// 实现（`a21zigai/86JP/Server/DfoServer/Game/Inventory/DisjointResultCalculator.cs`
// 与 `Tool/PvfLib/Models/DisjointFile.cs`、`ItemMetadataResolver.cs`）逐条对齐了
// "参考实现有兜底、我们没有"的字段：
//
//   - `EquipmentFile.Rarity` 默认 -1（PVF 文档没有 [rarity]）；参考实现
//     `DisjointResultCalculator.Calculate` 用 `metadata?.Rarity ?? 0`，
//     `DisjointFile.MapRarityToCubeCreationIndex` / `MapRarityToAdditionalIndex`
//     再把负值折到第 0 行，于是缺 [rarity] 的装备照样分解、至少 1 个无色小晶块。
//     旧代码在这里 `return errCurrentDisjointRewardInvalid`，客户端显示成
//     "目前无法分解"。
//   - 当前 PVF 保留 0..6 每一档；第 5 档显式禁用，第 6 档为传说。
//     越界或缺失稀有度没有 expand，不能借用邻行的概率表。
//   - `[value]/[price]` 缺失走 `Math.Max(1, SellGold ?? 1)`（上一轮已修）。
//
// 运行时 PVF 全量枚举见 TestRealPVFDisjointEveryEquipmentSourceDecomposes：
// 修复前只有 6 个装备文档会被判非法（100300084..100300089），修复后 0 个。

// currentDisjointRarityFallbackAssets 造一套与运行时 etc/disjoint.etc 同形、但把
// 第 0 行与第 1 行**故意做成可区分**的表：无色小晶块倍率 `[1 3]`、附加产物
// `[3033 3034 ...]`、expand `[10100115 10100116 ...]`。任何"索引没落到第 0 行"
// 的实现都会断言失败。`[additional result const]` 故意不写：Go 侧解析不出行时
// `currentDisjointAdditionalCount` 返回 1，产物数量因此是确定的 1。
func currentDisjointRarityFallbackAssets(t *testing.T, slots map[string]dnfrepo.ItemStack) (dnfrepo.Group, *pvfDungeonDropCatalog) {
	t.Helper()
	const materialStackable = "[stackable type] `[material]`\n[stack limit] 999\n"
	source := bridgePVFSource{
		"monster/monster.lst": "",
		"equipment/equipment.lst": "5000 `no_rarity_coat.equ`\n5001 `negative_rarity_coat.equ`\n" +
			"5002 `rarity_seven_coat.equ`\n5003 `rarity_one_coat.equ`\n5004 `rarity_five_coat.equ`\n" +
			"5005 `no_rarity_no_value.equ`\n5100 `avatar_hat.equ`\n",
		"stackable/stackable.lst": "3037 `cube.stk`\n3033 `red.stk`\n3034 `white.stk`\n3035 `black.stk`\n" +
			"3036 `blue.stk`\n3166 `element_a.stk`\n3229 `misc.stk`\n3332 `misc2.stk`\n4000 `potion.stk`\n" +
			"10100115 `soul1.stk`\n10100116 `soul2.stk`\n10099775 `soul5.stk`\n",
		"stackable/cube.stk":      materialStackable,
		"stackable/red.stk":       materialStackable,
		"stackable/white.stk":     materialStackable,
		"stackable/black.stk":     materialStackable,
		"stackable/blue.stk":      materialStackable,
		"stackable/element_a.stk": materialStackable,
		"stackable/misc.stk":      materialStackable,
		"stackable/misc2.stk":     materialStackable,
		"stackable/potion.stk":    materialStackable,
		"stackable/soul1.stk":     materialStackable,
		"stackable/soul2.stk":     materialStackable,
		"stackable/soul5.stk":     materialStackable,
		// 没有 [rarity]、没有 [value]/[price]：运行时
		// equipment/character/common/etc/100300084..100300089 的形状。
		"equipment/no_rarity_coat.equ": "[equipment type] `[coat]`\n[minimum level] 10\n[value] 300\n",
		// 显式负 rarity：PvfLib 的 EquipmentFile.Rarity 默认值形状。
		"equipment/negative_rarity_coat.equ": "[equipment type] `[coat]`\n[rarity] -1\n[minimum level] 10\n[value] 300\n",
		// 越界 rarity：倍率/附加产物折回第 0 行，expand 没有行。
		"equipment/rarity_seven_coat.equ": "[equipment type] `[coat]`\n[rarity] 7\n[minimum level] 10\n[value] 300\n",
		"equipment/rarity_one_coat.equ":   "[equipment type] `[coat]`\n[rarity] 1\n[minimum level] 10\n[value] 300\n",
		// rarity 5：附加产物走第 5 行，但 expand 第5行在当前PVF中显式禁用（6保留在第6行）。
		"equipment/rarity_five_coat.equ": "[equipment type] `[coat]`\n[rarity] 5\n[minimum level] 10\n[value] 300\n",
		// 缺 rarity 也缺 value/price：两个兜底同时生效，仍产出 1 个无色小晶块。
		"equipment/no_rarity_no_value.equ": "[equipment type] `[coat]`\n[minimum level] 1\n",
		"equipment/avatar_hat.equ":         "[equipment type] `[hat avatar]`\n[usable job] `[fighter]`\n[grade] 2\n",
		"etc/disjoint.etc": "[cube index]\n`[no element]` 3037\n[/cube index]\n" +
			"[cube creation const]\n150 1 3\n[/cube creation const]\n" +
			"[additional result]\n1 3033 1 3034 1 3035 1 3036 1 3166 1 3229 1 3332\n[/additional result]\n" +
			"[additional result expand]\n1 10100115 1 10100116 1 10099773 1 10099774 1 10158124 0 1 10099775 1 10158124\n[/additional result expand]\n" +
			"[additional result expand const]\n25 0.00 100 25 0.00 100 25 0.00 100 25 0.00 100 25 0.00 100 0 25 0.00 100 25 0.00 100\n[/additional result expand const]\n" +
			"[avatar disjoint info]\n[fighter]\n2559000 2559003 2559006 2559009 2559006 2559001 2559004 2559007 2559010 2559007 2559002 2559005 2559008 2559011 2559014\n[/fighter]\n[/avatar disjoint info]\n",
		"etc/emblemcompound.etc": "[emblem compound info]\n1 2 9000\n[/emblem compound info]\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := mustCurrentNPCShopRepositories(t, 0, slots)
	return repositories, catalog
}

func currentDisjointFallbackDocument(t *testing.T, catalog *pvfDungeonDropCatalog, itemID uint32) (*dnfpvf.Document, string) {
	t.Helper()
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatalf("resolve %d: %v", itemID, err)
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		t.Fatalf("parse %s: %v", definition.PVFPath, err)
	}
	return document, definition.PVFPath
}

// TestCurrentDisjointRarityIndexMappings pins current-PVF rarity
// 映射函数，包括"缺 [rarity] → -1 → 第 0 行 / 没有 expand"这一条。
func TestCurrentDisjointRarityIndexMappings(t *testing.T) {
	for _, tc := range []struct {
		rarity int64
		row    int
		expand int
	}{
		{-1, 0, -1},
		{0, 0, 0},
		{1, 1, 1},
		{4, 4, 4},
		{5, 5, 5},
		{6, 6, 6},
		{7, 7, -1},
		{64, 64, -1},
	} {
		if got := currentDisjointRarityRowIndex(tc.rarity); got != tc.row {
			t.Fatalf("row index rarity=%d got=%d want=%d", tc.rarity, got, tc.row)
		}
		if got := currentDisjointRarityExpandIndex(tc.rarity); got != tc.expand {
			t.Fatalf("expand index rarity=%d got=%d want=%d", tc.rarity, got, tc.expand)
		}
	}
}

// TestCurrentDisjointMissingRarityFallsBackToRowZero 是现场那条的聚焦回归：
// 缺 [rarity]（或无 [rarity] 且无 [value]/[price]）的装备必须按第 0 行产出，
// 而不是被判"奖励映射非法"。
func TestCurrentDisjointMissingRarityFallsBackToRowZero(t *testing.T) {
	_, catalog := currentDisjointRarityFallbackAssets(t, nil)
	config, err := loadCurrentDisjointPVFConfig(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		itemID  uint32
		rewards []currentDisjointReward
	}{
		// 倍率表 [1 3] + [value] 300 + base 150：第 0 行 2 个、第 1 行 6 个。
		// 缺 [rarity] ⇒ -1 ⇒ 倍率/附加产物走第 0 行，但 expand **没有行**
		// （参考实现 MapRarityToExpandIndex(-1) = -1），所以产物里没有灵魂。
		{"missing [rarity] uses cube row 0", 5000, []currentDisjointReward{{ItemID: 3037, Count: 2}, {ItemID: 3033, Count: 1}}},
		{"negative [rarity] uses cube row 0", 5001, []currentDisjointReward{{ItemID: 3037, Count: 2}, {ItemID: 3033, Count: 1}}},
		{"missing [rarity] and missing value yields one cube", 5005, []currentDisjointReward{{ItemID: 3037, Count: 1}, {ItemID: 3033, Count: 1}}},
		{"rarity 1 uses row 1", 5003, []currentDisjointReward{{ItemID: 3037, Count: 6}, {ItemID: 3034, Count: 1}, {ItemID: 10100116, Count: 1}}},
		// rarity 7：倍率折回第 0 行（2），附加产物越界 → 无，expand 没有行 → 无。
		{"out-of-range rarity 7 falls back to row 0 without expand", 5002, []currentDisjointReward{{ItemID: 3037, Count: 2}}},
		// rarity 5：附加产物走第 5 行，但 expand 第5行在当前PVF中显式禁用。
		{"rarity 5 has no expand row", 5004, []currentDisjointReward{{ItemID: 3037, Count: 2}, {ItemID: 3229, Count: 1}}},
	} {
		document, path := currentDisjointFallbackDocument(t, catalog, tc.itemID)
		rewards, err := currentEquipmentDisjointRewards(config, document)
		if err != nil {
			t.Fatalf("%s (item %d, %s): %v", tc.name, tc.itemID, path, err)
		}
		if len(rewards) != len(tc.rewards) {
			t.Fatalf("%s (item %d): rewards=%+v want %+v", tc.name, tc.itemID, rewards, tc.rewards)
		}
		for index := range tc.rewards {
			if rewards[index] != tc.rewards[index] {
				t.Fatalf("%s (item %d): rewards=%+v want %+v", tc.name, tc.itemID, rewards, tc.rewards)
			}
		}
	}
}

// TestCurrentDisjointMissingRarityEndToEndStillDecomposes 走真实 op26：缺 [rarity]
// 的来源必须回成功包、消耗来源、把无色小晶块落进账号晶块仓库。
func TestCurrentDisjointMissingRarityEndToEndStillDecomposes(t *testing.T) {
	repositories, catalog := currentDisjointRarityFallbackAssets(t, map[string]dnfrepo.ItemStack{
		"0:65": {ItemID: 5000, Count: 1},
	})
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(65, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("equipment without [rarity] must decompose, got ACK=%x trailing=%d", ack.Body, len(trailing))
	}
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if cube := currentDisjointRewardRowForItem(t, rows, 3037); cube != (currentDisjointRewardRow{Slot: 358, ItemID: 3037, Granted: 2}) {
		t.Fatalf("cube row=%+v want item 3037 slot 358 granted 2", cube)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if _, exists := inventory.Slots["0:65"]; exists {
		t.Fatal("source without [rarity] was not consumed")
	}
	account, _, _ := repositories.AccountInventory.Load(context.Background(), "account-1")
	if stack := account.Slots["0:358"]; stack.ItemID != 3037 || stack.Count != 2 {
		t.Fatalf("crystal warehouse 358=%+v want 3037x2", stack)
	}
}

// TestCurrentDisjointStillRejectsTrulyInvalidSources 钉住"只补兜底、不放宽非法来源"：
// 非装备（堆叠物）、时装、空槽、账号晶块仓库槽都仍然回通用失败码 19，来源不被消耗、
// 也不产出任何东西。
func TestCurrentDisjointStillRejectsTrulyInvalidSources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		slots map[string]dnfrepo.ItemStack
		slot  int16
	}{
		{
			name:  "stackable source is not equipment",
			slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 4000, Count: 1}},
			slot:  65,
		},
		{
			name:  "avatar source belongs to the avatar disjoint",
			slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 5100, Count: 1}},
			slot:  65,
		},
		{
			name:  "empty slot",
			slots: map[string]dnfrepo.ItemStack{},
			slot:  65,
		},
		{
			name:  "crystal warehouse slot is not a disjoint source",
			slots: map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: 5}},
			slot:  358,
		},
	} {
		repositories, catalog := currentDisjointRarityFallbackAssets(t, tc.slots)
		seedCurrentDisjointAccount(t, repositories, nil)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(tc.slot, 0, -1, 0)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || !bytes.Equal(failure.Body, []byte{0, currentDisjointFailureInvalid}) {
			t.Fatalf("%s: failure=%x want code %d", tc.name, failure.Body, currentDisjointFailureInvalid)
		}
		inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
		for key, before := range tc.slots {
			after, exists := inventory.Slots[key]
			if !exists || after.ItemID != before.ItemID || after.Count != before.Count {
				t.Fatalf("%s: rejected source mutated at %s: exists=%t %+v", tc.name, key, exists, after)
			}
		}
		account, _, _ := repositories.AccountInventory.Load(context.Background(), "account-1")
		if len(account.Slots) != 0 {
			t.Fatalf("%s: rejected source still granted %+v", tc.name, account.Slots)
		}
	}
}

// initialEquipmentDisjointRow 造出运行时会话里"新手装备"那一行的真实形状：
// `buildInitialEquipmentRawEntry` 的 43 字节 legacy raw + `initialEquipmentRecord`
// 写的 Extra（**没有** item_kind / quality_seed）。unequipped=true 时再补上
// equip/owner.go `stackFromEntry` 在脱下时镜像的 raw_entry_hex / equipment_slot。
func initialEquipmentDisjointRow(t *testing.T, slot int16, itemID int64, pvfPath string, unequipped bool) dnfrepo.ItemStack {
	t.Helper()
	record := initialEquipmentRecord("19", []initialEquipmentEntry{{
		Slot:       slot,
		ItemID:     itemID,
		Durability: 45,
		PVFPath:    pvfPath,
	}}, time.Now())
	entry, found := record.Entries["9"]
	if !found {
		t.Fatalf("initial equipment record has no slot entry: %+v", record.Entries)
	}
	stack := dnfrepo.ItemStack{
		ItemID:   entry.ItemID,
		Count:    1,
		RawEntry: append([]byte(nil), entry.RawEntry...),
		Extra:    entry.Extra,
	}
	if stack.Extra["item_kind"] != "" || stack.Extra["quality_seed"] != "" {
		t.Fatalf("seeded equipment row must not carry item_kind/quality_seed: %+v", stack.Extra)
	}
	if unequipped {
		stack.Extra["equipment_slot"] = "9"
		stack.Extra["raw_entry_hex"] = hex.EncodeToString(entry.RawEntry)
	}
	return stack
}

// TestInitialEquipmentSeededRowStillDecomposes 钉住操作者报的那一条：`source =
// pvf_create_equipment_list` 的播种行（43 字节 legacy raw、Extra 里没有
// item_kind / quality_seed）必须能正常分解。分解链的来源合法性来自运行时 PVF 的
// `equipment/equipment.lst` 归属（`catalog.ResolveItem`），**不读** Extra 里的
// item_kind；这里用生产播种函数把形状钉死。
func TestInitialEquipmentSeededRowStillDecomposes(t *testing.T) {
	const (
		crossItemID = 108000525 // 训练用十字架 / equipment/character/priest/weapon/cross/108000525.equ
		pantItemID  = 12400     // 残破的牛皮短裤 / equipment/character/common/pants/leather/pant_crleather.equ
	)
	source := bridgePVFSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "108000525 `cross.equ`\n12400 `pant.equ`\n",
		"stackable/stackable.lst": "3037 `cube.stk`\n3166 `element.stk`\n10100115 `soul.stk`\n",
		"stackable/cube.stk":      "[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/element.stk":   "[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/soul.stk":      "[stackable type] `[material]`\n[stack limit] 999\n",
		"equipment/cross.equ":     "[equipment type] `[weapon]`\n[rarity] 0\n[minimum level] 1\n[value] 504\n[price] 700\n[attach type] `[free]`\n",
		"equipment/pant.equ":      "[equipment type] `[pants]`\n[rarity] 0\n[minimum level] 1\n[value] 288\n[price] 400\n[attach type] `[free]`\n",
		"etc/disjoint.etc": "[cube index]\n`[no element]` 3037\n[/cube index]\n" +
			"[cube creation const]\n150 1.12 1.35\n[/cube creation const]\n" +
			"[additional result]\n0 1 3166 1 3166\n[/additional result]\n" +
			"[additional result expand]\n0\n[/additional result expand]\n" +
			"[additional result expand const]\n0\n[/additional result expand const]\n" +
			"[avatar disjoint info]\n[fighter]\n2559000 2559003 2559006 2559009 2559006 2559001 2559004 2559007 2559010 2559007 2559002 2559005 2559008 2559011 2559014\n[/fighter]\n[/avatar disjoint info]\n",
		"etc/emblemcompound.etc": "[emblem compound info]\n1 2 9000\n[/emblem compound info]\n",
	}
	for _, tc := range []struct {
		name       string
		itemID     int64
		pvfPath    string
		unequipped bool
	}{
		{"seeded cross (43-byte raw, no item_kind)", crossItemID, "equipment/cross.equ", false},
		{"unequipped cross (43-byte raw + raw_entry_hex mirror)", crossItemID, "equipment/cross.equ", true},
		{"seeded pant (43-byte raw, no item_kind)", pantItemID, "equipment/pant.equ", false},
		{"unequipped pant (43-byte raw + raw_entry_hex mirror)", pantItemID, "equipment/pant.equ", true},
	} {
		catalog, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			t.Fatal(err)
		}
		stack := initialEquipmentDisjointRow(t, 9, tc.itemID, tc.pvfPath, tc.unequipped)
		repositories := mustCurrentNPCShopRepositories(t, 0, map[string]dnfrepo.ItemStack{"0:9": stack})
		seedCurrentDisjointAccount(t, repositories, nil)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
			t.Fatalf("%s: seeded initial equipment must decompose, got ACK=%x trailing=%d", tc.name, ack.Body, len(trailing))
		}
		rows := parseCurrentDisjointRewardRows(t, ack.Body)
		if cube := currentDisjointRewardRowForItem(t, rows, 3037); cube.Slot != 358 || cube.Granted < 1 {
			t.Fatalf("%s: cube row=%+v want at least one clear cube in warehouse slot 358", tc.name, cube)
		}
		inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
		if _, exists := inventory.Slots["0:9"]; exists {
			t.Fatalf("%s: seeded source was not consumed", tc.name)
		}
	}
}

// TestRealPVFDisjointEveryEquipmentSourceDecomposes 是运行时 Script.pvf 的全量枚举：
//  1. 列出本轮兜底救回来的装备（缺/负 [rarity]）——这正是给操作者核对"你分解的是
//     不是这一件"用的清单；
//  2. 顺带列出上一轮 [value]/[price] 兜底救回来的称号/纹章数量；
//  3. 断言修复后**没有任何**非时装装备会被判"奖励映射非法"，且清单里的每一件都能
//     产出无色小晶块（含端到端 op26）；
//  4. 断言真正非法的来源（非装备）仍然回失败码 19。
func TestRealPVFDisjointEveryEquipmentSourceDecomposes(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentDisjointPVFConfig(archive)
	if err != nil {
		t.Fatal(err)
	}

	type equipmentSource struct {
		itemID  uint32
		path    string
		missing string
		name    string
	}
	fixedThisRound := make([]equipmentSource, 0, 8)
	fixedPrevRound := make([]equipmentSource, 0, 8192)
	stillRejected := make([]equipmentSource, 0, 8)
	scanned := 0
	avatars := 0
	ids := make([]uint32, 0, len(catalog.itemRefs))
	for itemID, reference := range catalog.itemRefs {
		if reference.kind == dungeonDropItemEquipment {
			ids = append(ids, itemID)
		}
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	for _, itemID := range ids {
		definition, resolveErr := catalog.ResolveItem(itemID)
		if resolveErr != nil {
			continue
		}
		document, parseErr := parseDungeonCardPVFDocument(archive, definition.PVFPath)
		if parseErr != nil {
			stillRejected = append(stillRejected, equipmentSource{itemID, definition.PVFPath, "parse-error", ""})
			continue
		}
		if currentDisjointDocumentIsAvatar(document) {
			// 时装走 op202（currentAvatarDisjointRewards），不在装备 op26 链上。
			avatars++
			continue
		}
		scanned++
		rarity, rarityFound := document.Int("rarity")
		_, valueFound := document.Int("value")
		_, priceFound := document.Int("price")
		missingRarity := !rarityFound || rarity < 0
		missingSellValue := !valueFound && !priceFound
		rewards, rewardErr := currentEquipmentDisjointRewards(config, document)
		if rewardErr != nil {
			stillRejected = append(stillRejected, equipmentSource{itemID, definition.PVFPath, "reward-mapping-invalid", ""})
			continue
		}
		// 兜底后必须仍然产出：首个产物是无色小晶块且数量 ≥ 1。
		if len(rewards) == 0 || rewards[0].ItemID != config.CubeItemID || rewards[0].Count < 1 {
			t.Fatalf("item=%d path=%s rewards=%+v want at least one clear cube", itemID, definition.PVFPath, rewards)
		}
		if missingRarity {
			// 本轮修的那一类：修复前在 `document.Int("rarity")` 的
			// `!found || rarity < 0` 判断上直接判"奖励映射非法"。
			name, _ := document.Text("name")
			fixedThisRound = append(fixedThisRound, equipmentSource{itemID, definition.PVFPath, "[rarity]", name})
			// 运行时 etc/disjoint.etc 的 `[additional result]` 第 0 行是空的
			// （`0 4 3033 ...`），expand 也没有 rarity -1 的行 ⇒ 这几件的产物
			// 恰好是一个无色小晶块（倍率表第 0 行 1.12、卖价兜底 1 ⇒ max(1, 0)）。
			if len(rewards) != 1 || rewards[0] != (currentDisjointReward{ItemID: config.CubeItemID, Count: 1}) {
				t.Fatalf("item=%d path=%s rewards=%+v want exactly one clear cube", itemID, definition.PVFPath, rewards)
			}
			t.Logf("  FIXED item=%d name=%s path=%s missing=[rarity] rewards=%+v", itemID, name, definition.PVFPath, rewards)
		}
		if missingSellValue {
			// 上一轮修的那一类：`[value]/[price]` 缺失走 Math.Max(1, ...) 兜底。
			fixedPrevRound = append(fixedPrevRound, equipmentSource{itemID, definition.PVFPath, "[value]/[price]", ""})
		}
	}
	t.Logf("runtime equipment sources scanned=%d avatar-skipped=%d", scanned, avatars)
	t.Logf("fixed this round (missing [rarity]) count=%d", len(fixedThisRound))
	t.Logf("fixed last round (missing [value]/[price]) count=%d", len(fixedPrevRound))
	for index, entry := range fixedPrevRound {
		if index >= 10 {
			t.Logf("  ... %d more", len(fixedPrevRound)-index)
			break
		}
		t.Logf("  MISSING [value]/[price] item=%d path=%s", entry.itemID, entry.path)
	}
	if len(stillRejected) != 0 {
		t.Fatalf("equipment sources still rejected as invalid: %+v", stillRejected)
	}
	want := []equipmentSource{
		{100300084, "equipment/character/common/etc/100300084.equ", "[rarity]", "黑色小晶块效果"},
		{100300085, "equipment/character/common/etc/100300085.equ", "[rarity]", "蓝色小晶块效果"},
		{100300086, "equipment/character/common/etc/100300086.equ", "[rarity]", "红色小晶块效果"},
		{100300087, "equipment/character/common/etc/100300087.equ", "[rarity]", "白色小晶块效果"},
		{100300088, "equipment/character/common/etc/100300088.equ", "[rarity]", "无色小晶块效果"},
		{100300089, "equipment/character/common/etc/100300089.equ", "[rarity]", "金色小晶块效果"},
	}
	if len(fixedThisRound) != len(want) {
		t.Fatalf("runtime missing-[rarity] equipment changed: got %+v want %+v", fixedThisRound, want)
	}
	for index := range want {
		if fixedThisRound[index] != want[index] {
			t.Fatalf("runtime missing-[rarity] equipment[%d]=%+v want %+v", index, fixedThisRound[index], want[index])
		}
	}
	if len(fixedPrevRound) == 0 {
		t.Fatal("runtime PVF no longer has any equipment without [value]/[price]")
	}

	// 端到端：清单里的第一件放进真实 op26 请求，必须成功、来源被消耗、晶块进仓库。
	repositories := mustCurrentNPCShopRepositories(t, 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: int64(want[0].itemID), Count: 1},
	})
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("runtime %d must decompose, got ACK=%x trailing=%d", want[0].itemID, ack.Body, len(trailing))
	}
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if cube := currentDisjointRewardRowForItem(t, rows, config.CubeItemID); cube.Slot != 358 || cube.Granted < 1 {
		t.Fatalf("runtime %d cube row=%+v want at least one clear cube in warehouse 358", want[0].itemID, cube)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if _, exists := inventory.Slots["0:9"]; exists {
		t.Fatalf("runtime %d source was not consumed", want[0].itemID)
	}

	// 真正非法的来源仍然回 19：拿运行时 stackable 列表里的第一件当 op26 来源。
	if len(catalog.stackableIDs) == 0 {
		t.Fatal("runtime PVF has no stackable source to prove the invalid path")
	}
	invalidSourceID := catalog.stackableIDs[0]
	repositories, catalog2 := currentDisjointRarityFallbackAssets(t, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: int64(invalidSourceID), Count: 1},
	})
	seedCurrentDisjointAccount(t, repositories, nil)
	connection = &bufferConn{}
	service = currentDisjointTestService(repositories, catalog2)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || !bytes.Equal(failure.Body, []byte{0, currentDisjointFailureInvalid}) {
		t.Fatalf("non-equipment source must report code %d, got %x", currentDisjointFailureInvalid, failure.Body)
	}
	inventory, _, _ = repositories.Inventory.Load(context.Background(), "19")
	if stack := inventory.Slots["0:9"]; stack.ItemID != int64(invalidSourceID) || stack.Count != 1 {
		t.Fatalf("non-equipment source mutated: %+v", stack)
	}
}

// TestRealPVFDisjointInitialEquipmentDecomposes 用运行时 PVF 的
// `[create equipment list]` 播种项（每个职业的武器/上衣/下装）打一遍：奖励解析必须
// 成功，且操作者点名的三件（训练用十字架 / 残破的牛皮短裤 / 残破的牛皮上衣）
// 以"脱下后"的行形状端到端 op26 成功。
func TestRealPVFDisjointInitialEquipmentDecomposes(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentDisjointPVFConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	byJob, err := parseInitialCharacterEquipmentAllFromSource(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(byJob) == 0 {
		t.Fatal("runtime PVF has no initial equipment")
	}
	for job, entries := range byJob {
		for _, entry := range entries {
			document, parseErr := parseDungeonCardPVFDocument(archive, entry.PVFPath)
			if parseErr != nil {
				t.Fatalf("job=%d item=%d path=%s: %v", job, entry.ItemID, entry.PVFPath, parseErr)
			}
			rewards, rewardErr := currentEquipmentDisjointRewards(config, document)
			if rewardErr != nil || len(rewards) == 0 || rewards[0].ItemID != config.CubeItemID || rewards[0].Count < 1 {
				t.Fatalf("job=%d slot=%d item=%d path=%s rewards=%+v err=%v", job, entry.Slot, entry.ItemID, entry.PVFPath, rewards, rewardErr)
			}
		}
	}
	for _, itemID := range []int64{108000525, 12400, 10400} {
		definition, resolveErr := catalog.ResolveItem(uint32(itemID))
		if resolveErr != nil {
			t.Fatalf("runtime item %d: %v", itemID, resolveErr)
		}
		stack := initialEquipmentDisjointRow(t, 9, itemID, definition.PVFPath, true)
		repositories := mustCurrentNPCShopRepositories(t, 0, map[string]dnfrepo.ItemStack{"0:9": stack})
		seedCurrentDisjointAccount(t, repositories, nil)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
			t.Fatal(err)
		}
		ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || len(ack.Body) == 0 || ack.Body[0] != 1 {
			t.Fatalf("runtime initial equipment %d (%s) must decompose, got ACK=%x trailing=%d", itemID, definition.PVFPath, ack.Body, len(trailing))
		}
		rows := parseCurrentDisjointRewardRows(t, ack.Body)
		if cube := currentDisjointRewardRowForItem(t, rows, config.CubeItemID); cube.Slot != 358 || cube.Granted < 1 {
			t.Fatalf("runtime initial equipment %d cube row=%+v", itemID, cube)
		}
		inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
		if _, exists := inventory.Slots["0:9"]; exists {
			t.Fatalf("runtime initial equipment %d source was not consumed", itemID)
		}
	}
}

// TestCurrentDisjointOwnerOwnsTheCommit 确认缺 [rarity] 的来源走的是既有 domain
// owner 事务：成功后来源被消耗，桥接层不自建旁路行。
func TestCurrentDisjointOwnerOwnsTheCommit(t *testing.T) {
	repositories, catalog := currentDisjointRarityFallbackAssets(t, map[string]dnfrepo.ItemStack{
		"0:65": {ItemID: 5000, Count: 1},
	})
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(65, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, trailing := splitGameServerUpperPacket(t, connection.write.Bytes()); len(trailing) != 0 {
		t.Fatalf("unexpected trailing packets: %d", len(trailing))
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	for key, stack := range inventory.Slots {
		if stack.ItemID == 5000 {
			t.Fatalf("disjoint left the source equipment behind at %s: %+v", key, stack)
		}
	}
}
