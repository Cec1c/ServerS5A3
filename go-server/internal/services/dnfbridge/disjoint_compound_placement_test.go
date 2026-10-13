package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 装备分解（op26）产出落点 / 空间判定 / 批量数量的聚焦回归。
//
// 运行时 Script.pvf 的事实（本次用真实归档核对；参考实现为 86JP
// DfoServer/Game/Inventory/DisjointResultCalculator.cs 与 ItemSlotBoundService.cs）：
//   - etc/disjoint.etc 的 `[additional result]` 把 rarity 2..6 指向 3166/3167/
//     3229/3332，`[additional result expand]` 指向 10100115/10100116/10099773/
//     10099774/10158124，全部是 `[stackable type] [material]` ⇒ 主背包材料段
//     121..176（与 86JP ItemSlotBoundService KindMaterial 的 121..176 完全一致）。
//   - 无色/有色小晶块（3033..3037/3262）与灵魂（10100115..10158124）按既有已验证
//     结论落在账号共享的晶块/灵魂仓库 354..359 / 360..365。
//   - 运行时 PVF 里绝大多数 `[equipment type] [title name]` 称号与 `[crest]` 纹章
//     没有 [value]/[price]；86JP DisjointResultCalculator.CalculateCubeCount 用
//     `Math.Max(1, metadata.SellGold ?? 1)` 兜底，仍然产出至少 1 个无色小晶块。
//   - 失败码：4 是客户端唯一渲染成"物品栏空间不足，无法获得分解物品。请整理物品
//     快捷栏、材料栏或副职材料栏后再试。"的码（86JP ErrorInventoryFull = 0x04），
//     其余拒收用通用码 19（86JP ErrorInvalidRequest/ErrorInvalidTarget = 0x13）。

type currentDisjointRewardRow struct {
	Slot    int16
	ItemID  uint32
	Granted uint32
}

func mustCurrentDisjointPlacementAssets(t *testing.T, slots map[string]dnfrepo.ItemStack) (dnfrepo.Group, *pvfDungeonDropCatalog) {
	t.Helper()
	// 表形与运行时 etc/disjoint.etc 一致；只把 expand 的成功概率钉成 0，让产出
	// 数量可确定断言（真实概率表由 TestRealPVFDisjointPlacesRewardsInRuntimeSegments
	// 覆盖）。
	source := bridgePVFSource{
		"monster/monster.lst":          "",
		"stackable/stackable.lst":      "3037 `cube.stk`\n3033 `colored.stk`\n3166 `element.stk`\n10099773 `soul.stk`\n9000 `booster.stk`\n9001 `result.stk`\n",
		"equipment/equipment.lst":      "5000 `valued_coat.equ`\n5200 `no_value_title.equ`\n5100 `avatar_hat.equ`\n",
		"stackable/cube.stk":           "[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/colored.stk":        "[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/element.stk":        "[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/soul.stk":           "[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/booster.stk":        "[stackable type] `[booster]`\n[stack limit] 999\n",
		"stackable/result.stk":         "[stackable type] `[avatar emblem]`\n[grade] 2\n[stack limit] 999\n",
		"equipment/valued_coat.equ":    "[equipment type] `[coat]`\n[rarity] 2\n[minimum level] 10\n[value] 30000\n",
		"equipment/no_value_title.equ": "[equipment type] `[title name]`\n[rarity] 2\n[minimum level] 1\n",
		"equipment/avatar_hat.equ":     "[equipment type] `[hat avatar]`\n[usable job] `[fighter]`\n[grade] 2\n",
		"etc/disjoint.etc": "[cube index]\n`[no element]` 3037\n[/cube index]\n" +
			"[cube creation const]\n150 1.12 1.35 0.07 1.48 1.80 0.70 1.53\n[/cube creation const]\n" +
			"[additional result]\n0 4 3033 3034 3035 3036 1 3166 1 3167 0 1 3229 1 3332\n[/additional result]\n" +
			"[additional result const]\n0.00 0.30 8.70 8 5 50 50 8 10 0.19 5.65 8 20 0.00 1.30 5.50 6 15 0.19 5.65 8 20\n[/additional result const]\n" +
			"[additional result expand]\n1 10100115 1 10100116 1 10099773 1 10099773 1 10158124 0 1 10099775\n[/additional result expand]\n" +
			"[additional result expand const]\n25 0 100 25 0 100 20 0 100 20 0 100 25 0 100 0 16 0 100\n[/additional result expand const]\n" +
			"[avatar disjoint info]\n[fighter]\n2559000 2559003 2559006 2559009 2559006 2559001 2559004 2559007 2559010 2559007 2559002 2559005 2559008 2559011 2559014\n[/fighter]\n[/avatar disjoint info]\n",
		"etc/emblemcompound.etc":                  "[emblem compound info]\n1 2 9000\n[/emblem compound info]\n",
		"etc/avatardisjoint/emblemlistinfo_0.etc": "[result info]\n1 9000 100 1 0\n[/result info]\n",
		"etc/avatardisjoint/emblemlistinfo_1.etc": "[result info]\n1 9000 100 1 0\n[/result info]\n",
		"etc/avatardisjoint/emblemlistinfo_2.etc": "[min max value]\n50 69\n[result info]\n1 9001 100 1 0\n[/result info]\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := mustCurrentNPCShopRepositories(t, 0, slots)
	return repositories, catalog
}

func parseCurrentDisjointRewardRows(t *testing.T, body []byte) []currentDisjointRewardRow {
	t.Helper()
	const header = 1 + 2 + 1 + 2
	if len(body) < header+1 {
		t.Fatalf("op26 ack too short: %x", body)
	}
	count := int(body[header])
	if len(body) != header+1+count*10 {
		t.Fatalf("op26 ack rows=%d body=%x", count, body)
	}
	rows := make([]currentDisjointRewardRow, 0, count)
	for index := 0; index < count; index++ {
		offset := header + 1 + index*10
		rows = append(rows, currentDisjointRewardRow{
			Slot:    int16(binary.LittleEndian.Uint16(body[offset : offset+2])),
			ItemID:  binary.LittleEndian.Uint32(body[offset+2 : offset+6]),
			Granted: binary.LittleEndian.Uint32(body[offset+6 : offset+10]),
		})
	}
	return rows
}

// currentDisjointRewardRowForItem 按产物 id 取 ACK 行：产物行按槽位排序，位置不固定。
func currentDisjointRewardRowForItem(t *testing.T, rows []currentDisjointRewardRow, itemID uint32) currentDisjointRewardRow {
	t.Helper()
	for _, row := range rows {
		if row.ItemID == itemID {
			return row
		}
	}
	t.Fatalf("no ACK row for item %d in %+v", itemID, rows)
	return currentDisjointRewardRow{}
}

func seedCurrentDisjointAccount(t *testing.T, repositories dnfrepo.Group, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	if slots == nil {
		slots = map[string]dnfrepo.ItemStack{}
	}
	if err := repositories.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: "account-1",
		Slots:     slots,
	}); err != nil {
		t.Fatal(err)
	}
}

// (a) 正常分解：无色小晶块进账号晶块仓库 358，灵魂进灵魂仓库 362，
// [material] 元素结晶并入主背包材料段既有行（不占新格）。
func TestCurrentDisjointRewardsLandInRuntimePVFSegments(t *testing.T) {
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, map[string]dnfrepo.ItemStack{
		"0:65":  {ItemID: 5000, Count: 1},
		"0:121": {ItemID: 3166, Count: 5},
	})
	seedCurrentDisjointAccount(t, repositories, map[string]dnfrepo.ItemStack{
		"0:358": {ItemID: 3037, Count: 800},
	})
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(65, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("op26 ACK=%x header=%+v trailing=%d", ack.Body, ack.Header, len(trailing))
	}
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if len(rows) != 3 {
		t.Fatalf("rows=%+v want 3 (clear cube + element crystal + rare soul)", rows)
	}
	if cube := currentDisjointRewardRowForItem(t, rows, 3037); cube != (currentDisjointRewardRow{Slot: 358, ItemID: 3037, Granted: 14}) {
		t.Fatalf("cube row=%+v want item 3037 slot 358 granted 14", cube)
	}
	if element := currentDisjointRewardRowForItem(t, rows, 3166); element != (currentDisjointRewardRow{Slot: 121, ItemID: 3166, Granted: 1}) {
		t.Fatalf("element row=%+v want item 3166 merged into slot 121", element)
	}
	if soul := currentDisjointRewardRowForItem(t, rows, 10099773); soul != (currentDisjointRewardRow{Slot: 362, ItemID: 10099773, Granted: 1}) {
		t.Fatalf("soul row=%+v want item 10099773 slot 362", soul)
	}
	ctx := context.Background()
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	if _, exists := inventory.Slots["0:65"]; exists {
		t.Fatal("source equipment was not consumed")
	}
	if stack := inventory.Slots["0:121"]; stack.ItemID != 3166 || stack.Count != 6 {
		t.Fatalf("material slot 121=%+v want 3166x6 (merged, no extra slot)", stack)
	}
	for key, stack := range inventory.Slots {
		switch {
		case stack.ItemID == 3037 || stack.ItemID == 10099773:
			t.Fatalf("crystal/soul %d stayed in the character backpack at %s", stack.ItemID, key)
		case stack.ItemID == 3166 && key != "0:121":
			t.Fatalf("[material] reward split into a second slot %s=%+v", key, stack)
		}
	}
	account, found, err := repositories.AccountInventory.Load(ctx, "account-1")
	if err != nil || !found {
		t.Fatalf("account inventory found=%t err=%v", found, err)
	}
	if stack := account.Slots["0:358"]; stack.ItemID != 3037 || stack.Count != 814 {
		t.Fatalf("crystal warehouse 358=%+v want 3037x814", stack)
	}
	if stack := account.Slots["0:362"]; stack.ItemID != 10099773 || stack.Count != 1 {
		t.Fatalf("soul warehouse 362=%+v want 10099773x1", stack)
	}
}

// (a2) 无 [value]/[price] 的装备（运行时 PVF 的称号/纹章）必须仍可分解：86JP 用
// `Math.Max(1, SellGold ?? 1)` 兜底 → 至少 1 个无色小晶块。旧实现直接判"奖励映射
// 非法"，客户端把它显示成"物品栏空间不足，无法获得分解物品…"。
func TestCurrentDisjointWithoutSellValueStillYieldsOneCube(t *testing.T) {
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5200, Count: 1},
	})
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("no-value equipment must decompose, got ACK=%x trailing=%d", ack.Body, len(trailing))
	}
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if len(rows) != 3 {
		t.Fatalf("rows=%+v want 3", rows)
	}
	if cube := currentDisjointRewardRowForItem(t, rows, 3037); cube != (currentDisjointRewardRow{Slot: 358, ItemID: 3037, Granted: 1}) {
		t.Fatalf("cube row=%+v want item 3037 slot 358 granted 1", cube)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if _, exists := inventory.Slots["0:9"]; exists {
		t.Fatal("source was not consumed")
	}
}

// (b) 只有目标段真的占满（快捷栏 3..8 + 材料段 121..176 都满、且没有可并的既有
// 行）才回失败码 4；来源不得被消耗，晶块仓库也不得被写入（同事务回滚）。
func TestCurrentDisjointReportsInventoryFullOnlyWhenTargetSegmentIsFull(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{"0:65": {ItemID: 5000, Count: 1}}
	for slot := int16(3); slot <= 8; slot++ {
		slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: 1}
	}
	for slot := int16(121); slot <= 176; slot++ {
		slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: 1}
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(65, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || !bytes.Equal(failure.Body, []byte{0, 4}) {
		t.Fatalf("full material segment must report code 4, got %x trailing=%d", failure.Body, len(trailing))
	}
	ctx := context.Background()
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	if stack := inventory.Slots["0:65"]; stack.ItemID != 5000 || stack.Count != 1 {
		t.Fatalf("full-segment failure consumed the source: %+v", stack)
	}
	if stack := inventory.Slots["0:125"]; stack.ItemID != 3227 {
		t.Fatalf("full-segment failure granted %+v elsewhere", stack)
	}
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if len(account.Slots) != 0 {
		t.Fatalf("full-segment failure still wrote the crystal warehouse: %+v", account.Slots)
	}
}

// (b2) 空间判定按目标段"真实可用的槽位"算：材料段只剩 1 格也必须成功，不能要求
// 整段空闲。
func TestCurrentDisjointUsesRealFreeMaterialSlots(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{"0:65": {ItemID: 5000, Count: 1}}
	for slot := int16(3); slot <= 8; slot++ {
		slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: 1}
	}
	for slot := int16(121); slot <= 175; slot++ {
		slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: 1}
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(65, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("one free material slot must be enough, got %x trailing=%d", ack.Body, len(trailing))
	}
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if element := currentDisjointRewardRowForItem(t, rows, 3166); element != (currentDisjointRewardRow{Slot: 176, ItemID: 3166, Granted: 1}) {
		t.Fatalf("element row=%+v want the only free material slot 176", element)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if stack := inventory.Slots["0:176"]; stack.ItemID != 3166 || stack.Count != 1 {
		t.Fatalf("slot 176=%+v want 3166x1", stack)
	}
}

// (c) 批量分解（客户端逐件下发 op26）：每件来源只消耗一次，ACK 只上报本次获得量，
// 重复操作只并堆不另占格，账号仓库累计正确。
func TestCurrentDisjointBatchConsumesAndGrantsExactCounts(t *testing.T) {
	sources := []int16{60, 61, 62}
	slots := map[string]dnfrepo.ItemStack{}
	for _, slot := range sources {
		slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 5000, Count: 1}
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	// 材料族目标段（客户端文案点名的三处）：快捷栏 3..8、材料段 121..176、
	// 副职材料段 233..288。首次产出可以落在快捷栏空位（与掉落/点券发放同一条
	// grantCurrentCeraShopProduct -> PlacePickupItem 判据，不另造第二套）；后续
	// 操作必须并进同一行。
	elementSlot := int16(-1)
	for index, slot := range sources {
		if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(slot, 0, -1, 0)); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(rest) != 0 || len(ack.Body) == 0 || ack.Body[0] != 1 {
			t.Fatalf("operation %d ACK=%x", index, ack.Body)
		}
		rows := parseCurrentDisjointRewardRows(t, ack.Body)
		if len(rows) != 3 {
			t.Fatalf("operation %d rows=%+v want 3", index, rows)
		}
		if cube := currentDisjointRewardRowForItem(t, rows, 3037); cube != (currentDisjointRewardRow{Slot: 358, ItemID: 3037, Granted: 14}) {
			t.Fatalf("operation %d cube row=%+v want granted 14 (this operation only)", index, cube)
		}
		element := currentDisjointRewardRowForItem(t, rows, 3166)
		if element.Granted != 1 {
			t.Fatalf("operation %d element row=%+v want granted 1", index, element)
		}
		if elementSlot < 0 {
			elementSlot = element.Slot
		} else if element.Slot != elementSlot {
			t.Fatalf("operation %d element moved from slot %d to %d", index, elementSlot, element.Slot)
		}
		if soul := currentDisjointRewardRowForItem(t, rows, 10099773); soul != (currentDisjointRewardRow{Slot: 362, ItemID: 10099773, Granted: 1}) {
			t.Fatalf("operation %d soul row=%+v", index, soul)
		}
		connection.write.Reset()
	}
	if elementSlot < 3 || (elementSlot > 8 && (elementSlot < 121 || elementSlot > 176)) {
		t.Fatalf("element crystal landed outside the material family segments: slot %d", elementSlot)
	}
	ctx := context.Background()
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	for _, slot := range sources {
		if _, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, slot)]; exists {
			t.Fatalf("batch left source slot %d behind", slot)
		}
	}
	if stack := inventory.Slots[currentCeraShopInventorySlotKey(0, elementSlot)]; stack.ItemID != 3166 || stack.Count != 3 {
		t.Fatalf("material slot %d=%+v want 3166x3 from three operations", elementSlot, stack)
	}
	var materialSlots int
	for _, stack := range inventory.Slots {
		if stack.ItemID == 3166 {
			materialSlots++
		}
	}
	if materialSlots != 1 {
		t.Fatalf("three operations used %d material slots, want 1 merged row", materialSlots)
	}
	if len(inventory.Slots) != 1 {
		t.Fatalf("batch left unexpected character rows: %+v", inventory.Slots)
	}
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if stack := account.Slots["0:358"]; stack.ItemID != 3037 || stack.Count != 42 {
		t.Fatalf("crystal warehouse 358=%+v want 3037x42", stack)
	}
	if stack := account.Slots["0:362"]; stack.ItemID != 10099773 || stack.Count != 3 {
		t.Fatalf("soul warehouse 362=%+v want 10099773x3", stack)
	}
	if len(account.Slots) != 2 {
		t.Fatalf("batch touched unexpected warehouse slots: %+v", account.Slots)
	}
}

// (d) 真实运行时 PVF 端到端：没有 [value]/[price] 的称号必须回成功包，晶块落到
// 账号晶块仓库 358，其余 [material] 产物只出现在主背包材料段 121..176。
func TestRealPVFDisjointPlacesRewardsInRuntimeSegments(t *testing.T) {
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
	// 取证：400330100 是 `[equipment type] [title name]`，没有 [value]/[price]，
	// 也没有 [impossible content]；旧实现因此拒绝整次分解。
	const titleItemID = 400330100
	titleDefinition, err := catalog.ResolveItem(titleItemID)
	if err != nil {
		t.Fatal(err)
	}
	if titleDefinition.Kind != dungeonDropItemEquipment {
		t.Fatalf("runtime item %d kind=%s want equipment", titleItemID, titleDefinition.Kind)
	}
	document, err := parseDungeonCardPVFDocument(archive, titleDefinition.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := document.Int("value"); found {
		t.Fatalf("runtime fixture drifted: %s now has [value]", titleDefinition.PVFPath)
	}
	rewards, err := currentEquipmentDisjointRewards(config, document)
	if err != nil {
		t.Fatalf("runtime equipment without [value] must decompose: %v", err)
	}
	if len(rewards) == 0 || rewards[0].ItemID != 3037 || rewards[0].Count < 1 {
		t.Fatalf("rewards=%+v want at least one clear cube", rewards)
	}
	// 端到端：同一件称号放进真实 op26 请求，必须成功且落点正确。
	repositories := mustCurrentNPCShopRepositories(t, 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: titleItemID, Count: 1},
	})
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(9, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("runtime title without [value] ACK=%x trailing=%d", ack.Body, len(trailing))
	}
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if cube := currentDisjointRewardRowForItem(t, rows, 3037); cube.Slot != 358 || cube.Granted < 1 {
		t.Fatalf("cube row=%+v want crystal warehouse slot 358", cube)
	}
	ctx := context.Background()
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	for key, stack := range inventory.Slots {
		listType, slot, ok := parseSceneInventorySlotKey(key)
		if !ok || listType != dnfrepo.MainInventoryListType {
			t.Fatalf("runtime [material] reward %d landed on list %d at %s", stack.ItemID, listType, key)
		}
		// 材料族目标段：快捷栏 3..8 / 材料段 121..176 / 副职材料段 233..288。
		if (slot >= 3 && slot <= 8) || (slot >= 121 && slot <= 176) || (slot >= 233 && slot <= 288) {
			continue
		}
		t.Fatalf("runtime [material] reward %d landed outside the material family segments at %s", stack.ItemID, key)
	}
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if stack := account.Slots["0:358"]; stack.ItemID != 3037 || stack.Count != int64(currentDisjointRewardRowForItem(t, rows, 3037).Granted) {
		t.Fatalf("crystal warehouse 358=%+v want the ACK granted amount", stack)
	}
}
