// 本文件是"背包内物品移动"（op19 的非穿戴端点分支，唯一出口是
// inventory.Owner.saveMoveResult）走 O-B 增量写的等价性与语句集合证据。
//
// 判据与 equip 侧同构（mysql_write_scope_equivalence_test.go）：同一份初值分别用
// DNFBRIDGE_INVENTORY_SAVE_SCOPE 的 collection 档与 slot 档跑同一条**真实的**
// inventory.Owner.Move，比对六张表的最终行集合（reflect.DeepEqual），并断言增量档
// 真正写到的 key 集合**正好**是本次改动的那几格、未改动的格一个字节都不出现。
//
// 这是 R1（"key 集合错/漏 ⇒ 某格永远不落库"）唯一的可证伪判据：任何一格被改了却没进
// key 集合，增量档的库状态就会与整容器档不同，本文件立刻失败。
package mysql

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/repository"
)

const (
	bagMoveCharacterID = rowModelCharacterID
	bagMoveAccountID   = "acct-77"

	bagMoveListMain          byte = 0
	bagMoveListAvatar        byte = 1
	bagMoveListPersonalCargo byte = 2
	bagMoveListGuildMedal    byte = 38
)

func bagMoveSlotKey(listType byte, slot int16) string {
	return fmt.Sprintf("%d:%d", listType, slot)
}

func bagMoveEquipStack(itemID int64) repository.ItemStack {
	return repository.ItemStack{
		ItemID:   itemID,
		Count:    1,
		Bind:     true,
		RawEntry: rowModelRawEntry(byte(itemID)),
		Extra: map[string]string{
			"pvf_path":                    "equipment/weapon/sword.equ",
			"item_kind":                   "equipment",
			"current_exe_inventory_count": "1",
		},
	}
}

func bagMoveStackable(itemID, count int64) repository.ItemStack {
	return repository.ItemStack{
		ItemID:   itemID,
		Count:    count,
		Bind:     true,
		RawEntry: rowModelRawEntry(byte(itemID)),
		Extra: map[string]string{
			"pvf_path":                    "stackable/consumption/potion.stk",
			"item_kind":                   "stackable",
			"stack_limit":                 "100",
			"current_exe_inventory_count": strconv.FormatInt(count, 10),
		},
	}
}

// bagMoveBaseRecord 是全部用例共用的初值：
//
//	slots      9/10/11 装备（不可堆叠）、12 = 500001x5、13 = 500001x7（可合并）、
//	          14 = 500002x5、20 = 500003x3、1:0 时装、38:0 勋章；
//	warehouse  2:0 装备、2:1 = 500001x2（可与 0:12 跨容器合并）。
//
// 空格（30..49）留给"移到空格"与"自动并入第三方格"的形状。
func bagMoveBaseRecord() repository.InventoryRecord {
	return repository.InventoryRecord{
		CharacterID: bagMoveCharacterID,
		Slots: map[string]repository.ItemStack{
			bagMoveSlotKey(bagMoveListMain, 9):       bagMoveEquipStack(100009),
			bagMoveSlotKey(bagMoveListMain, 10):      bagMoveEquipStack(100010),
			bagMoveSlotKey(bagMoveListMain, 11):      bagMoveEquipStack(100011),
			bagMoveSlotKey(bagMoveListMain, 12):      bagMoveStackable(500001, 5),
			bagMoveSlotKey(bagMoveListMain, 13):      bagMoveStackable(500001, 7),
			bagMoveSlotKey(bagMoveListMain, 14):      bagMoveStackable(500002, 5),
			bagMoveSlotKey(bagMoveListMain, 20):      bagMoveStackable(500003, 3),
			bagMoveSlotKey(bagMoveListAvatar, 0):     bagMoveEquipStack(300001),
			bagMoveSlotKey(bagMoveListGuildMedal, 0): bagMoveEquipStack(380001),
		},
		Warehouse: map[string]repository.ItemStack{
			bagMoveSlotKey(bagMoveListPersonalCargo, 0): bagMoveEquipStack(400001),
			bagMoveSlotKey(bagMoveListPersonalCargo, 1): bagMoveStackable(500001, 2),
			// 2:2 不被任何用例碰到：用来断言"未改动的 key 一个字节都不出现"。
			bagMoveSlotKey(bagMoveListPersonalCargo, 2): bagMoveEquipStack(400002),
		},
	}
}

type bagMoveCase struct {
	name string
	cmd  dnfinventory.Command
	// seed 在共用初值上做用例专属调整（可空）。
	seed func(*repository.InventoryRecord)
	// wantMode 是 Owner.Move 的 result.Mode。
	wantMode string
	// wantSlots / wantWarehouse 是本次移动真正改动的 key（`<list>:<slot>`）。
	wantSlots     []string
	wantWarehouse []string
	// wantDeltaWriteCalls 是增量档落库语句数上限（非空洞性判据）。
	wantDeltaWriteCalls int
}

func bagMoveCommand(srcList byte, srcSlot int16, dstList byte, dstSlot int16, moveCount int32) dnfinventory.Command {
	return dnfinventory.Command{
		Operation:            "move-item-space",
		AccountID:            bagMoveAccountID,
		SelectedCharacterID:  77,
		SourceListType:       srcList,
		SourceSlotIndex:      srcSlot,
		MoveCount:            moveCount,
		DestinationListType:  dstList,
		DestinationSlotIndex: dstSlot,
	}
}

func bagMoveCases() []bagMoveCase {
	return []bagMoveCase{
		// 1) 背包内互换：两格都有货、不可堆叠 ⇒ swap。
		{
			name:      "within_bag_swap_two_occupied_slots",
			cmd:       bagMoveCommand(bagMoveListMain, 9, bagMoveListMain, 10, 1),
			wantMode:  "swap",
			wantSlots: []string{bagMoveSlotKey(bagMoveListMain, 9), bagMoveSlotKey(bagMoveListMain, 10)},
		},
		// 2) 移到空格：目标格空、容器里也没有可并入的同种堆叠 ⇒ move（源格行消失）。
		{
			name:      "move_to_empty_slot",
			cmd:       bagMoveCommand(bagMoveListMain, 20, bagMoveListMain, 40, 3),
			wantMode:  "move",
			wantSlots: []string{bagMoveSlotKey(bagMoveListMain, 20), bagMoveSlotKey(bagMoveListMain, 40)},
		},
		// 3) 移到被占的格（同 itemID 可堆叠）⇒ stack：两格的 Count 都变。
		{
			name:      "move_to_occupied_slot_merges_stack_count",
			cmd:       bagMoveCommand(bagMoveListMain, 12, bagMoveListMain, 13, 5),
			wantMode:  "stack",
			wantSlots: []string{bagMoveSlotKey(bagMoveListMain, 12), bagMoveSlotKey(bagMoveListMain, 13)},
		},
		// 4) 拆堆到空格（容器里没有同种堆叠）⇒ split：两格的 Count 都变。
		{
			name:      "split_stack_to_empty_slot",
			cmd:       bagMoveCommand(bagMoveListMain, 20, bagMoveListMain, 41, 1),
			wantMode:  "split",
			wantSlots: []string{bagMoveSlotKey(bagMoveListMain, 20), bagMoveSlotKey(bagMoveListMain, 41)},
		},
		// 5) 落点空但容器里有可并入的同种堆叠 ⇒ auto_stack：真正被写的是**第三方格**
		//    0:13（不是请求里的落点 0:42）。这一格是 R1 最容易漏的 key。
		{
			name:      "auto_stack_into_third_slot",
			cmd:       bagMoveCommand(bagMoveListMain, 12, bagMoveListMain, 42, 5),
			wantMode:  "auto_stack",
			wantSlots: []string{bagMoveSlotKey(bagMoveListMain, 12), bagMoveSlotKey(bagMoveListMain, 13)},
		},
		// 6) 源格空、目标格有货（客户端把方向写反）⇒ reverse_move：源格被填上。
		{
			name:      "reverse_move_from_empty_source",
			cmd:       bagMoveCommand(bagMoveListMain, 43, bagMoveListMain, 9, 1),
			wantMode:  "reverse_move",
			wantSlots: []string{bagMoveSlotKey(bagMoveListMain, 43), bagMoveSlotKey(bagMoveListMain, 9)},
		},
		// 7) 跨容器：主背包 0:9 → 仓库空格 2:5（两个 collection 各写一次）。
		{
			name:          "cross_container_bag_to_warehouse_empty_slot",
			cmd:           bagMoveCommand(bagMoveListMain, 9, bagMoveListPersonalCargo, 5, 1),
			wantMode:      "move",
			wantSlots:     []string{bagMoveSlotKey(bagMoveListMain, 9)},
			wantWarehouse: []string{bagMoveSlotKey(bagMoveListPersonalCargo, 5)},
		},
		// 8) 跨容器反向：仓库 2:0 → 主背包空格 0:44。
		{
			name:          "cross_container_warehouse_to_bag_empty_slot",
			cmd:           bagMoveCommand(bagMoveListPersonalCargo, 0, bagMoveListMain, 44, 1),
			wantMode:      "move",
			wantSlots:     []string{bagMoveSlotKey(bagMoveListMain, 44)},
			wantWarehouse: []string{bagMoveSlotKey(bagMoveListPersonalCargo, 0)},
		},
		// 9) 跨容器堆叠合并：主背包 0:12 → 仓库 2:1（同种堆叠）⇒ 源格行消失。
		{
			name:          "cross_container_stack_merge_bag_to_warehouse",
			cmd:           bagMoveCommand(bagMoveListMain, 12, bagMoveListPersonalCargo, 1, 5),
			wantMode:      "stack",
			wantSlots:     []string{bagMoveSlotKey(bagMoveListMain, 12)},
			wantWarehouse: []string{bagMoveSlotKey(bagMoveListPersonalCargo, 1)},
		},
		// 10) 同一 collection 的另一个 list：时装页 1:0 → 主背包空格 0:45。
		{
			name:      "avatar_list_to_bag_empty_slot",
			cmd:       bagMoveCommand(bagMoveListAvatar, 0, bagMoveListMain, 45, 1),
			wantMode:  "move",
			wantSlots: []string{bagMoveSlotKey(bagMoveListAvatar, 0), bagMoveSlotKey(bagMoveListMain, 45)},
		},
		// 11) 勋章页 38:0 → 主背包空格 0:46（同一 collection，不同 list 编号）。
		{
			name:      "guild_medal_list_to_bag_empty_slot",
			cmd:       bagMoveCommand(bagMoveListGuildMedal, 0, bagMoveListMain, 46, 1),
			wantMode:  "move",
			wantSlots: []string{bagMoveSlotKey(bagMoveListGuildMedal, 0), bagMoveSlotKey(bagMoveListMain, 46)},
		},
	}
}

// rowModelAccountInventory 是账号仓库仓储的最小替身。
//
// 为什么需要：主背包（list 0）移动的 result.Refresh 分支会读一次账号仓库
// （inventory/owner.go 的 withMoveRefresh → accountRepo.Load），而账号仓库的三张表
// 不在行模型的六张表里。它只影响回给客户端的刷新快照（result.Refresh），
// **不参与** inventory 落库，因此替身不会掩盖任何写路径差异。
type rowModelAccountInventory struct{}

func (rowModelAccountInventory) Load(context.Context, string) (repository.AccountInventoryRecord, bool, error) {
	return repository.AccountInventoryRecord{}, false, nil
}

func (rowModelAccountInventory) Save(context.Context, repository.AccountInventoryRecord) error {
	return nil
}

func newRowModelBagGroup(t *testing.T, db *rowModelDB) repository.Group {
	t.Helper()
	group := newRowModelGroup(t, db, true)
	group.AccountInventory = rowModelAccountInventory{}
	// 本组验证增量写入，夹具明确开放原先使用的全部主包/仓库格。
	if err := group.Settings.Save(context.Background(), repository.SettingsRecord{
		Scope:  repository.CharacterContainerStateScope("77"),
		Values: map[string]string{"main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "200"},
	}); err != nil {
		t.Fatal(err)
	}
	return group
}

type bagMoveRun struct {
	state       map[string][]string
	writes      []fakeSQLCall
	totalCalls  int
	mode        string
	changed     bool
	fallbacks   []repository.InventoryWriteFallbackEvent
	writeEvents []WriteEvent
}

func captureInventoryWriteFallbacks(t *testing.T) *[]repository.InventoryWriteFallbackEvent {
	t.Helper()
	events := &[]repository.InventoryWriteFallbackEvent{}
	var mu sync.Mutex
	repository.SetInventoryWriteFallbackObserver(func(event repository.InventoryWriteFallbackEvent) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, event)
	})
	t.Cleanup(func() { repository.SetInventoryWriteFallbackObserver(nil) })
	return events
}

// runRowModelBagMove 在行模型上跑一条真实的 inventory.Owner.Move：
// 先用"整容器写"灌初值（= 线上库里的样子），清空调用记录，再只统计被测那一次。
func runRowModelBagMove(t *testing.T, testCase bagMoveCase, scope string) bagMoveRun {
	t.Helper()
	t.Setenv("DNFBRIDGE_INVENTORY_SAVE_SCOPE", scope)

	ctx := context.Background()
	db := newRowModelDB()
	group := newRowModelBagGroup(t, db)
	record := bagMoveBaseRecord()
	if testCase.seed != nil {
		testCase.seed(&record)
	}
	if err := repository.SaveInventoryFields(
		ctx, group.Inventory, record,
		repository.InventoryFieldSlots, repository.InventoryFieldWarehouse,
	); err != nil {
		t.Fatalf("seed inventory error = %v", err)
	}

	fallbacks := captureInventoryWriteFallbacks(t)
	writeEvents := captureWriteEvents(t)

	owner, err := dnfinventory.NewOwner(group)
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	db.calls = nil
	result, err := owner.Move(ctx, testCase.cmd)
	if err != nil {
		t.Fatalf("Move(%+v) error = %v", testCase.cmd, err)
	}
	writes := make([]fakeSQLCall, 0, len(db.calls))
	for _, call := range db.calls {
		if strings.HasPrefix(call.Query, "INSERT INTO ") || strings.HasPrefix(call.Query, "DELETE FROM ") {
			writes = append(writes, call)
		}
	}
	return bagMoveRun{
		state:       db.model.snapshot(),
		writes:      writes,
		totalCalls:  len(db.calls),
		mode:        result.Mode,
		changed:     result.Changed,
		fallbacks:   append([]repository.InventoryWriteFallbackEvent(nil), (*fallbacks)...),
		writeEvents: append([]WriteEvent(nil), (*writeEvents)...),
	}
}

// bagDeltaKeysByCollection 取出增量删除语句里每个 collection 的 key 集合（已排序）。
func bagDeltaKeysByCollection(writes []fakeSQLCall, table string) map[string][]string {
	out := map[string][]string{}
	for _, call := range writes {
		if !strings.HasPrefix(call.Query, "DELETE FROM ") || !strings.Contains(call.Query, table) {
			continue
		}
		if !strings.Contains(call.Query, "entry_key IN (") || len(call.Args) < 3 {
			continue
		}
		collection, ok := call.Args[1].(string)
		if !ok {
			continue
		}
		for _, arg := range call.Args[2:] {
			if text, ok := arg.(string); ok {
				out[collection] = append(out[collection], text)
			}
		}
	}
	for collection := range out {
		sort.Strings(out[collection])
	}
	return out
}

// TestMySQLBagMoveWriteScopeEquivalenceAcrossMoveSet 是背包内移动的核心判据：
// 11 类真实 op19（互换 / 移空格 / 合并 / 拆堆 / 自动并入第三方格 / 反向移动 /
// 跨容器两个方向 / 跨容器合并 / 时装页 / 勋章页）在 collection 档与 slot 档下落库后的
// 六张表行集合必须**逐行相同**，且增量档写到的 key 恰好是本次改动的那几格。
func TestMySQLBagMoveWriteScopeEquivalenceAcrossMoveSet(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	for _, testCase := range bagMoveCases() {
		t.Run(testCase.name, func(t *testing.T) {
			collectionRun := runRowModelBagMove(t, testCase, "collection")
			deltaRun := runRowModelBagMove(t, testCase, "slot")

			if !collectionRun.changed || !deltaRun.changed {
				t.Fatalf("Changed: collection=%v delta=%v, want both true", collectionRun.changed, deltaRun.changed)
			}
			if collectionRun.mode != testCase.wantMode || deltaRun.mode != testCase.wantMode {
				t.Fatalf("Mode: collection=%q delta=%q, want %q", collectionRun.mode, deltaRun.mode, testCase.wantMode)
			}
			if !reflect.DeepEqual(collectionRun.state, deltaRun.state) {
				t.Fatalf(
					"row state differs between collection and slot scope\n%s",
					rowModelStateDiff(collectionRun.state, deltaRun.state),
				)
			}
			if len(collectionRun.fallbacks) != 0 || len(deltaRun.fallbacks) != 0 {
				t.Fatalf("fallback events: collection=%+v delta=%+v", collectionRun.fallbacks, deltaRun.fallbacks)
			}
			if len(collectionRun.writeEvents) != 0 || len(deltaRun.writeEvents) != 0 {
				t.Fatalf("mysql write events: collection=%+v delta=%+v", collectionRun.writeEvents, deltaRun.writeEvents)
			}
			t.Logf(
				"mode=%s collection_writes=%d delta_writes=%d (all statements in the move: collection=%d delta=%d)",
				collectionRun.mode, len(collectionRun.writes), len(deltaRun.writes),
				collectionRun.totalCalls, deltaRun.totalCalls,
			)

			// 非空洞性：把增量接线拿掉（两条路都整容器写）时下面的断言必须失败。
			if len(collectionRun.writes) < 8 {
				t.Fatalf("collection scope wrote only %d statements, want the full-container rewrite", len(collectionRun.writes))
			}
			if len(collectionRun.writes) == len(deltaRun.writes) {
				t.Fatalf("collection and slot scope wrote the same %d statements, want the dirty-key-only path", len(deltaRun.writes))
			}
			// 增量档：父表 2 条 upsert（跨容器两次调用）以内 + 每个 collection 2 条 IN-DELETE
			// + 该 collection 自己那几行 INSERT。
			if limit := testCase.wantDeltaWriteCalls; limit > 0 && len(deltaRun.writes) > limit {
				t.Fatalf("slot scope wrote %d statements, want <= %d", len(deltaRun.writes), limit)
			}

			keys := bagDeltaKeysByCollection(deltaRun.writes, "dnf_inventory_items")
			wantSlots := append([]string(nil), testCase.wantSlots...)
			wantWarehouse := append([]string(nil), testCase.wantWarehouse...)
			sort.Strings(wantSlots)
			sort.Strings(wantWarehouse)
			if !reflect.DeepEqual(keys["slots"], wantSlots) {
				t.Fatalf("slots delta keys = %v, want %v", keys["slots"], wantSlots)
			}
			if !reflect.DeepEqual(keys["warehouse"], wantWarehouse) {
				t.Fatalf("warehouse delta keys = %v, want %v", keys["warehouse"], wantWarehouse)
			}
			// 未改动的格一个字节都不许出现在增量写的语句里。
			for _, untouched := range []string{
				bagMoveSlotKey(bagMoveListMain, 11),
				bagMoveSlotKey(bagMoveListMain, 14),
				bagMoveSlotKey(bagMoveListPersonalCargo, 2),
				bagMoveSlotKey(bagMoveListMain, 47),
			} {
				if keyAppearsInCalls(deltaRun.writes, untouched) {
					t.Fatalf("delta scope touched untouched key %q: %v", untouched, callArgs(deltaRun.writes))
				}
			}
		})
	}
}

// TestMySQLBagMoveDeltaFallsBackToCollectionWhenKeysUnavailable 钉住"取不到 key"这一格：
// 背包内移动必须整容器重写（两个 collection 都重写、且是**不带 IN** 的整容器 DELETE），
// 绝不能出现"只写了其中一个容器"或"key 集合不全却照写"的部分写。
func TestMySQLBagMoveDeltaFallsBackToCollectionWhenKeysUnavailable(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	t.Setenv("DNFBRIDGE_INVENTORY_SAVE_SCOPE", "slot")
	events := captureInventoryWriteFallbacks(t)

	ctx := context.Background()
	fields := []repository.InventoryField{repository.InventoryFieldSlots, repository.InventoryFieldWarehouse}
	for _, testCase := range []struct {
		name string
		keys repository.InventorySlotDeltaKeys
	}{
		{"nil_keys", nil},
		{"empty_map", repository.InventorySlotDeltaKeys{}},
		{"slots_only_partial", repository.InventorySlotDeltaKeys{
			"slots": []string{bagMoveSlotKey(bagMoveListMain, 9)},
		}},
		{"warehouse_only_partial", repository.InventorySlotDeltaKeys{
			"warehouse": []string{bagMoveSlotKey(bagMoveListPersonalCargo, 0)},
		}},
		{"empty_key_lists", repository.InventorySlotDeltaKeys{"slots": nil, "warehouse": nil}},
		{"unknown_collection", repository.InventorySlotDeltaKeys{"bags": []string{"0:9"}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newRowModelDB()
			group := newRowModelBagGroup(t, db)
			record := bagMoveBaseRecord()
			if err := repository.SaveInventoryFields(ctx, group.Inventory, record, fields...); err != nil {
				t.Fatalf("seed error = %v", err)
			}
			db.calls = nil
			*events = nil

			if err := repository.SaveInventoryFieldsScoped(ctx, group.Inventory, record, fields, testCase.keys); err != nil {
				t.Fatalf("SaveInventoryFieldsScoped() error = %v", err)
			}
			joined := strings.Join(execQueries(&fakeSQLDB{execs: db.calls}), "\n")
			for _, needle := range []string{
				"DELETE FROM `dnf_s1_w1`.`dnf_inventory_item_extra` WHERE `character_id` = ? AND collection_name = ?",
				"DELETE FROM `dnf_s1_w1`.`dnf_inventory_items` WHERE `character_id` = ? AND collection_name = ?",
			} {
				if !strings.Contains(joined, needle) {
					t.Fatalf("fallback did not rewrite the whole container, missing %q:\n%s", needle, joined)
				}
			}
			if strings.Contains(joined, "entry_key IN") {
				t.Fatalf("fallback emitted a partial (key-scoped) write:\n%s", joined)
			}
			// 两个 collection 的 item 行都必须按全集重写（slots 9 行 + warehouse 3 行）。
			slotsInserts, warehouseInserts := 0, 0
			for _, call := range db.calls {
				if !strings.HasPrefix(call.Query, "INSERT INTO `dnf_s1_w1`.`dnf_inventory_items`") || len(call.Args) < 2 {
					continue
				}
				switch call.Args[1] {
				case "slots":
					slotsInserts++
				case "warehouse":
					warehouseInserts++
				}
			}
			if want := len(record.Slots); slotsInserts != want {
				t.Fatalf("fallback slots inserts = %d, want the whole collection (%d)", slotsInserts, want)
			}
			if want := len(record.Warehouse); warehouseInserts != want {
				t.Fatalf("fallback warehouse inserts = %d, want the whole collection (%d)", warehouseInserts, want)
			}
			// 域层必须留痕：inventory:keys_unavailable（不是静默不写）。
			reasons := []string{}
			for _, event := range *events {
				reasons = append(reasons, event.Target+":"+event.Reason)
			}
			if !reflect.DeepEqual(reasons, []string{"inventory:keys_unavailable"}) {
				t.Fatalf("fallback events = %v, want [inventory:keys_unavailable]", reasons)
			}
		})
	}
}

// TestMySQLBagMoveDeltaLowLevelRejectsEmptyKeys 复核第三条安全性质在背包路径上同样成立：
// 空 key 集合直达仓储最底层时必须是**硬错误**（事务回滚），而不是静默不写。
// 仓储入口（SaveItemStackSlotDelta）那一层则是"整容器写 + item_stack_keys_unavailable
// 事件"，由 TestMySQLItemStackDeltaFallsBackToCollectionWhenKeysUnavailable 覆盖。
func TestMySQLBagMoveDeltaLowLevelRejectsEmptyKeys(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	db := newRowModelDB()
	group := newRowModelBagGroup(t, db)
	bag := group.Inventory.(*mysqlBagStore)
	db.calls = nil

	// 仓储入口：空 key = 整容器写 + 事件，绝不报错也绝不空写。
	events := captureWriteEvents(t)
	if err := bag.SaveItemStackSlotDelta(ctx, bagMoveBaseRecord(), "slots", nil); err != nil {
		t.Fatalf("SaveItemStackSlotDelta(nil keys) error = %v", err)
	}
	if !hasWriteEvent(*events, "item_stack_keys_unavailable") {
		t.Fatalf("repository-level fallback was not reported: %+v", *events)
	}

	// 最底层：replaceItemStackKeys 的硬错误（与 O-B 落地时的判据同一条）。
	if err := replaceItemStackKeys(
		ctx, db, "`dnf_s1_w1`.`dnf_inventory_items`", "`dnf_s1_w1`.`dnf_inventory_item_extra`",
		"character_id", bagMoveCharacterID, "slots", true, bagMoveBaseRecord().Slots, nil,
	); err == nil {
		t.Fatal("replaceItemStackKeys(nil keys) = nil, want a hard error")
	}
}

// TestMySQLBagMoveCrossContainerWritesBothCollections 钉住 study 点名的 R3：
// "主背包 ↔ 仓库"的一次移动必须对两个 collection 各写一次增量删除+插入；
// 只写其中一个就是静默数据丢失。
func TestMySQLBagMoveCrossContainerWritesBothCollections(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	for _, testCase := range bagMoveCases() {
		if len(testCase.wantWarehouse) == 0 || len(testCase.wantSlots) == 0 {
			continue
		}
		t.Run(testCase.name, func(t *testing.T) {
			run := runRowModelBagMove(t, testCase, "slot")
			keys := bagDeltaKeysByCollection(run.writes, "dnf_inventory_items")
			if len(keys["slots"]) == 0 || len(keys["warehouse"]) == 0 {
				t.Fatalf("cross-container move keys = %v, want both collections", keys)
			}
			if _, ok := keys["warehouse"]; !ok {
				t.Fatalf("cross-container move never wrote the warehouse collection: %v", callArgs(run.writes))
			}
		})
	}
}

// TestMySQLBagMoveIncrementalStatementCount 记录并钉住增量档的语句规模：
// 参考实现一次背包内移动是 8 条 SQL，我们的口径是"父表 upsert + 每个 collection
// 2 条 IN-DELETE + 该 collection 自己被改动的那几行 INSERT"。
func TestMySQLBagMoveIncrementalStatementCount(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	for _, testCase := range bagMoveCases() {
		t.Run(testCase.name, func(t *testing.T) {
			run := runRowModelBagMove(t, testCase, "slot")
			deletes, inserts := 0, 0
			for _, call := range run.writes {
				switch {
				case strings.HasPrefix(call.Query, "DELETE FROM "):
					deletes++
				case strings.HasPrefix(call.Query, "INSERT INTO "):
					inserts++
				}
			}
			t.Logf(
				"mode=%s delta writes=%d (inserts=%d deletes=%d) all statements=%d",
				run.mode, len(run.writes), inserts, deletes, run.totalCalls,
			)
			if deletes > 4 {
				t.Fatalf("delta deletes = %d, want <= 4 (2 per affected collection)", deletes)
			}
			if len(run.writes) > 40 {
				t.Fatalf("delta writes = %d, want the dirty-slot-only path", len(run.writes))
			}
		})
	}
}
