package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 随机称号附魔（2017 劳动节「拨云见日宝珠」）的 0x77 行用例。真实身份全部按
// .lst 的 ID→引用值绑定（见 E:/90/repair-round28-20261017/gift-bead/REPORT.md
// §3/§4，PVF SHA256 AD7D05C70421278F7385729BCBA3E81E37D99D0F3DF85A87A9F0DC19C92504CB）：
//
//	宝珠 490700960 → stackable/cash/chn_490700960.stk
//	  [stackable type] `[auto enchant] 0`
//	  [random title enchant] 10001
//	  [string data] `[title name]`
//	  [bead limited usable item] 400330094 400330096 400330101
//	称号 400330094 → equipment/character/common/title/chn_2017Laborday_test_400330094.equ
//	  [equipment type] `[title name] 1`
//
// 本文件断言三件事，全部落在真实 PVF + 真实 inventory owner 上，不另造行为镜像：
//   - 持久化：owner 落库的 Extra/RawEntry/raw_entry_hex 形状，经 0x77 行投影后
//     +0x61 的 u16 config 与 +0x63 的 u16 row 同时正确；
//   - 拒绝：只写了半条（只有 config 或只有 row）或写了非法值的行，投影必须逐字节
//     等于「没有这两个键」的普通装备行——不得清掉 +0x0E..+0x12，也不得改写未知字节；
//   - 守恒：普通装备（含普通宝珠附魔）投影后 +0x61/+0x63 与 +0x0E..+0x12 与输入 raw
//     逐字节一致。
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -run RandomTitleEnchant ./internal/services/dnfbridge
const (
	currentRandomTitleEnchantRealBeadItemID  = int64(490700960)
	currentRandomTitleEnchantRealTitleItemID = int64(400330094)
	currentRandomTitleEnchantRealConfigID    = uint16(10001)
	// 称号按 [equipment type] `[title name]` 落在当前 EXE 的 33 槽表槽 13
	// （equipment_placement.go 的 currentEquipmentPlacementRuleForPVFType）。
	currentRandomTitleEnchantWornTitleActorSlot = int16(13)
)

// currentRealRandomTitleEnchantFixture 打开真实 Script.pvf，解析出宝珠→称号的
// 随机称号附魔结果。config 必须是 etc/randomtitleenchant.etc 里 10001，row 必须是该
// config 真实的行（row 由 PVF 权重表掷出，不落在物品模板里）。
func currentRealRandomTitleEnchantFixture(t *testing.T) (alignedcmd.EnchantBeadResolution, *pvfDungeonDropCatalog) {
	t.Helper()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolveCurrentEnchantBeadMetadata(
		catalog,
		catalog.source,
		currentRandomTitleEnchantRealBeadItemID,
		currentRandomTitleEnchantRealTitleItemID,
	)
	if err != nil {
		t.Fatalf("resolve real 2017 Labor-Day random-title bead: %v", err)
	}
	if resolution.RandomTitleEnchantConfigID != currentRandomTitleEnchantRealConfigID {
		t.Fatalf("random title config = %d, want %d (%+v)",
			resolution.RandomTitleEnchantConfigID, currentRandomTitleEnchantRealConfigID, resolution)
	}
	if resolution.RandomTitleEnchantRowID == 0 {
		t.Fatalf("random title row = 0, want a row of config %d", currentRandomTitleEnchantRealConfigID)
	}
	if resolution.CardItemID != 0 {
		t.Fatalf("card item = %d, want 0: a random-title bead carries no monster card", resolution.CardItemID)
	}
	whitelisted := false
	for _, id := range resolution.TargetWhitelist {
		if id == currentRandomTitleEnchantRealTitleItemID {
			whitelisted = true
		}
	}
	if !whitelisted {
		t.Fatalf("whitelist = %v, want title %d", resolution.TargetWhitelist, currentRandomTitleEnchantRealTitleItemID)
	}
	if !strings.EqualFold(strings.TrimSpace(resolution.TargetEquipmentType), "[title name]") {
		t.Fatalf("target equipment type = %q, want [title name]", resolution.TargetEquipmentType)
	}
	random, err := parseCurrentRandomTitleEnchantCatalog(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := random.Configs[currentRandomTitleEnchantRealConfigID]
	if !ok {
		t.Fatalf("real PVF has no etc/randomtitleenchant.etc config %d", currentRandomTitleEnchantRealConfigID)
	}
	rowFound := false
	for _, row := range config.Rows {
		if row.ID == resolution.RandomTitleEnchantRowID {
			rowFound = true
		}
	}
	if !rowFound {
		t.Fatalf("row %d is not a row of config %d", resolution.RandomTitleEnchantRowID, currentRandomTitleEnchantRealConfigID)
	}
	return resolution, catalog
}

// currentRandomTitleEnchantSentinelRow 造一条「已经存下来的 0x77 行」：字段区以外
// 逐字节填哨兵，用来证明半条/非法字段与普通装备都不会碰未知字节。
func currentRandomTitleEnchantSentinelRow(itemID int64, configID, rowID uint16) []byte {
	raw := make([]byte, currentItemListEntryWireSize)
	for offset := 0x0D; offset < len(raw); offset++ {
		raw[offset] = byte(0x41 + offset%0x1f)
	}
	binary.LittleEndian.PutUint16(raw[0x00:0x02], uint16(currentRandomTitleEnchantWornTitleActorSlot))
	binary.LittleEndian.PutUint32(raw[0x02:0x06], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[0x06:0x0A], 0x0badf00d)
	// 旧行上还可能留着普通宝珠附魔（卡 id + 升级数）：随机称号附魔落库时必须清掉。
	copy(raw[0x0E:0x12], []byte{0x11, 0x22, 0x33, 0x44})
	raw[0x12] = 0x05
	binary.LittleEndian.PutUint16(raw[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantRowOffset], configID)
	binary.LittleEndian.PutUint16(raw[currentRandomTitleEnchantRowOffset:currentRandomTitleEnchantFieldEnd], rowID)
	return raw
}

// currentRandomTitleEnchantBaseExtra 是普通称号装备行的 Extra：没有随机称号附魔键。
func currentRandomTitleEnchantBaseExtra() map[string]string {
	return map[string]string{
		"item_kind":      string(dungeonDropItemEquipment),
		"equipment_type": "[title name]",
	}
}

// currentRandomTitleEnchantPersistedExtra 复刻 inventory.setRandomTitleEnchantFields
// 落库的 Extra 键（internal/modules/dnf/inventory/owner.go）。
func currentRandomTitleEnchantPersistedExtra(configID, rowID uint16) map[string]string {
	extra := currentRandomTitleEnchantBaseExtra()
	extra["value_a"] = "0"
	extra["enchant_card_id"] = "0"
	extra["byte_12"] = "0"
	extra["enchant_upgrade_count"] = "0"
	extra["random_title_enchant_config_id"] = strconv.FormatUint(uint64(configID), 10)
	extra["random_title_enchant_row_id"] = strconv.FormatUint(uint64(rowID), 10)
	return extra
}

func currentRandomTitleEnchantExtraWith(configID, rowID string) map[string]string {
	extra := currentRandomTitleEnchantBaseExtra()
	if configID != "" {
		extra["random_title_enchant_config_id"] = configID
	}
	if rowID != "" {
		extra["random_title_enchant_row_id"] = rowID
	}
	return extra
}

func currentRandomTitleEnchantStackEntry(raw []byte, extra map[string]string, itemID int64, slot int16) currentItemListEntry {
	stack := dnfrepo.ItemStack{
		ItemID:   itemID,
		Count:    1,
		RawEntry: append([]byte(nil), raw...),
		Extra:    extra,
	}
	return currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
}

func currentRandomTitleEnchantEquipmentEntry(t *testing.T, raw []byte, extra map[string]string, itemID int64) currentItemListEntry {
	t.Helper()
	equipped := dnfrepo.EquipmentEntry{
		SlotIndex: currentRandomTitleEnchantWornTitleActorSlot,
		ItemID:    itemID,
		RawEntry:  append([]byte(nil), raw...),
		Extra:     extra,
	}
	entry, ok := currentItemListEntryFromEquipment(equipped)
	if !ok {
		t.Fatalf("worn title row (item %d) was not projected", itemID)
	}
	return entry
}

// currentRandomTitleEnchantOverlayOffsets 是 overlay 唯一允许改动的字节：
// 普通宝珠附魔段（+0x0E..+0x12）与随机称号附魔字段（+0x61..+0x64）。
func currentRandomTitleEnchantOverlayOffsets() map[int]bool {
	offsets := make(map[int]bool, 9)
	offsets[0x0E] = true
	offsets[0x0F] = true
	offsets[0x10] = true
	offsets[0x11] = true
	offsets[0x12] = true
	for offset := currentRandomTitleEnchantConfigOffset; offset < currentRandomTitleEnchantFieldEnd; offset++ {
		offsets[offset] = true
	}
	return offsets
}

func assertCurrentRandomTitleEnchantWireFields(t *testing.T, label string, entry currentItemListEntry, configID, rowID uint16) {
	t.Helper()
	if got := binary.LittleEndian.Uint16(entry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantRowOffset]); got != configID {
		t.Fatalf("%s wire+0x61 config = %d, want %d", label, got, configID)
	}
	if got := binary.LittleEndian.Uint16(entry.data[currentRandomTitleEnchantRowOffset:currentRandomTitleEnchantFieldEnd]); got != rowID {
		t.Fatalf("%s wire+0x63 row = %d, want %d", label, got, rowID)
	}
}

func assertCurrentRandomTitleEnchantCardFieldsCleared(t *testing.T, label string, entry currentItemListEntry) {
	t.Helper()
	for offset := 0x0E; offset < 0x13; offset++ {
		if entry.data[offset] != 0 {
			t.Fatalf("%s wire+0x%02X = 0x%02X, want the ordinary monster-card enchant cleared", label, offset, entry.data[offset])
		}
	}
}

// assertCurrentRandomTitleEnchantOnlyOverlayBytesChanged 证明 overlay 只动了
// +0x0E..+0x12 与 +0x61..+0x64，行内其余字节与「没有这两个键」的普通装备行逐字节一致。
func assertCurrentRandomTitleEnchantOnlyOverlayBytesChanged(t *testing.T, label string, got, baseline currentItemListEntry) {
	t.Helper()
	allowed := currentRandomTitleEnchantOverlayOffsets()
	for offset := 0; offset < currentItemListEntryWireSize; offset++ {
		if got.data[offset] == baseline.data[offset] || allowed[offset] {
			continue
		}
		t.Fatalf("%s wire+0x%02X = 0x%02X, want 0x%02X from the ordinary projection",
			label, offset, got.data[offset], baseline.data[offset])
	}
}

// TestRealPVFRandomTitleEnchantPersistedRowProjectsBothContainers 走真实链路：
// 真 PVF 解析 → 真 owner 落库 → 落库后的 0x77 行在「背包 stack」与「穿戴 equipment」
// 两条投影上给出同一个 +0x61 u16 config / +0x63 u16 row。
func TestRealPVFRandomTitleEnchantPersistedRowProjectsBothContainers(t *testing.T) {
	resolution, catalog := currentRealRandomTitleEnchantFixture(t)
	// config 是宝珠模板里写死的（[random title enchant] 10001）；row 由每次解析现掷，
	// 所以下面只固定 config，row 断言 owner 自己那一次掷出的值。
	configID := resolution.RandomTitleEnchantConfigID

	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	const characterID = "77"
	const beadSlot = int16(12)
	const targetSlot = int16(30)
	const staleConfig = uint16(0x0bad)
	const staleRow = uint16(0x0bee)
	targetRaw := currentRandomTitleEnchantSentinelRow(currentRandomTitleEnchantRealTitleItemID, staleConfig, staleRow)
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots: map[string]dnfrepo.ItemStack{
			fmt.Sprintf("%d:%d", dnfrepo.MainInventoryListType, beadSlot): {
				ItemID: currentRandomTitleEnchantRealBeadItemID,
				Count:  2,
				Extra:  map[string]string{"item_kind": string(dungeonDropItemStackable)},
			},
			fmt.Sprintf("%d:%d", dnfrepo.MainInventoryListType, targetSlot): {
				ItemID:   currentRandomTitleEnchantRealTitleItemID,
				Count:    1,
				RawEntry: targetRaw,
				Extra:    currentRandomTitleEnchantBaseExtra(),
			},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(beadItemID int64, targetItemID int64) (alignedcmd.EnchantBeadResolution, error) {
		return resolveCurrentEnchantBeadMetadata(catalog, catalog.source, beadItemID, targetItemID)
	}
	result, err := owner.Enchant(ctx, dnfinventory.NewEnchantCommand(
		alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77},
		dnfinventory.EnchantByBeadRequest{
			BeadListType:    dnfrepo.MainInventoryListType,
			BeadSlotIndex:   beadSlot,
			TargetListType:  dnfrepo.MainInventoryListType,
			TargetSlotIndex: targetSlot,
		},
	), resolver)
	if err != nil {
		t.Fatalf("Enchant error = %v", err)
	}
	if !result.Success || !result.Changed || result.ErrorCode != 0 {
		t.Fatalf("result = %+v, want a committed random-title enchant", result)
	}
	if result.CardItemID != 0 {
		t.Fatalf("result card = %d, want 0 for a random-title enchant", result.CardItemID)
	}
	if result.BeadRemainingCount != 1 {
		t.Fatalf("bead remaining = %d, want 1", result.BeadRemainingCount)
	}

	loaded, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if got := loaded.Slots[fmt.Sprintf("%d:%d", dnfrepo.MainInventoryListType, beadSlot)].Count; got != 1 {
		t.Fatalf("bead count = %d, want 1", got)
	}
	target := loaded.Slots[fmt.Sprintf("%d:%d", dnfrepo.MainInventoryListType, targetSlot)]
	if target.ItemID != currentRandomTitleEnchantRealTitleItemID || target.Count != 1 {
		t.Fatalf("persisted target = item %d count %d", target.ItemID, target.Count)
	}

	// 持久化：Extra 双写 + RawEntry 现场 + raw_entry_hex 镜像。
	// 注意 row 是 resolver 每次调用现掷的（etc/randomtitleenchant.etc 的权重表），
	// 所以这一条要断言 owner 自己那次掷出的值，而不是本用例先前解析出来的那一次。
	persistedConfigValue, configErr := strconv.ParseUint(strings.TrimSpace(target.Extra["random_title_enchant_config_id"]), 10, 16)
	persistedRowValue, rowErr := strconv.ParseUint(strings.TrimSpace(target.Extra["random_title_enchant_row_id"]), 10, 16)
	if configErr != nil || rowErr != nil {
		t.Fatalf("persisted config/row = %q/%q, want decimal uint16 values",
			target.Extra["random_title_enchant_config_id"], target.Extra["random_title_enchant_row_id"])
	}
	persistedConfig := uint16(persistedConfigValue)
	persistedRow := uint16(persistedRowValue)
	if persistedConfig != configID {
		t.Fatalf("persisted config = %q, want %d", target.Extra["random_title_enchant_config_id"], configID)
	}
	if persistedRow == 0 {
		t.Fatal("persisted row = 0, want a drawn row of the real config")
	}
	randomCatalog, err := parseCurrentRandomTitleEnchantCatalog(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := randomCatalog.Configs[configID]
	if !ok {
		t.Fatalf("real PVF has no random-title config %d", configID)
	}
	drawnRow := false
	for _, row := range config.Rows {
		if row.ID == persistedRow && row.Weight > 0 {
			drawnRow = true
		}
	}
	if !drawnRow {
		t.Fatalf("persisted row %d is not a positive-weight row of config %d", persistedRow, configID)
	}
	for _, key := range []string{"value_a", "enchant_card_id", "byte_12", "enchant_upgrade_count"} {
		if got := target.Extra[key]; got != "0" {
			t.Fatalf("persisted %s = %q, want 0: a random-title enchant carries no monster card", key, got)
		}
	}
	if len(target.RawEntry) != currentItemListEntryWireSize {
		t.Fatalf("persisted RawEntry size = %d, want %d", len(target.RawEntry), currentItemListEntryWireSize)
	}
	for offset := 0x0E; offset < 0x13; offset++ {
		if target.RawEntry[offset] != 0 {
			t.Fatalf("persisted raw+0x%02X = 0x%02X, want 0", offset, target.RawEntry[offset])
		}
	}
	if got := binary.LittleEndian.Uint16(target.RawEntry[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantRowOffset]); got != persistedConfig {
		t.Fatalf("persisted raw+0x61 = %d, want %d", got, persistedConfig)
	}
	if got := binary.LittleEndian.Uint16(target.RawEntry[currentRandomTitleEnchantRowOffset:currentRandomTitleEnchantFieldEnd]); got != persistedRow {
		t.Fatalf("persisted raw+0x63 = %d, want %d", got, persistedRow)
	}
	if got := target.Extra["raw_entry_hex"]; got != hex.EncodeToString(target.RawEntry) {
		t.Fatalf("raw_entry_hex = %q, want the persisted RawEntry mirror", got)
	}
	// 字段之外的未知字节必须原样留着（+0x5F / +0x65 既不在普通附魔段也不在字段里）。
	for _, offset := range []int{0x5F, 0x65, 0x66} {
		if target.RawEntry[offset] != targetRaw[offset] {
			t.Fatalf("persistence rewrote unknown raw+0x%02X = 0x%02X, want 0x%02X",
				offset, target.RawEntry[offset], targetRaw[offset])
		}
	}

	// 投影：落库行与「旧行（没有这两个键）」逐字节相比，只允许 overlay 的那九个字节不同。
	baselineExtra := currentRandomTitleEnchantBaseExtra()
	baselineStack := currentRandomTitleEnchantStackEntry(targetRaw, baselineExtra, currentRandomTitleEnchantRealTitleItemID, targetSlot)
	baselineEquipment := currentRandomTitleEnchantEquipmentEntry(t, targetRaw, baselineExtra, currentRandomTitleEnchantRealTitleItemID)
	stackEntry := currentRandomTitleEnchantStackEntry(target.RawEntry, target.Extra, currentRandomTitleEnchantRealTitleItemID, targetSlot)
	equipmentEntry := currentRandomTitleEnchantEquipmentEntry(t, target.RawEntry, target.Extra, currentRandomTitleEnchantRealTitleItemID)
	assertCurrentRandomTitleEnchantOnlyOverlayBytesChanged(t, "stack", stackEntry, baselineStack)
	assertCurrentRandomTitleEnchantOnlyOverlayBytesChanged(t, "equipment", equipmentEntry, baselineEquipment)
	for _, projected := range []struct {
		label string
		entry currentItemListEntry
	}{
		{label: "stack", entry: stackEntry},
		{label: "equipment", entry: equipmentEntry},
	} {
		assertCurrentRandomTitleEnchantWireFields(t, projected.label, projected.entry, persistedConfig, persistedRow)
		assertCurrentRandomTitleEnchantCardFieldsCleared(t, projected.label, projected.entry)
	}
	if !bytes.Equal(
		stackEntry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd],
		equipmentEntry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd],
	) {
		t.Fatalf("stack +0x61..+0x64 = %x, equipment = %x: both containers must agree",
			stackEntry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd],
			equipmentEntry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd])
	}

	// 穿戴称号的 actor 槽映射：PVF 类型 → 槽 13，两条身份判据也要给 13。
	rule, ok := currentEquipmentPlacementRuleForPVFType("[title name]")
	if !ok || rule.targetSlot != currentRandomTitleEnchantWornTitleActorSlot {
		t.Fatalf("placement rule for [title name] = %+v ok=%t, want slot %d", rule, ok, currentRandomTitleEnchantWornTitleActorSlot)
	}
	if got := binary.LittleEndian.Uint16(equipmentEntry.data[0x00:0x02]); got != uint16(currentRandomTitleEnchantWornTitleActorSlot) {
		t.Fatalf("equipment row slot = %d, want the title actor slot %d", got, currentRandomTitleEnchantWornTitleActorSlot)
	}
	runtimeMoved := dnfrepo.EquipmentEntry{
		SlotIndex: currentRandomTitleEnchantWornTitleActorSlot,
		ItemID:    currentRandomTitleEnchantRealTitleItemID,
		RawEntry:  append([]byte(nil), targetRaw...),
		Extra:     currentRandomTitleEnchantBaseExtra(),
	}
	if slot, ok := currentEXEActorEquipmentSlot(runtimeMoved); !ok || slot != uint64(currentRandomTitleEnchantWornTitleActorSlot) {
		t.Fatalf("currentEXEActorEquipmentSlot(worn title) = (%d, %t), want (%d, true)",
			slot, ok, currentRandomTitleEnchantWornTitleActorSlot)
	}
}

// TestRealPVFRandomTitleEnchantRowProjectionSources 覆盖 0x77 行的两种来源：
// 「服务端自造的全零底座（Extra-only）」与「已存下来整行 raw」。两种来源都必须给出
// 同一个 +0x61/+0x63，且 stack 与 equipment 两条投影一致。
func TestRealPVFRandomTitleEnchantRowProjectionSources(t *testing.T) {
	resolution, _ := currentRealRandomTitleEnchantFixture(t)
	configID := resolution.RandomTitleEnchantConfigID
	rowID := resolution.RandomTitleEnchantRowID
	const slot = int16(30)
	itemID := currentRandomTitleEnchantRealTitleItemID

	// appliesOverlay=false 是「整行 raw 带着字段、Extra 镜像丢了」的读路径：没有完整的
	// config/row 对时 overlay 不动作，字段只能来自 raw，普通附魔段也必须原样保留。
	check := func(t *testing.T, label string, raw []byte, extra map[string]string, appliesOverlay bool) {
		t.Helper()
		baseline := currentRandomTitleEnchantStackEntry(raw, currentRandomTitleEnchantBaseExtra(), itemID, slot)
		stack := currentRandomTitleEnchantStackEntry(raw, extra, itemID, slot)
		if appliesOverlay {
			assertCurrentRandomTitleEnchantOnlyOverlayBytesChanged(t, label+" stack", stack, baseline)
			assertCurrentRandomTitleEnchantCardFieldsCleared(t, label+" stack", stack)
		} else if stack.data != baseline.data {
			t.Fatalf("%s stack row = %x, want the ordinary projection %x", label, stack.data, baseline.data)
		}
		assertCurrentRandomTitleEnchantWireFields(t, label+" stack", stack, configID, rowID)

		baselineEquipment := currentRandomTitleEnchantEquipmentEntry(t, raw, currentRandomTitleEnchantBaseExtra(), itemID)
		equipment := currentRandomTitleEnchantEquipmentEntry(t, raw, extra, itemID)
		if appliesOverlay {
			assertCurrentRandomTitleEnchantOnlyOverlayBytesChanged(t, label+" equipment", equipment, baselineEquipment)
			assertCurrentRandomTitleEnchantCardFieldsCleared(t, label+" equipment", equipment)
		} else if equipment.data != baselineEquipment.data {
			t.Fatalf("%s equipment row = %x, want the ordinary projection %x", label, equipment.data, baselineEquipment.data)
		}
		assertCurrentRandomTitleEnchantWireFields(t, label+" equipment", equipment, configID, rowID)

		if !bytes.Equal(
			stack.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd],
			equipment.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd],
		) {
			t.Fatalf("%s: stack +0x61..+0x64 = %x, equipment = %x", label,
				stack.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd],
				equipment.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantFieldEnd])
		}
	}

	t.Run("extra only synthesis", func(t *testing.T) {
		// 2026-09-27 [upgrade limit cube] 称号变换的形状：owner 换模板时清掉 RawEntry，
		// Extra 里也没有 raw_entry_hex —— 底座是全零，附魔只能由 Extra 盖回来。
		check(t, "extra only", nil, currentRandomTitleEnchantPersistedExtra(configID, rowID), true)
	})
	t.Run("stored 0x77 row with extra mirror", func(t *testing.T) {
		raw := currentRandomTitleEnchantSentinelRow(itemID, configID, rowID)
		extra := currentRandomTitleEnchantPersistedExtra(configID, rowID)
		extra["raw_entry_hex"] = hex.EncodeToString(raw)
		check(t, "stored row", raw, extra, true)
	})
	t.Run("stored 0x77 row without extra mirror", func(t *testing.T) {
		// Extra 镜像丢失时，字段必须还能从整行 raw 读出来。
		raw := currentRandomTitleEnchantSentinelRow(itemID, configID, rowID)
		check(t, "stored row without mirror", raw, currentRandomTitleEnchantBaseExtra(), false)
	})
}

// TestRealPVFRandomTitleEnchantHalfOrIllegalFieldsLeaveBytesUntouched 是拒绝面：
// 只有 config 或只有 row、零值、非数字、超出 u16 的值，都必须让整条行逐字节等于
// 「没有这两个键」的普通装备行——既不清 +0x0E..+0x12，也不改任何未知字节。
func TestRealPVFRandomTitleEnchantHalfOrIllegalFieldsLeaveBytesUntouched(t *testing.T) {
	resolution, _ := currentRealRandomTitleEnchantFixture(t)
	configID := resolution.RandomTitleEnchantConfigID
	rowID := resolution.RandomTitleEnchantRowID
	const slot = int16(30)
	itemID := currentRandomTitleEnchantRealTitleItemID
	raw := currentRandomTitleEnchantSentinelRow(itemID, configID, rowID)

	validConfig := strconv.FormatUint(uint64(configID), 10)
	validRow := strconv.FormatUint(uint64(rowID), 10)
	baselineExtra := currentRandomTitleEnchantBaseExtra()
	baselineStack := currentRandomTitleEnchantStackEntry(raw, baselineExtra, itemID, slot)
	baselineEquipment := currentRandomTitleEnchantEquipmentEntry(t, raw, baselineExtra, itemID)

	// 普通装备行本身必须带旧附魔段，否则这条用例证明不了「不清掉」。
	for offset := 0x0E; offset < 0x13; offset++ {
		if baselineStack.data[offset] != raw[offset] {
			t.Fatalf("baseline stack wire+0x%02X = 0x%02X, want the stored raw value 0x%02X",
				offset, baselineStack.data[offset], raw[offset])
		}
	}

	cases := []struct {
		name   string
		config string
		row    string
	}{
		{name: "config only", config: validConfig},
		{name: "row only", row: validRow},
		{name: "config zero", config: "0", row: validRow},
		{name: "row zero", config: validConfig, row: "0"},
		{name: "config non numeric", config: "cfg", row: validRow},
		{name: "row non numeric", config: validConfig, row: "1row"},
		{name: "config over uint16", config: "65536", row: validRow},
		{name: "config hex over uint16", config: "0x10000", row: validRow},
		{name: "config negative", config: "-1", row: validRow},
		{name: "config blank", config: " ", row: validRow},
		{name: "row blank", config: validConfig, row: " "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			extra := currentRandomTitleEnchantExtraWith(tc.config, tc.row)
			stack := currentRandomTitleEnchantStackEntry(raw, extra, itemID, slot)
			if stack.data != baselineStack.data {
				t.Fatalf("stack row changed for a half/illegal pair: got %x want %x", stack.data, baselineStack.data)
			}
			equipment := currentRandomTitleEnchantEquipmentEntry(t, raw, extra, itemID)
			if equipment.data != baselineEquipment.data {
				t.Fatalf("equipment row changed for a half/illegal pair: got %x want %x", equipment.data, baselineEquipment.data)
			}
			assertCurrentRandomTitleEnchantWireFields(t, "half/illegal stack", stack, configID, rowID)
			for offset := 0x0E; offset < 0x13; offset++ {
				if stack.data[offset] != raw[offset] {
					t.Fatalf("half/illegal pair cleared wire+0x%02X = 0x%02X, want the stored 0x%02X",
						offset, stack.data[offset], raw[offset])
				}
			}
		})
	}
}

// TestRealPVFRandomTitleEnchantOrdinaryEquipmentBytesUnchanged 是守恒面：
// 普通装备（Extra 里没有随机称号附魔键）投影后，附魔段与 +0x61..+0x65 必须逐字节
// 等于输入 raw；带普通宝珠附魔的装备只改附魔段，+0x61..+0x65 仍然原样。
func TestRealPVFRandomTitleEnchantOrdinaryEquipmentBytesUnchanged(t *testing.T) {
	_, _ = currentRealRandomTitleEnchantFixture(t)
	const slot = int16(30)
	itemID := currentRandomTitleEnchantRealTitleItemID
	raw := currentRandomTitleEnchantSentinelRow(itemID, 0x1234, 0x5678)

	for _, projected := range []struct {
		label string
		entry currentItemListEntry
	}{
		{label: "stack", entry: currentRandomTitleEnchantStackEntry(raw, currentRandomTitleEnchantBaseExtra(), itemID, slot)},
		{label: "equipment", entry: currentRandomTitleEnchantEquipmentEntry(t, raw, currentRandomTitleEnchantBaseExtra(), itemID)},
	} {
		if got := projected.entry.data[0x0E:0x14]; !bytes.Equal(got, raw[0x0E:0x14]) {
			t.Fatalf("ordinary %s +0x0E..+0x13 = %x, want the raw bytes %x", projected.label, got, raw[0x0E:0x14])
		}
		if got := binary.LittleEndian.Uint16(projected.entry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantRowOffset]); got != 0x1234 {
			t.Fatalf("ordinary %s wire+0x61 = %d, want the stored raw value 0x1234", projected.label, got)
		}
		if got := binary.LittleEndian.Uint16(projected.entry.data[currentRandomTitleEnchantRowOffset:currentRandomTitleEnchantFieldEnd]); got != 0x5678 {
			t.Fatalf("ordinary %s wire+0x63 = %d, want the stored raw value 0x5678", projected.label, got)
		}
	}

	t.Run("ordinary card enchant keeps the stored title field", func(t *testing.T) {
		extra := currentRandomTitleEnchantBaseExtra()
		extra["value_a"] = "7001"
		extra["byte_12"] = "2"
		stack := currentRandomTitleEnchantStackEntry(raw, extra, itemID, slot)
		if got := binary.LittleEndian.Uint32(stack.data[0x0E:0x12]); got != 7001 {
			t.Fatalf("ordinary stack wire+0x0E card = %d, want 7001", got)
		}
		if stack.data[0x12] != 2 {
			t.Fatalf("ordinary stack wire+0x12 upgrade = %d, want 2", stack.data[0x12])
		}
		equipment := currentRandomTitleEnchantEquipmentEntry(t, raw, extra, itemID)
		if got := binary.LittleEndian.Uint32(equipment.data[0x0E:0x12]); got != 7001 {
			t.Fatalf("ordinary equipment wire+0x0E card = %d, want 7001", got)
		}
		if equipment.data[0x12] != 2 {
			t.Fatalf("ordinary equipment wire+0x12 upgrade = %d, want 2", equipment.data[0x12])
		}
		for _, entry := range []currentItemListEntry{stack, equipment} {
			if got := binary.LittleEndian.Uint16(entry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantRowOffset]); got != 0x1234 {
				t.Fatalf("ordinary card enchant rewrote wire+0x61 = %d, want the stored 0x1234", got)
			}
			if got := binary.LittleEndian.Uint16(entry.data[currentRandomTitleEnchantRowOffset:currentRandomTitleEnchantFieldEnd]); got != 0x5678 {
				t.Fatalf("ordinary card enchant rewrote wire+0x63 = %d, want the stored 0x5678", got)
			}
		}
	})
}

// TestRealPVFRandomTitleEnchantWornSlotRejectsTitleFieldOnOrdinaryRow 钉住
// 「+0x61/+0x63 只在完整的随机称号附魔对上才出现」：普通装备给一个合法 config 但
// 没有 row（或反过来）时，投影不得凭空造出 +0x61/+0x63，也不得覆盖普通附魔段。
func TestRealPVFRandomTitleEnchantWornSlotRejectsTitleFieldOnOrdinaryRow(t *testing.T) {
	resolution, _ := currentRealRandomTitleEnchantFixture(t)
	const slot = int16(30)
	itemID := currentRandomTitleEnchantRealTitleItemID
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0x00:0x02], uint16(currentRandomTitleEnchantWornTitleActorSlot))
	binary.LittleEndian.PutUint32(raw[0x02:0x06], uint32(itemID))
	// 普通附魔：卡 id 7001 + 升级 2。
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], 7001)
	raw[0x12] = 2

	half := currentRandomTitleEnchantExtraWith(strconv.FormatUint(uint64(resolution.RandomTitleEnchantConfigID), 10), "")
	stack := currentRandomTitleEnchantStackEntry(raw, half, itemID, slot)
	if got := binary.LittleEndian.Uint16(stack.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantRowOffset]); got != 0 {
		t.Fatalf("half pair synthesized wire+0x61 = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint16(stack.data[currentRandomTitleEnchantRowOffset:currentRandomTitleEnchantFieldEnd]); got != 0 {
		t.Fatalf("half pair synthesized wire+0x63 = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint32(stack.data[0x0E:0x12]); got != 7001 {
		t.Fatalf("half pair overwrote the ordinary card = %d, want 7001", got)
	}
}
