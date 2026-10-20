// 本文件是 O-B（只写脏 key）的等价性与回退证据。
//
// 判据不是"SQL 看起来对"，而是**落库之后库里的行逐行相同**：
// 同一份初值分别用 DNFBRIDGE_INVENTORY_SAVE_SCOPE / DNFBRIDGE_EQUIPMENT_SAVE_SCOPE 的
// collection 档与 slot/entry 档跑同一条 op19，比对六张表的最终行集合。
package mysql

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	"longheng.io/server/internal/modules/dnf/repository"
)

const (
	rowModelCharacterID   = "77"
	rowModelBagSlotFirst  = int16(9)
	rowModelBagSlotLast   = int16(28)
	rowModelWornSlotFirst = int16(11)
	rowModelWornSlotLast  = int16(20)
	rowModelAvatarItemID  = int64(300001)
	rowModelCargoItemID   = int64(400001)
)

func rowModelBagItemID(slot int16) int64  { return 100000 + int64(slot) }
func rowModelWornItemID(slot int16) int64 { return 200000 + int64(slot) }

func rowModelBagKey(slot int16) string { return fmt.Sprintf("0:%d", slot) }

func rowModelItemStack(itemID int64) repository.ItemStack {
	return repository.ItemStack{
		ItemID:   itemID,
		Count:    1,
		Bind:     true,
		RawEntry: rowModelRawEntry(byte(itemID)),
		Extra: map[string]string{
			"pvf_path":                    "equipment/weapon/sword.equ",
			"item_kind":                   "equipment",
			"current_exe_inventory_count": "1",
			"raw_entry_hex":               strings.ToUpper(hex.EncodeToString(rowModelRawEntry(byte(itemID)))),
		},
	}
}

func rowModelEquipmentEntry(slot int16) repository.EquipmentEntry {
	itemID := rowModelWornItemID(slot)
	return repository.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		Bind:      true,
		RawEntry:  rowModelRawEntry(byte(itemID)),
		Extra: map[string]string{
			"pvf_path":                 "equipment/weapon/sword.equ",
			"current_exe_runtime_move": "1",
			"equipped_slot":            strconv.Itoa(int(slot)),
		},
	}
}

func rowModelFixtureInventory(characterID string) repository.InventoryRecord {
	slots := make(map[string]repository.ItemStack)
	for slot := rowModelBagSlotFirst; slot <= rowModelBagSlotLast; slot++ {
		slots[rowModelBagKey(slot)] = rowModelItemStack(rowModelBagItemID(slot))
	}
	slots["1:0"] = rowModelItemStack(rowModelAvatarItemID)
	warehouse := map[string]repository.ItemStack{"2:0": rowModelItemStack(rowModelCargoItemID)}
	return repository.InventoryRecord{CharacterID: characterID, Slots: slots, Warehouse: warehouse}
}

func rowModelFixtureEquipment(characterID string) repository.EquipmentRecord {
	entries := make(map[string]repository.EquipmentEntry)
	for slot := rowModelWornSlotFirst; slot <= rowModelWornSlotLast; slot++ {
		entries[strconv.Itoa(int(slot))] = rowModelEquipmentEntry(slot)
	}
	return repository.EquipmentRecord{CharacterID: characterID, Entries: entries}
}

type writeScopeRun struct {
	state      map[string][]string
	writes     []fakeSQLCall
	totalCalls int
	mode       string
	changed    bool
}

// runRowModelEquipMove 在行模型上跑一条真实的 equip.Owner.Move：
// 先用"整容器写"灌初值（= 线上库里的样子），清空调用记录，再只统计被测那一次。
func runRowModelEquipMove(
	t *testing.T,
	cmd dnfequip.MoveCommand,
	inventoryScope, equipmentScope string,
) writeScopeRun {
	t.Helper()
	t.Setenv("DNFBRIDGE_INVENTORY_SAVE_SCOPE", inventoryScope)
	t.Setenv("DNFBRIDGE_EQUIPMENT_SAVE_SCOPE", equipmentScope)

	ctx := context.Background()
	db := newRowModelDB()
	group := newRowModelGroup(t, db, true)
	if err := repository.SaveInventoryFields(
		ctx, group.Inventory, rowModelFixtureInventory(rowModelCharacterID),
		repository.InventoryFieldSlots, repository.InventoryFieldWarehouse,
	); err != nil {
		t.Fatalf("seed inventory error = %v", err)
	}
	if err := repository.SaveEquipmentFields(
		ctx, group.Equipment, rowModelFixtureEquipment(rowModelCharacterID),
		repository.EquipmentFieldEntries,
	); err != nil {
		t.Fatalf("seed equipment error = %v", err)
	}

	owner, err := dnfequip.NewOwnerWithPlacementValidator(group, dnfequip.PlacementValidatorFunc(
		func(context.Context, dnfequip.Placement) error { return nil },
	))
	if err != nil {
		t.Fatalf("NewOwnerWithPlacementValidator() error = %v", err)
	}

	db.calls = nil
	result, err := owner.Move(ctx, cmd)
	if err != nil {
		t.Fatalf("Move(%+v) error = %v", cmd, err)
	}
	writes := make([]fakeSQLCall, 0, len(db.calls))
	for _, call := range db.calls {
		if strings.HasPrefix(call.Query, "INSERT INTO ") || strings.HasPrefix(call.Query, "DELETE FROM ") {
			writes = append(writes, call)
		}
	}
	return writeScopeRun{
		state:      db.model.snapshot(),
		writes:     writes,
		totalCalls: len(db.calls),
		mode:       result.Mode,
		changed:    result.Changed,
	}
}

func rowModelMoveCases() []struct {
	name         string
	cmd          dnfequip.MoveCommand
	mode         string
	inventoryKey []string
	equipmentKey []string
} {
	bagCmd := func(srcSlot, dstSlot int16) dnfequip.MoveCommand {
		return dnfequip.MoveCommand{
			SelectedCharacterID: 77,
			SourceListType:      0, SourceSlotIndex: srcSlot, SourceInstanceValue: int32(rowModelBagItemID(srcSlot)),
			MoveCount:           1,
			DestinationListType: 3, DestinationSlotIndex: dstSlot, DestinationInstanceValue: 0,
		}
	}
	wornCmd := func(srcSlot int16, dstList byte, dstSlot int16, dstInstance int32) dnfequip.MoveCommand {
		return dnfequip.MoveCommand{
			SelectedCharacterID: 77,
			SourceListType:      3, SourceSlotIndex: srcSlot, SourceInstanceValue: int32(rowModelWornItemID(srcSlot)),
			MoveCount:           1,
			DestinationListType: dstList, DestinationSlotIndex: dstSlot, DestinationInstanceValue: dstInstance,
		}
	}
	return []struct {
		name         string
		cmd          dnfequip.MoveCommand
		mode         string
		inventoryKey []string
		equipmentKey []string
	}{
		// 1) 穿上：背包 0:20 -> 空着的穿戴槽 22。
		{"equip_bag_to_empty_worn_slot", bagCmd(20, 22), "equip", []string{rowModelBagKey(20)}, []string{"22"}},
		// 2) 脱下：穿戴槽 12 -> 空着的背包 0:30。
		{"unequip_worn_to_empty_bag_slot", wornCmd(12, 0, 30, 0), "unequip", []string{rowModelBagKey(30)}, []string{"12"}},
		// 3) 脱下且请求的落点被占：0:20 有东西 ⇒ nextEmptyUnequipInventorySlot 改落到 0:29，
		//    因此这一格是 Mode=unequip（不是 swap），delta 必须是**改后的落点** 0:29。
		//    这是 O-B 最容易写错的一格。
		{"unequip_worn_to_occupied_bag_slot_relocates", wornCmd(12, 0, 20, 0), "unequip", []string{rowModelBagKey(29)}, []string{"12"}},
		// 4) 穿上的那个穿戴槽原本有东西 ⇒ 被顶掉的那件回背包 0:20（换装档）。
		{"equip_swap_bag_item_replaces_worn", bagCmd(20, 12), "equip_swap", []string{rowModelBagKey(20)}, []string{"12"}},
		// 5) 两个穿戴槽互换：一个背包 key 都不写。
		{
			"swap_two_worn_slots",
			dnfequip.MoveCommand{
				SelectedCharacterID: 77,
				SourceListType:      3, SourceSlotIndex: 12, SourceInstanceValue: int32(rowModelWornItemID(12)),
				MoveCount:           1,
				DestinationListType: 3, DestinationSlotIndex: 14, DestinationInstanceValue: int32(rowModelWornItemID(14)),
			},
			"equip_slot_swap", nil, []string{"12", "14"},
		},
		// 6) 另一个容器（时装列表 1，同一 slots collection）穿到穿戴槽。
		{
			"equip_avatar_list_to_worn_slot",
			dnfequip.MoveCommand{
				SelectedCharacterID: 77,
				SourceListType:      1, SourceSlotIndex: 0, SourceInstanceValue: int32(rowModelAvatarItemID),
				MoveCount:           1,
				DestinationListType: 3, DestinationSlotIndex: 22, DestinationInstanceValue: 0,
			},
			"equip", []string{"1:0"}, []string{"22"},
		},
		// 7) 另一个 collection（个人仓库列表 2 = warehouse）穿到穿戴槽。
		{
			"equip_cargo_list_to_worn_slot",
			dnfequip.MoveCommand{
				SelectedCharacterID: 77,
				SourceListType:      2, SourceSlotIndex: 0, SourceInstanceValue: int32(rowModelCargoItemID),
				MoveCount:           1,
				DestinationListType: 3, DestinationSlotIndex: 22, DestinationInstanceValue: 0,
			},
			"equip", []string{"2:0"}, []string{"22"},
		},
	}
}

// TestMySQLWriteScopeEquivalenceAcrossOp19MoveSet 是 O-B 的核心判据：
// 七类真实 op19（穿 / 脱 / 脱且改落点 / 换装 / 穿戴槽互换 / 时装列表 / 仓库列表）
// 在 collection 档与 slot+entry 档下落库后的六张表行集合必须**逐行相同**。
func TestMySQLWriteScopeEquivalenceAcrossOp19MoveSet(t *testing.T) {
	for _, testCase := range rowModelMoveCases() {
		t.Run(testCase.name, func(t *testing.T) {
			collectionRun := runRowModelEquipMove(t, testCase.cmd, "collection", "collection")
			deltaRun := runRowModelEquipMove(t, testCase.cmd, "slot", "entry")

			if !collectionRun.changed || !deltaRun.changed {
				t.Fatalf("Changed: collection=%v delta=%v, want both true", collectionRun.changed, deltaRun.changed)
			}
			if collectionRun.mode != testCase.mode || deltaRun.mode != testCase.mode {
				t.Fatalf("Mode: collection=%q delta=%q, want %q", collectionRun.mode, deltaRun.mode, testCase.mode)
			}
			if !reflect.DeepEqual(collectionRun.state, deltaRun.state) {
				t.Fatalf(
					"row state differs between collection and slot/entry scope\n%s",
					rowModelStateDiff(collectionRun.state, deltaRun.state),
				)
			}
			t.Logf(
				"mode=%s collection_writes=%d delta_writes=%d (all statements in the move: collection=%d delta=%d)",
				collectionRun.mode, len(collectionRun.writes), len(deltaRun.writes), collectionRun.totalCalls, deltaRun.totalCalls,
			)
			// 非空洞性：如果 O-B 的接线被拿掉（两条路都整容器写），下面的计数断言必须失败。
			if len(collectionRun.writes) < 40 {
				t.Fatalf("collection scope wrote only %d statements, want the full-container rewrite", len(collectionRun.writes))
			}
			if len(deltaRun.writes) > 20 {
				t.Fatalf("slot/entry scope wrote %d statements, want the dirty-key-only path", len(deltaRun.writes))
			}
			// delta 的 key 集合必须**正好**是本次改动的那 1-2 格（不是别的格、也不是全集）。
			if got, want := deltaDeleteKeys(deltaRun.writes, "dnf_inventory_items"), testCase.inventoryKey; !reflect.DeepEqual(got, want) {
				t.Fatalf("inventory delta keys = %v, want %v", got, want)
			}
			if got, want := deltaDeleteKeys(deltaRun.writes, "dnf_equipment_entries"), testCase.equipmentKey; !reflect.DeepEqual(got, want) {
				t.Fatalf("equipment delta keys = %v, want %v", got, want)
			}
			if len(testCase.inventoryKey) == 0 && keyAppearsInCalls(deltaRun.writes, rowModelBagKey(12)) {
				t.Fatalf("equipment-only move still wrote an inventory key: %v", callArgs(deltaRun.writes))
			}
			// 未改动的 key 一个字节都不许出现在增量写的语句里。
			for _, untouched := range []string{rowModelBagKey(9), rowModelBagKey(10), strconv.Itoa(int(rowModelWornSlotLast))} {
				if keyAppearsInCalls(deltaRun.writes, untouched) {
					t.Fatalf("delta scope touched untouched key %q: %v", untouched, callArgs(deltaRun.writes))
				}
			}
		})
	}
}

// deltaDeleteKeys 取出增量写里 entry_key IN (...) 删除语句携带的 key 集合（排序后）。
func deltaDeleteKeys(writes []fakeSQLCall, table string) []string {
	out := []string{}
	for _, call := range writes {
		if !strings.HasPrefix(call.Query, "DELETE FROM ") || !strings.Contains(call.Query, table) {
			continue
		}
		if !strings.Contains(call.Query, "entry_key IN (") {
			continue
		}
		lead := 1
		if strings.Contains(call.Query, "collection_name = ?") {
			lead = 2
		}
		for _, arg := range call.Args[lead:] {
			if text, ok := arg.(string); ok {
				out = append(out, text)
			}
		}
	}
	sort.Strings(out)
	deduped := out[:0]
	for idx, value := range out {
		if idx > 0 && out[idx-1] == value {
			continue
		}
		deduped = append(deduped, value)
	}
	if len(deduped) == 0 {
		return nil
	}
	return deduped
}

func keyAppearsInCalls(calls []fakeSQLCall, key string) bool {
	for _, call := range calls {
		for _, arg := range call.Args {
			if text, ok := arg.(string); ok && text == key {
				return true
			}
		}
	}
	return false
}

func callArgs(calls []fakeSQLCall) []string {
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, fmt.Sprintf("%s %v", call.Query, call.Args))
	}
	return out
}

func rowModelStateDiff(left, right map[string][]string) string {
	var builder strings.Builder
	tables := make([]string, 0, len(left))
	for table := range left {
		tables = append(tables, table)
	}
	for table := range right {
		if _, ok := left[table]; !ok {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	for _, table := range tables {
		leftRows := left[table]
		rightRows := right[table]
		if reflect.DeepEqual(leftRows, rightRows) {
			continue
		}
		fmt.Fprintf(&builder, "table %s: collection=%d rows, delta=%d rows\n", table, len(leftRows), len(rightRows))
		leftSet := map[string]struct{}{}
		for _, row := range leftRows {
			leftSet[row] = struct{}{}
		}
		rightSet := map[string]struct{}{}
		for _, row := range rightRows {
			rightSet[row] = struct{}{}
		}
		for _, row := range leftRows {
			if _, ok := rightSet[row]; !ok {
				fmt.Fprintf(&builder, "  only in collection: %s\n", row)
			}
		}
		for _, row := range rightRows {
			if _, ok := leftSet[row]; !ok {
				fmt.Fprintf(&builder, "  only in slot/entry: %s\n", row)
			}
		}
	}
	return builder.String()
}

func sortStrings(values []string) {
	sort.Strings(values)
}

// TestMySQLSaveItemStackSlotDeltaWritesOnlyRequestedKeys 钉住增量写的语句集合：
// 父表 upsert + 只含请求 key 的 IN-list DELETE + 只含请求 key 的 INSERT。
func TestMySQLSaveItemStackSlotDeltaWritesOnlyRequestedKeys(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	db := newRowModelDB()
	group := newRowModelGroup(t, db, false)
	record := rowModelFixtureInventory(rowModelCharacterID)
	if err := repository.SaveInventoryFields(ctx, group.Inventory, record, repository.InventoryFieldSlots); err != nil {
		t.Fatalf("seed error = %v", err)
	}
	db.calls = nil

	bag := group.Inventory.(*mysqlBagStore)
	record.Slots[rowModelBagKey(20)] = rowModelItemStack(999999)
	if err := bag.SaveItemStackSlotDelta(ctx, record, "slots", []string{rowModelBagKey(20)}); err != nil {
		t.Fatalf("SaveItemStackSlotDelta() error = %v", err)
	}

	queries := execQueries(&fakeSQLDB{execs: db.calls})
	joined := strings.Join(queries, "\n")
	for _, needle := range []string{
		"DELETE FROM `dnf_s1_w1`.`dnf_inventory_item_extra` WHERE `character_id` = ? AND collection_name = ? AND entry_key IN (?)",
		"DELETE FROM `dnf_s1_w1`.`dnf_inventory_items` WHERE `character_id` = ? AND collection_name = ? AND entry_key IN (?)",
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("slot delta missing %q:\n%s", needle, joined)
		}
	}
	for _, call := range db.calls {
		if !strings.HasPrefix(call.Query, "INSERT INTO ") || !strings.Contains(call.Query, "dnf_inventory_item") {
			continue
		}
		if len(call.Args) < 3 {
			t.Fatalf("insert args = %v", call.Args)
		}
		if key, ok := call.Args[2].(string); !ok || key != rowModelBagKey(20) {
			t.Fatalf("insert touched key %v, want only %q: %s", call.Args[2], rowModelBagKey(20), call.Query)
		}
	}
	for _, call := range db.calls {
		for _, arg := range call.Args {
			if text, ok := arg.(string); ok && text == rowModelBagKey(19) {
				t.Fatalf("slot delta touched untouched key: %s %v", call.Query, call.Args)
			}
		}
	}
}

// TestMySQLSaveEquipmentEntryDeltaWritesOnlyRequestedKeys 同上，穿戴表口径。
func TestMySQLSaveEquipmentEntryDeltaWritesOnlyRequestedKeys(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	db := newRowModelDB()
	group := newRowModelGroup(t, db, false)
	record := rowModelFixtureEquipment(rowModelCharacterID)
	if err := repository.SaveEquipmentFields(ctx, group.Equipment, record, repository.EquipmentFieldEntries); err != nil {
		t.Fatalf("seed error = %v", err)
	}
	db.calls = nil

	store := group.Equipment.(*mysqlEquipmentStore)
	entry := record.Entries["12"]
	entry.ItemID = 555555
	record.Entries["12"] = entry
	if err := store.SaveEquipmentEntryDelta(ctx, record, []string{"12"}); err != nil {
		t.Fatalf("SaveEquipmentEntryDelta() error = %v", err)
	}

	joined := strings.Join(execQueries(&fakeSQLDB{execs: db.calls}), "\n")
	for _, needle := range []string{
		"DELETE FROM `dnf_s1_w1`.`dnf_equipment_entry_extra` WHERE character_id = ? AND entry_key IN (?)",
		"DELETE FROM `dnf_s1_w1`.`dnf_equipment_entries` WHERE character_id = ? AND entry_key IN (?)",
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("entry delta missing %q:\n%s", needle, joined)
		}
	}
	for _, call := range db.calls {
		if !strings.HasPrefix(call.Query, "INSERT INTO ") {
			continue
		}
		if !strings.Contains(call.Query, "dnf_equipment_entries") && !strings.Contains(call.Query, "dnf_equipment_entry_extra") {
			continue
		}
		if len(call.Args) < 2 {
			t.Fatalf("insert args = %v", call.Args)
		}
		if key, ok := call.Args[1].(string); !ok || key != "12" {
			t.Fatalf("insert touched key %v, want only 12: %s", call.Args[1], call.Query)
		}
	}
}

// TestMySQLItemStackDeltaFallsBackToCollectionWhenKeysUnavailable 钉住回退：
// 空 key 集合必须整容器重写（含两条不带 IN 的 DELETE），而不是静默不写。
func TestMySQLItemStackDeltaFallsBackToCollectionWhenKeysUnavailable(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	events := captureWriteEvents(t)

	ctx := context.Background()
	db := newRowModelDB()
	group := newRowModelGroup(t, db, false)
	record := rowModelFixtureInventory(rowModelCharacterID)
	bag := group.Inventory.(*mysqlBagStore)
	if err := bag.SaveItemStackSlotDelta(ctx, record, "slots", nil); err != nil {
		t.Fatalf("SaveItemStackSlotDelta(nil keys) error = %v", err)
	}
	joined := strings.Join(execQueries(&fakeSQLDB{execs: db.calls}), "\n")
	if !strings.Contains(joined, "DELETE FROM `dnf_s1_w1`.`dnf_inventory_item_extra` WHERE `character_id` = ? AND collection_name = ?") {
		t.Fatalf("fallback did not rewrite the whole container:\n%s", joined)
	}
	if strings.Contains(joined, "entry_key IN") {
		t.Fatalf("fallback emitted a partial (key-scoped) delete:\n%s", joined)
	}
	inserts := 0
	for _, call := range db.calls {
		if strings.HasPrefix(call.Query, "INSERT INTO `dnf_s1_w1`.`dnf_inventory_items`") {
			inserts++
		}
	}
	if want := len(record.Slots); inserts != want {
		t.Fatalf("fallback item inserts = %d, want the whole collection (%d)", inserts, want)
	}
	if !hasWriteEvent(*events, "item_stack_keys_unavailable") {
		t.Fatalf("fallback was not reported: %+v", *events)
	}

	// 装备表同款。
	dbEquipment := newRowModelDB()
	groupEquipment := newRowModelGroup(t, dbEquipment, false)
	store := groupEquipment.Equipment.(*mysqlEquipmentStore)
	if err := store.SaveEquipmentEntryDelta(ctx, rowModelFixtureEquipment(rowModelCharacterID), nil); err != nil {
		t.Fatalf("SaveEquipmentEntryDelta(nil keys) error = %v", err)
	}
	joinedEquipment := strings.Join(execQueries(&fakeSQLDB{execs: dbEquipment.calls}), "\n")
	if !strings.Contains(joinedEquipment, "DELETE FROM `dnf_s1_w1`.`dnf_equipment_entries` WHERE character_id = ?") {
		t.Fatalf("equipment fallback did not rewrite the whole table:\n%s", joinedEquipment)
	}
	if strings.Contains(joinedEquipment, "entry_key IN") {
		t.Fatalf("equipment fallback emitted a partial delete:\n%s", joinedEquipment)
	}
	if !hasWriteEvent(*events, "equipment_entry_keys_unavailable") {
		t.Fatalf("equipment fallback was not reported: %+v", *events)
	}
}

// TestMySQLDeltaGuardsRejectSilentNoWrite 钉住"最坏形状在结构上不可能"：
// 底层 key 集合为空 ⇒ 硬错误（事务回滚）；collection 不认识 ⇒ 硬错误。
func TestMySQLDeltaGuardsRejectSilentNoWrite(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	db := newRowModelDB()
	group := newRowModelGroup(t, db, false)
	record := rowModelFixtureInventory(rowModelCharacterID)
	db.calls = nil

	if err := replaceItemStackKeys(
		ctx, db, "`dnf_s1_w1`.`dnf_inventory_items`", "`dnf_s1_w1`.`dnf_inventory_item_extra`",
		"character_id", rowModelCharacterID, "slots", true, record.Slots, nil,
	); err == nil {
		t.Fatal("replaceItemStackKeys(nil keys) = nil, want a hard error")
	}
	if err := replaceEquipmentEntryKeys(
		ctx, db, "`dnf_s1_w1`.`dnf_equipment_entries`", "`dnf_s1_w1`.`dnf_equipment_entry_extra`",
		rowModelCharacterID, rowModelFixtureEquipment(rowModelCharacterID).Entries, []string{},
	); err == nil {
		t.Fatal("replaceEquipmentEntryKeys(empty keys) = nil, want a hard error")
	}
	if len(db.calls) != 0 {
		t.Fatalf("guarded delta paths emitted SQL: %v", execQueries(&fakeSQLDB{execs: db.calls}))
	}

	bag := group.Inventory.(*mysqlBagStore)
	if err := bag.SaveItemStackSlotDelta(ctx, record, "bags", []string{rowModelBagKey(9)}); err == nil {
		t.Fatal("SaveItemStackSlotDelta(unknown collection) = nil, want a hard error")
	}
	if len(db.calls) != 0 {
		t.Fatalf("unknown collection emitted SQL: %v", execQueries(&fakeSQLDB{execs: db.calls}))
	}
}

func captureWriteEvents(t *testing.T) *[]WriteEvent {
	t.Helper()
	events := &[]WriteEvent{}
	SetWriteEventObserver(func(event WriteEvent) {
		*events = append(*events, event)
	})
	t.Cleanup(func() { SetWriteEventObserver(nil) })
	return events
}

func hasWriteEvent(events []WriteEvent, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}
