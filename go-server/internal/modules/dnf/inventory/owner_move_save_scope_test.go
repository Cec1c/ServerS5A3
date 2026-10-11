// 本文件是 O-B 在**背包内移动**（inventory）这一侧的判据，结构与 equip 侧
// owner_move_save_scope_test.go 同构：
//
//  1. 开关默认档（以及全部回滚字面量）必须逐字节等于今天：走
//     dnfrepo.SaveInventoryFields 的整容器路，一个增量接口都不调；
//  2. slot 档必须把**本次移动真正改动的槽**按 collection 分组交给增量写接口 ——
//     跨容器移动（主背包 ↔ 仓库）对两个 collection 各调一次；
//  3. 任何"取不到 key / key 不含全部受影响容器 / 仓储不支持增量"的情况必须
//     **回退整容器写并记一条事件** —— 静默不写是数据丢失，是这项改动唯一不可接受的
//     失败形状。
package inventory

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type moveDeltaCall struct {
	characterID string
	collection  string
	keys        []string
}

// recordingMoveRepository 同时实现 dnfrepo.InventoryRepository 与增量写接口，并记录调用。
type recordingMoveRepository struct {
	mu          sync.Mutex
	loaded      dnfrepo.InventoryRecord
	fullSaves   [][]dnfrepo.InventoryField
	fullRecords []dnfrepo.InventoryRecord
	deltaCalls  []moveDeltaCall
}

func (r *recordingMoveRepository) Load(context.Context, string) (dnfrepo.InventoryRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return dnfrepo.CloneInventory(r.loaded), true, nil
}

func (r *recordingMoveRepository) Save(_ context.Context, record dnfrepo.InventoryRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fullSaves = append(r.fullSaves, dnfrepo.AllInventoryFields())
	r.fullRecords = append(r.fullRecords, dnfrepo.CloneInventory(record))
	return nil
}

func (r *recordingMoveRepository) SaveFields(_ context.Context, record dnfrepo.InventoryRecord, fields ...dnfrepo.InventoryField) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fullSaves = append(r.fullSaves, append([]dnfrepo.InventoryField(nil), fields...))
	r.fullRecords = append(r.fullRecords, dnfrepo.CloneInventory(record))
	return nil
}

func (r *recordingMoveRepository) SaveItemStackSlotDelta(
	_ context.Context,
	record dnfrepo.InventoryRecord,
	collection string,
	keys []string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deltaCalls = append(r.deltaCalls, moveDeltaCall{
		characterID: record.CharacterID,
		collection:  collection,
		keys:        append([]string(nil), keys...),
	})
	return nil
}

// fullOnlyMoveRepository 只实现 dnfrepo 接口（= memory / trade-scoped / rental-scoped /
// settlement-scoped 的形状）：类型断言必须失败并回退整容器写。
type fullOnlyMoveRepository struct {
	saves   int
	last    dnfrepo.InventoryRecord
	loaded  dnfrepo.InventoryRecord
	deltaNo bool // 只为让"没有增量方法"这件事显式可见
}

func (r *fullOnlyMoveRepository) Load(context.Context, string) (dnfrepo.InventoryRecord, bool, error) {
	return dnfrepo.CloneInventory(r.loaded), true, nil
}

func (r *fullOnlyMoveRepository) Save(_ context.Context, record dnfrepo.InventoryRecord) error {
	r.saves++
	r.last = dnfrepo.CloneInventory(record)
	return nil
}

type recordingMoveUnitOfWork struct {
	inventory dnfrepo.InventoryRepository
}

func (u recordingMoveUnitOfWork) WithinCharacterItems(
	_ context.Context,
	characterID string,
	apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	if strings.TrimSpace(characterID) == "" || apply == nil {
		return dnfrepo.ErrCharacterItemTransactionUnavailable
	}
	return apply(u.inventory, nil)
}

// moveAccountInventoryStub 是账号仓库仓储替身：主背包（list 0）移动的刷新分支会读一次
// 账号仓库，它只影响 result.Refresh，不参与背包落库。
type moveAccountInventoryStub struct{}

func (moveAccountInventoryStub) Load(context.Context, string) (dnfrepo.AccountInventoryRecord, bool, error) {
	return dnfrepo.AccountInventoryRecord{}, false, nil
}

func (moveAccountInventoryStub) Save(context.Context, dnfrepo.AccountInventoryRecord) error {
	return nil
}

func captureInventorySaveFallbacks(t *testing.T) *[]dnfrepo.InventoryWriteFallbackEvent {
	t.Helper()
	events := &[]dnfrepo.InventoryWriteFallbackEvent{}
	var mu sync.Mutex
	dnfrepo.SetInventoryWriteFallbackObserver(func(event dnfrepo.InventoryWriteFallbackEvent) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, event)
	})
	t.Cleanup(func() { dnfrepo.SetInventoryWriteFallbackObserver(nil) })
	return events
}

func inventorySaveFallbackReasons(events []dnfrepo.InventoryWriteFallbackEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Target+":"+event.Reason)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}

func moveTestStack(itemID, count int64, kind, pvfPath string) dnfrepo.ItemStack {
	extra := map[string]string{
		"pvf_path":                    pvfPath,
		"item_kind":                   kind,
		"current_exe_inventory_count": "1",
	}
	if kind == "stackable" {
		extra["stack_limit"] = "100"
	}
	raw := make([]byte, 0x38)
	for idx := range raw {
		raw[idx] = byte(itemID) + byte(idx)
	}
	return dnfrepo.ItemStack{ItemID: itemID, Count: count, Bind: true, RawEntry: raw, Extra: extra}
}

func moveTestInventoryRecord() dnfrepo.InventoryRecord {
	return dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:9":  moveTestStack(100009, 1, "equipment", "equipment/weapon/sword.equ"),
			"0:10": moveTestStack(100010, 1, "equipment", "equipment/weapon/sword.equ"),
			"0:12": moveTestStack(500001, 5, "stackable", "stackable/consumption/potion.stk"),
			"0:13": moveTestStack(500001, 7, "stackable", "stackable/consumption/potion.stk"),
			"0:20": moveTestStack(500003, 3, "stackable", "stackable/consumption/potion.stk"),
		},
		Warehouse: map[string]dnfrepo.ItemStack{
			"2:0": moveTestStack(400001, 1, "equipment", "equipment/weapon/sword.equ"),
			"2:1": moveTestStack(500001, 2, "stackable", "stackable/consumption/potion.stk"),
		},
	}
}

// TestSaveMoveResultDefaultsToCollectionScope 钉住默认档与**全部回滚字面量**：
// 一个增量接口都不调，整容器写只发一次（字段集合 = srcField/dstField 归一化后的全集）。
func TestSaveMoveResultDefaultsToCollectionScope(t *testing.T) {
	for _, literal := range []string{
		"", "collection", "COLLECTION", "full", "today", "legacy", "off", "0", "false", "no", "disable", "disabled", "none",
	} {
		t.Run("literal="+literal, func(t *testing.T) {
			t.Setenv(dnfrepo.InventorySaveScopeEnvName, literal)
			events := captureInventorySaveFallbacks(t)

			repo := &recordingMoveRepository{loaded: moveTestInventoryRecord()}
			record := moveTestInventoryRecord()
			keys := newBagMoveWriteKeys(dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldWarehouse)
			keys.add(dnfrepo.InventoryFieldSlots, "0:9")
			keys.add(dnfrepo.InventoryFieldWarehouse, "2:5")

			if _, err := saveMoveResult(
				context.Background(), repo, record,
				dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldWarehouse, keys, MoveResult{},
			); err != nil {
				t.Fatalf("saveMoveResult() error = %v", err)
			}
			if len(repo.deltaCalls) != 0 {
				t.Fatalf("default scope called the delta writer: %+v", repo.deltaCalls)
			}
			if len(repo.fullSaves) != 1 {
				t.Fatalf("default scope full saves = %d, want 1", len(repo.fullSaves))
			}
			wantFields := []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldWarehouse}
			if !reflect.DeepEqual(repo.fullSaves[0], wantFields) {
				t.Fatalf("full save fields = %v, want %v", repo.fullSaves[0], wantFields)
			}
			if len(*events) != 0 {
				t.Fatalf("default scope reported fallbacks: %+v", *events)
			}
		})
	}
}

// TestSaveMoveResultSlotScopeThreadsChangedKeys 钉住 slot 档：增量写**按 collection 各一次**，
// key 集合正好是本次改动的槽；跨容器移动必须两个 collection 都有调用。
func TestSaveMoveResultSlotScopeThreadsChangedKeys(t *testing.T) {
	t.Setenv(dnfrepo.InventorySaveScopeEnvName, "slot")
	events := captureInventorySaveFallbacks(t)

	for _, testCase := range []struct {
		name       string
		srcField   dnfrepo.InventoryField
		dstField   dnfrepo.InventoryField
		srcKey     string
		dstKey     string
		want       []moveDeltaCall
		wantFields []dnfrepo.InventoryField
	}{
		{
			name:     "within_bag_two_slots",
			srcField: dnfrepo.InventoryFieldSlots, dstField: dnfrepo.InventoryFieldSlots,
			srcKey: "0:9", dstKey: "0:10",
			want: []moveDeltaCall{{collection: "slots", keys: []string{"0:9", "0:10"}}},
		},
		{
			name:     "cross_container_bag_to_warehouse",
			srcField: dnfrepo.InventoryFieldSlots, dstField: dnfrepo.InventoryFieldWarehouse,
			srcKey: "0:9", dstKey: "2:5",
			want: []moveDeltaCall{
				{collection: "slots", keys: []string{"0:9"}},
				{collection: "warehouse", keys: []string{"2:5"}},
			},
		},
		{
			name:     "cross_container_warehouse_to_bag",
			srcField: dnfrepo.InventoryFieldWarehouse, dstField: dnfrepo.InventoryFieldSlots,
			srcKey: "2:0", dstKey: "0:44",
			want: []moveDeltaCall{
				{collection: "slots", keys: []string{"0:44"}},
				{collection: "warehouse", keys: []string{"2:0"}},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &recordingMoveRepository{loaded: moveTestInventoryRecord()}
			record := moveTestInventoryRecord()
			keys := newBagMoveWriteKeys(testCase.srcField, testCase.dstField)
			keys.add(testCase.srcField, testCase.srcKey)
			keys.add(testCase.dstField, testCase.dstKey)

			if _, err := saveMoveResult(
				context.Background(), repo, record, testCase.srcField, testCase.dstField, keys, MoveResult{},
			); err != nil {
				t.Fatalf("saveMoveResult() error = %v", err)
			}
			if len(repo.fullSaves) != 0 {
				t.Fatalf("slot scope still wrote the whole container: %v", repo.fullSaves)
			}
			got := append([]moveDeltaCall(nil), repo.deltaCalls...)
			for idx := range got {
				got[idx].characterID = ""
				got[idx].keys = sortedKeys(got[idx].keys)
			}
			want := append([]moveDeltaCall(nil), testCase.want...)
			for idx := range want {
				want[idx].keys = sortedKeys(want[idx].keys)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("delta calls = %+v, want %+v", got, want)
			}
			if len(*events) != 0 {
				t.Fatalf("slot scope reported fallbacks: %+v", *events)
			}
		})
	}
}

// TestSaveMoveResultFallsBackWhenKeysUnavailable 钉住"取不到 key / key 不全"这两格：
// 必须整容器写（两个字段一起写）+ 一条回退事件，且**不能**出现部分写。
func TestSaveMoveResultFallsBackWhenKeysUnavailable(t *testing.T) {
	t.Setenv(dnfrepo.InventorySaveScopeEnvName, "slot")

	for _, testCase := range []struct {
		name string
		keys *bagMoveWriteKeys
	}{
		{"nil_keys", nil},
		{"empty_collector", newBagMoveWriteKeys()},
		{"slots_only_partial", func() *bagMoveWriteKeys {
			keys := newBagMoveWriteKeys(dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldWarehouse)
			keys.add(dnfrepo.InventoryFieldSlots, "0:9")
			return keys
		}()},
		{"unknown_field", func() *bagMoveWriteKeys {
			keys := newBagMoveWriteKeys(dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldWarehouse)
			keys.add(dnfrepo.InventoryField("bags"), "0:9")
			keys.add(dnfrepo.InventoryFieldSlots, "0:9")
			keys.add(dnfrepo.InventoryFieldWarehouse, "2:5")
			return keys
		}()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			events := captureInventorySaveFallbacks(t)
			repo := &recordingMoveRepository{loaded: moveTestInventoryRecord()}
			record := moveTestInventoryRecord()
			if _, err := saveMoveResult(
				context.Background(), repo, record,
				dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldWarehouse, testCase.keys, MoveResult{},
			); err != nil {
				t.Fatalf("saveMoveResult() error = %v", err)
			}
			if len(repo.deltaCalls) != 0 {
				t.Fatalf("unavailable keys still called the delta writer: %+v", repo.deltaCalls)
			}
			if len(repo.fullSaves) != 1 {
				t.Fatalf("fallback full saves = %d, want 1 (never write nothing)", len(repo.fullSaves))
			}
			wantFields := []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldWarehouse}
			if !reflect.DeepEqual(repo.fullSaves[0], wantFields) {
				t.Fatalf("fallback fields = %v, want %v (never write half a container)", repo.fullSaves[0], wantFields)
			}
			if got := inventorySaveFallbackReasons(*events); !reflect.DeepEqual(got, []string{"inventory:keys_unavailable"}) {
				t.Fatalf("fallback events = %v, want [inventory:keys_unavailable]", got)
			}
		})
	}
}

// TestSaveMoveResultFallsBackWhenRepositoryLacksDelta 钉住类型断言这一格：
// memory / 交易作用域等实现没有增量接口 ⇒ 回退整容器写 + 事件。
func TestSaveMoveResultFallsBackWhenRepositoryLacksDelta(t *testing.T) {
	t.Setenv(dnfrepo.InventorySaveScopeEnvName, "slot")
	events := captureInventorySaveFallbacks(t)

	repo := &fullOnlyMoveRepository{loaded: moveTestInventoryRecord()}
	record := moveTestInventoryRecord()
	keys := newBagMoveWriteKeys(dnfrepo.InventoryFieldSlots)
	keys.add(dnfrepo.InventoryFieldSlots, "0:9")
	result, err := saveMoveResult(
		context.Background(), repo, record,
		dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldSlots, keys, MoveResult{},
	)
	if err != nil {
		t.Fatalf("saveMoveResult() error = %v", err)
	}
	if !result.Changed {
		t.Fatal("fallback result.Changed = false, want true")
	}
	if repo.saves != 1 {
		t.Fatalf("fallback saves = %d, want 1", repo.saves)
	}
	if got := inventorySaveFallbackReasons(*events); !reflect.DeepEqual(got, []string{"inventory:repository_does_not_support_delta"}) {
		t.Fatalf("fallback events = %v, want [inventory:repository_does_not_support_delta]", got)
	}
}

// TestSaveMoveResultUnknownScopeLiteralFallsBackToCollection 钉住开关字面量写错的口径：
// 回落今天的整容器写（安全默认）+ 一条事件。
func TestSaveMoveResultUnknownScopeLiteralFallsBackToCollection(t *testing.T) {
	t.Setenv(dnfrepo.InventorySaveScopeEnvName, "slto")
	events := captureInventorySaveFallbacks(t)

	repo := &recordingMoveRepository{loaded: moveTestInventoryRecord()}
	record := moveTestInventoryRecord()
	keys := newBagMoveWriteKeys(dnfrepo.InventoryFieldSlots)
	keys.add(dnfrepo.InventoryFieldSlots, "0:9")
	if _, err := saveMoveResult(
		context.Background(), repo, record,
		dnfrepo.InventoryFieldSlots, dnfrepo.InventoryFieldSlots, keys, MoveResult{},
	); err != nil {
		t.Fatalf("saveMoveResult() error = %v", err)
	}
	if len(repo.deltaCalls) != 0 || len(repo.fullSaves) != 1 {
		t.Fatalf("unknown literal did not fall back to collection: %+v / %d", repo.deltaCalls, len(repo.fullSaves))
	}
	want := []string{dnfrepo.InventorySaveScopeEnvName + ":unknown_scope_literal"}
	if got := inventorySaveFallbackReasons(*events); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback events = %v, want %v", got, want)
	}
}

// TestMoveThreadsExactlyChangedKeys 用真实的 Owner.Move 驱动"能 Load 的"仓储替身，
// 断言 slot 档下传下去的 key 集合正好是本次改动的槽：
//
//	互换/移动 = 源格 + 落点格；自动并入（auto_stack）= 源格 + **第三方格**（不是请求里的落点）；
//	跨容器 = 两个 collection 各一次。
func TestMoveThreadsExactlyChangedKeys(t *testing.T) {
	t.Setenv(dnfrepo.InventorySaveScopeEnvName, "slot")
	events := captureInventorySaveFallbacks(t)

	for _, testCase := range []struct {
		name     string
		cmd      Command
		wantMode string
		want     []moveDeltaCall
	}{
		{
			name:     "within_bag_swap",
			cmd:      moveTestCommand(0, 9, 0, 10, 1),
			wantMode: "swap",
			want:     []moveDeltaCall{{collection: "slots", keys: []string{"0:9", "0:10"}}},
		},
		{
			name:     "move_to_empty_slot",
			cmd:      moveTestCommand(0, 20, 0, 40, 3),
			wantMode: "move",
			want:     []moveDeltaCall{{collection: "slots", keys: []string{"0:20", "0:40"}}},
		},
		{
			name:     "auto_stack_writes_third_slot",
			cmd:      moveTestCommand(0, 12, 0, 42, 5),
			wantMode: "auto_stack",
			want:     []moveDeltaCall{{collection: "slots", keys: []string{"0:12", "0:13"}}},
		},
		{
			name:     "cross_container_bag_to_warehouse",
			cmd:      moveTestCommand(0, 9, 2, 5, 1),
			wantMode: "move",
			want: []moveDeltaCall{
				{collection: "slots", keys: []string{"0:9"}},
				{collection: "warehouse", keys: []string{"2:5"}},
			},
		},
		{
			name:     "cross_container_warehouse_to_bag",
			cmd:      moveTestCommand(2, 0, 0, 44, 1),
			wantMode: "move",
			want: []moveDeltaCall{
				{collection: "slots", keys: []string{"0:44"}},
				{collection: "warehouse", keys: []string{"2:0"}},
			},
		},
		{
			name:     "cross_container_stack_merge",
			cmd:      moveTestCommand(0, 12, 2, 1, 5),
			wantMode: "stack",
			want: []moveDeltaCall{
				{collection: "slots", keys: []string{"0:12"}},
				{collection: "warehouse", keys: []string{"2:1"}},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &recordingMoveRepository{loaded: moveTestInventoryRecord()}
			group := dnfrepo.Group{
				Inventory:        repo,
				CharacterItems:   recordingMoveUnitOfWork{inventory: repo},
				AccountInventory: moveAccountInventoryStub{},
			}
			owner, err := NewOwner(group)
			if err != nil {
				t.Fatalf("NewOwner() error = %v", err)
			}
			result, err := owner.Move(context.Background(), testCase.cmd)
			if err != nil {
				t.Fatalf("Move() error = %v", err)
			}
			if result.Mode != testCase.wantMode || !result.Changed {
				t.Fatalf("result = %+v, want mode %q changed", result, testCase.wantMode)
			}
			if len(repo.fullSaves) != 0 {
				t.Fatalf("slot scope wrote the whole container: %v", repo.fullSaves)
			}
			got := append([]moveDeltaCall(nil), repo.deltaCalls...)
			for idx := range got {
				got[idx].characterID = ""
				got[idx].keys = sortedKeys(got[idx].keys)
			}
			want := append([]moveDeltaCall(nil), testCase.want...)
			for idx := range want {
				want[idx].keys = sortedKeys(want[idx].keys)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("delta calls = %+v, want %+v", got, want)
			}
			if len(*events) != 0 {
				t.Fatalf("move reported fallbacks: %+v", *events)
			}
		})
	}
}

func moveTestCommand(srcList byte, srcSlot int16, dstList byte, dstSlot int16, moveCount int32) Command {
	return Command{
		Operation:            "move-item-space",
		AccountID:            "acct-77",
		SelectedCharacterID:  77,
		SourceListType:       srcList,
		SourceSlotIndex:      srcSlot,
		MoveCount:            moveCount,
		DestinationListType:  dstList,
		DestinationSlotIndex: dstSlot,
	}
}
