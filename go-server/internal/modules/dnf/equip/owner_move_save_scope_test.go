// 本文件是 O-B 在域层（equip）这一侧的判据：
//
//  1. 开关默认档必须逐字节等于今天（走 dnfrepo.SaveInventoryFields /
//     SaveEquipmentFields 的整容器路）；
//  2. slot/entry 档必须把**本次改动的 key 集合**交给增量写接口；
//  3. 任何"取不到 key / 仓储不支持增量"的情况必须**回退整容器写并记一条事件**
//     —— 静默不写是数据丢失，是这项改动唯一不可接受的失败形状。
package equip

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// recordingDeltaRepository 同时实现 dnfrepo 接口与增量写接口，并记录每次调用。
type recordingDeltaRepository struct {
	mu sync.Mutex

	fullInventorySaves []dnfrepo.InventoryRecord
	fullEquipmentSaves []dnfrepo.EquipmentRecord
	slotDeltaCalls     []slotDeltaCall
	entryDeltaCalls    [][]string
	failSlotDelta      error
}

type slotDeltaCall struct {
	characterID string
	collection  string
	keys        []string
}

func (r *recordingDeltaRepository) Load(context.Context, string) (dnfrepo.InventoryRecord, bool, error) {
	return dnfrepo.InventoryRecord{}, false, nil
}

func (r *recordingDeltaRepository) Save(_ context.Context, record dnfrepo.InventoryRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fullInventorySaves = append(r.fullInventorySaves, dnfrepo.CloneInventory(record))
	return nil
}

func (r *recordingDeltaRepository) SaveFields(ctx context.Context, record dnfrepo.InventoryRecord, _ ...dnfrepo.InventoryField) error {
	return r.Save(ctx, record)
}

func (r *recordingDeltaRepository) SaveItemStackSlotDelta(
	_ context.Context,
	record dnfrepo.InventoryRecord,
	collection string,
	keys []string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slotDeltaCalls = append(r.slotDeltaCalls, slotDeltaCall{
		characterID: record.CharacterID,
		collection:  collection,
		keys:        append([]string(nil), keys...),
	})
	return r.failSlotDelta
}

type recordingDeltaEquipment struct {
	mu sync.Mutex

	fullSaves  []dnfrepo.EquipmentRecord
	entryCalls [][]string
}

func (r *recordingDeltaEquipment) Load(context.Context, string) (dnfrepo.EquipmentRecord, bool, error) {
	return dnfrepo.EquipmentRecord{}, false, nil
}

func (r *recordingDeltaEquipment) Save(_ context.Context, record dnfrepo.EquipmentRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fullSaves = append(r.fullSaves, dnfrepo.CloneEquipment(record))
	return nil
}

func (r *recordingDeltaEquipment) SaveFields(ctx context.Context, record dnfrepo.EquipmentRecord, _ ...dnfrepo.EquipmentField) error {
	return r.Save(ctx, record)
}

func (r *recordingDeltaEquipment) SaveEquipmentEntryDelta(
	_ context.Context,
	_ dnfrepo.EquipmentRecord,
	keys []string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entryCalls = append(r.entryCalls, append([]string(nil), keys...))
	return nil
}

// fullOnlyInventory 只实现 dnfrepo 接口（= memory / trade-scoped / rental-scoped /
// settlement-scoped 的形状）：O-B 的类型断言必须失败并回退整容器写。
type fullOnlyInventory struct {
	saves int
	last  dnfrepo.InventoryRecord
}

func (r *fullOnlyInventory) Load(context.Context, string) (dnfrepo.InventoryRecord, bool, error) {
	return dnfrepo.InventoryRecord{}, false, nil
}

func (r *fullOnlyInventory) Save(_ context.Context, record dnfrepo.InventoryRecord) error {
	r.saves++
	r.last = dnfrepo.CloneInventory(record)
	return nil
}

type fullOnlyEquipment struct {
	saves int
	last  dnfrepo.EquipmentRecord
}

func (r *fullOnlyEquipment) Load(context.Context, string) (dnfrepo.EquipmentRecord, bool, error) {
	return dnfrepo.EquipmentRecord{}, false, nil
}

func (r *fullOnlyEquipment) Save(_ context.Context, record dnfrepo.EquipmentRecord) error {
	r.saves++
	r.last = dnfrepo.CloneEquipment(record)
	return nil
}

func captureMoveSaveFallbacks(t *testing.T) *[]MoveSaveFallbackEvent {
	t.Helper()
	events := &[]MoveSaveFallbackEvent{}
	var mu sync.Mutex
	SetMoveSaveFallbackObserver(func(event MoveSaveFallbackEvent) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, event)
	})
	t.Cleanup(func() { SetMoveSaveFallbackObserver(nil) })
	return events
}

func moveSaveFallbackReasons(events []MoveSaveFallbackEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Target+":"+event.Reason)
	}
	sort.Strings(out)
	return out
}

func testMoveOwner(inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) *Owner {
	return &Owner{
		inventory:          inventory,
		equipment:          equipment,
		placementValidator: PlacementValidatorFunc(func(context.Context, Placement) error { return nil }),
		inItemTransaction:  true,
	}
}

func testMoveRecords() (dnfrepo.InventoryRecord, dnfrepo.EquipmentRecord) {
	raw := make([]byte, 0x38)
	for idx := range raw {
		raw[idx] = byte(idx)
	}
	inventory := dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:20": {ItemID: 100020, Count: 1, RawEntry: raw, Extra: map[string]string{"a": "1"}},
			"0:21": {ItemID: 100021, Count: 1, RawEntry: raw},
		},
	}
	equipment := dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 200012, RawEntry: raw},
			"14": {SlotIndex: 14, ItemID: 200014, RawEntry: raw},
		},
	}
	return inventory, equipment
}

// TestSaveMoveDefaultsToCollectionScope 钉住默认档：一个增量接口都不调。
func TestSaveMoveDefaultsToCollectionScope(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "")
	t.Setenv(equipmentSaveScopeEnv, "")
	events := captureMoveSaveFallbacks(t)

	inventory := &recordingDeltaRepository{}
	equipment := &recordingDeltaEquipment{}
	owner := testMoveOwner(inventory, equipment)
	records, equipmentRecords := testMoveRecords()
	if _, err := owner.saveMove(
		context.Background(), records, equipmentRecords, []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots},
		moveWriteKeys{inventory: []string{"0:20"}, equipment: []string{"12"}}, MoveResult{},
	); err != nil {
		t.Fatalf("saveMove() error = %v", err)
	}
	if len(inventory.slotDeltaCalls) != 0 || len(equipment.entryCalls) != 0 {
		t.Fatalf("default scope called the delta writers: %+v / %+v", inventory.slotDeltaCalls, equipment.entryCalls)
	}
	if len(inventory.fullInventorySaves) != 1 || len(equipment.fullSaves) != 1 {
		t.Fatalf("default scope full saves = %d / %d, want 1 / 1", len(inventory.fullInventorySaves), len(equipment.fullSaves))
	}
	if len(*events) != 0 {
		t.Fatalf("default scope reported fallbacks: %+v", *events)
	}
}

// TestSaveMoveSlotScopeThreadsChangedKeys 钉住 slot/entry 档把改动 key 原样传下去，
// 并且**不再**发生整容器写。
func TestSaveMoveSlotScopeThreadsChangedKeys(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "slot")
	t.Setenv(equipmentSaveScopeEnv, "entry")
	events := captureMoveSaveFallbacks(t)

	inventory := &recordingDeltaRepository{}
	equipment := &recordingDeltaEquipment{}
	owner := testMoveOwner(inventory, equipment)
	records, equipmentRecords := testMoveRecords()
	if _, err := owner.saveMove(
		context.Background(), records, equipmentRecords, []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots},
		moveWriteKeys{inventory: []string{"0:20"}, equipment: []string{"12"}}, MoveResult{},
	); err != nil {
		t.Fatalf("saveMove() error = %v", err)
	}
	if len(inventory.slotDeltaCalls) != 1 {
		t.Fatalf("slot delta calls = %+v, want exactly one", inventory.slotDeltaCalls)
	}
	if got := inventory.slotDeltaCalls[0]; got.collection != "slots" || !reflect.DeepEqual(got.keys, []string{"0:20"}) {
		t.Fatalf("slot delta call = %+v, want slots/[\"0:20\"]", got)
	}
	if len(equipment.entryCalls) != 1 || !reflect.DeepEqual(equipment.entryCalls[0], []string{"12"}) {
		t.Fatalf("entry delta calls = %+v, want [[12]]", equipment.entryCalls)
	}
	if len(inventory.fullInventorySaves) != 0 || len(equipment.fullSaves) != 0 {
		t.Fatalf("slot scope still wrote the whole container: %d / %d", len(inventory.fullInventorySaves), len(equipment.fullSaves))
	}
	if len(*events) != 0 {
		t.Fatalf("slot scope reported fallbacks: %+v", *events)
	}
}

// TestSaveMoveWarehouseScopeUsesWarehouseCollection 钉住个人仓库（list 2）走的是
// warehouse collection，而不是 slots。
func TestSaveMoveWarehouseScopeUsesWarehouseCollection(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "slot")
	t.Setenv(equipmentSaveScopeEnv, "entry")

	inventory := &recordingDeltaRepository{}
	owner := testMoveOwner(inventory, &recordingDeltaEquipment{})
	records, equipmentRecords := testMoveRecords()
	records.Warehouse = map[string]dnfrepo.ItemStack{"2:0": {ItemID: 400001, Count: 1}}
	if _, err := owner.saveMove(
		context.Background(), records, equipmentRecords, []dnfrepo.InventoryField{dnfrepo.InventoryFieldWarehouse},
		moveWriteKeys{inventory: []string{"2:0"}, equipment: []string{"12"}}, MoveResult{},
	); err != nil {
		t.Fatalf("saveMove() error = %v", err)
	}
	if len(inventory.slotDeltaCalls) != 1 || inventory.slotDeltaCalls[0].collection != "warehouse" {
		t.Fatalf("warehouse move delta calls = %+v, want collection warehouse", inventory.slotDeltaCalls)
	}
}

// TestSaveMoveFallsBackWhenKeysUnavailable 钉住"取不到 key 集合"这一格：
// 必须整容器写 + 一条回退事件，且**不能**出现"一条 SQL 都不发"。
func TestSaveMoveFallsBackWhenKeysUnavailable(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "slot")
	t.Setenv(equipmentSaveScopeEnv, "entry")
	events := captureMoveSaveFallbacks(t)

	inventory := &recordingDeltaRepository{}
	equipment := &recordingDeltaEquipment{}
	owner := testMoveOwner(inventory, equipment)
	records, equipmentRecords := testMoveRecords()
	if _, err := owner.saveMove(
		context.Background(), records, equipmentRecords, []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots},
		moveWriteKeys{}, MoveResult{},
	); err != nil {
		t.Fatalf("saveMove() error = %v", err)
	}
	if len(inventory.slotDeltaCalls) != 0 || len(equipment.entryCalls) != 0 {
		t.Fatalf("unavailable keys still called the delta writers: %+v / %+v", inventory.slotDeltaCalls, equipment.entryCalls)
	}
	if len(inventory.fullInventorySaves) != 1 || len(equipment.fullSaves) != 1 {
		t.Fatalf("fallback full saves = %d / %d, want 1 / 1 (never write nothing)", len(inventory.fullInventorySaves), len(equipment.fullSaves))
	}
	want := []string{"equipment:keys_unavailable", "inventory:keys_unavailable"}
	if got := moveSaveFallbackReasons(*events); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback events = %v, want %v", got, want)
	}
}

// TestSaveMoveFallsBackWhenRepositoryLacksDelta 钉住类型断言这一格：
// memory / trade-scoped / 结算作用域等实现没有增量接口 ⇒ 回退整容器写 + 事件。
func TestSaveMoveFallsBackWhenRepositoryLacksDelta(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "slot")
	t.Setenv(equipmentSaveScopeEnv, "entry")
	events := captureMoveSaveFallbacks(t)

	inventory := &fullOnlyInventory{}
	equipment := &fullOnlyEquipment{}
	owner := testMoveOwner(inventory, equipment)
	records, equipmentRecords := testMoveRecords()
	result, err := owner.saveMove(
		context.Background(), records, equipmentRecords, []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots},
		moveWriteKeys{inventory: []string{"0:20"}, equipment: []string{"12"}}, MoveResult{},
	)
	if err != nil {
		t.Fatalf("saveMove() error = %v", err)
	}
	if inventory.saves != 1 || equipment.saves != 1 {
		t.Fatalf("fallback saves = %d / %d, want 1 / 1", inventory.saves, equipment.saves)
	}
	if !result.Changed {
		t.Fatal("fallback result.Changed = false, want true")
	}
	want := []string{"equipment:repository_does_not_support_delta", "inventory:repository_does_not_support_delta"}
	if got := moveSaveFallbackReasons(*events); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback events = %v, want %v", got, want)
	}
	if !strings.Contains(inventory.last.Slots["0:20"].Extra["a"], "1") {
		t.Fatalf("fallback did not write the full record: %+v", inventory.last.Slots)
	}
}

// TestSaveMoveUnknownScopeLiteralFallsBackToCollection 钉住开关字面量写错时的口径：
// 回落今天的整容器写（安全默认）+ 一条事件。
func TestSaveMoveUnknownScopeLiteralFallsBackToCollection(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "slto")
	t.Setenv(equipmentSaveScopeEnv, "entrie")
	events := captureMoveSaveFallbacks(t)

	inventory := &recordingDeltaRepository{}
	owner := testMoveOwner(inventory, &recordingDeltaEquipment{})
	records, equipmentRecords := testMoveRecords()
	if _, err := owner.saveMove(
		context.Background(), records, equipmentRecords, []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots},
		moveWriteKeys{inventory: []string{"0:20"}, equipment: []string{"12"}}, MoveResult{},
	); err != nil {
		t.Fatalf("saveMove() error = %v", err)
	}
	if len(inventory.slotDeltaCalls) != 0 || len(inventory.fullInventorySaves) != 1 {
		t.Fatalf("unknown literal did not fall back to collection: %+v / %d", inventory.slotDeltaCalls, len(inventory.fullInventorySaves))
	}
	want := []string{equipmentSaveScopeEnv + ":unknown_scope_literal", inventorySaveScopeEnv + ":unknown_scope_literal"}
	if got := moveSaveFallbackReasons(*events); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback events = %v, want %v", got, want)
	}
}

// threadFixture 是"能返回记录、并能记录增量调用"的仓储状态。
type threadFixture struct {
	mu            sync.Mutex
	inventory     dnfrepo.InventoryRecord
	equipment     dnfrepo.EquipmentRecord
	slotCalls     []slotDeltaCall
	entryCalls    [][]string
	inventoryFull int
	equipmentFull int
}

type threadInventoryRepo struct{ fixture *threadFixture }

func (r *threadInventoryRepo) Load(context.Context, string) (dnfrepo.InventoryRecord, bool, error) {
	return dnfrepo.CloneInventory(r.fixture.inventory), true, nil
}

func (r *threadInventoryRepo) Save(_ context.Context, record dnfrepo.InventoryRecord) error {
	r.fixture.inventoryFull++
	r.fixture.inventory = dnfrepo.CloneInventory(record)
	return nil
}

func (r *threadInventoryRepo) SaveFields(ctx context.Context, record dnfrepo.InventoryRecord, _ ...dnfrepo.InventoryField) error {
	return r.Save(ctx, record)
}

func (r *threadInventoryRepo) SaveItemStackSlotDelta(
	_ context.Context,
	record dnfrepo.InventoryRecord,
	collection string,
	keys []string,
) error {
	r.fixture.slotCalls = append(r.fixture.slotCalls, slotDeltaCall{
		characterID: record.CharacterID,
		collection:  collection,
		keys:        append([]string(nil), keys...),
	})
	r.fixture.inventory = dnfrepo.CloneInventory(record)
	return nil
}

type threadEquipmentRepo struct{ fixture *threadFixture }

func (r *threadEquipmentRepo) Load(context.Context, string) (dnfrepo.EquipmentRecord, bool, error) {
	return dnfrepo.CloneEquipment(r.fixture.equipment), true, nil
}

func (r *threadEquipmentRepo) Save(_ context.Context, record dnfrepo.EquipmentRecord) error {
	r.fixture.equipmentFull++
	r.fixture.equipment = dnfrepo.CloneEquipment(record)
	return nil
}

func (r *threadEquipmentRepo) SaveFields(ctx context.Context, record dnfrepo.EquipmentRecord, _ ...dnfrepo.EquipmentField) error {
	return r.Save(ctx, record)
}

func (r *threadEquipmentRepo) SaveEquipmentEntryDelta(
	_ context.Context,
	record dnfrepo.EquipmentRecord,
	keys []string,
) error {
	r.fixture.entryCalls = append(r.fixture.entryCalls, append([]string(nil), keys...))
	r.fixture.equipment = dnfrepo.CloneEquipment(record)
	return nil
}

// TestEquipAndUnequipThreadExactlyTwoChangedKeys 用真实的 Owner.equip / Owner.unequip
// 驱动"能 Load 的"仓储替身，断言传下去的 key 集合就是"背包端点 + 穿戴端点"两格：
// 穿上 = srcKey/dstKey；脱下 = 改后落点 dstKey/srcKey。
func TestEquipAndUnequipThreadExactlyTwoChangedKeys(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "slot")
	t.Setenv(equipmentSaveScopeEnv, "entry")

	raw := testRawEntry()
	for _, testCase := range []struct {
		name             string
		drive            func(*Owner, context.Context, MoveCommand) (MoveResult, error)
		cmd              MoveCommand
		wantInventoryKey []string
		wantEquipmentKey []string
		wantCollection   string
		wantMode         string
	}{
		{
			"equip",
			func(o *Owner, ctx context.Context, cmd MoveCommand) (MoveResult, error) { return o.equip(ctx, cmd) },
			MoveCommand{
				SelectedCharacterID: 77,
				SourceListType:      0, SourceSlotIndex: 20, SourceInstanceValue: 100020,
				MoveCount:           1,
				DestinationListType: 3, DestinationSlotIndex: 22, DestinationInstanceValue: 0,
			},
			[]string{"0:20"}, []string{"22"}, "slots", "equip",
		},
		{
			"unequip_relocated_slot",
			func(o *Owner, ctx context.Context, cmd MoveCommand) (MoveResult, error) { return o.unequip(ctx, cmd) },
			MoveCommand{
				SelectedCharacterID: 77,
				SourceListType:      3, SourceSlotIndex: 12, SourceInstanceValue: 200012,
				MoveCount:           1,
				DestinationListType: 0, DestinationSlotIndex: 20, DestinationInstanceValue: 0,
			},
			// 0:20 与 0:21 都被占 ⇒ 落点改到 0:22；delta 必须是改后的落点。
			[]string{"0:22"}, []string{"12"}, "slots", "unequip",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := &threadFixture{
				inventory: dnfrepo.InventoryRecord{
					CharacterID: "77",
					Slots: map[string]dnfrepo.ItemStack{
						"0:20": {ItemID: 100020, Count: 1, RawEntry: raw},
						"0:21": {ItemID: 100021, Count: 1, RawEntry: raw},
					},
				},
				equipment: dnfrepo.EquipmentRecord{
					CharacterID: "77",
					Entries: map[string]dnfrepo.EquipmentEntry{
						"12": {SlotIndex: 12, ItemID: 200012, RawEntry: raw},
					},
				},
			}
			owner := testMoveOwner(&threadInventoryRepo{fixture: fixture}, &threadEquipmentRepo{fixture: fixture})
			result, err := testCase.drive(owner, context.Background(), testCase.cmd)
			if err != nil {
				t.Fatalf("%s error = %v", testCase.name, err)
			}
			if result.Mode != testCase.wantMode {
				t.Fatalf("Mode = %q, want %q", result.Mode, testCase.wantMode)
			}
			if len(fixture.slotCalls) != 1 {
				t.Fatalf("slot delta calls = %+v, want one", fixture.slotCalls)
			}
			if got := fixture.slotCalls[0]; got.collection != testCase.wantCollection || !reflect.DeepEqual(got.keys, testCase.wantInventoryKey) {
				t.Fatalf("slot delta call = %+v, want %s/%v", got, testCase.wantCollection, testCase.wantInventoryKey)
			}
			if len(fixture.entryCalls) != 1 || !reflect.DeepEqual(fixture.entryCalls[0], testCase.wantEquipmentKey) {
				t.Fatalf("entry delta calls = %+v, want [%v]", fixture.entryCalls, testCase.wantEquipmentKey)
			}
			if fixture.inventoryFull != 0 || fixture.equipmentFull != 0 {
				t.Fatalf("slot/entry scope still did a full write: %d / %d", fixture.inventoryFull, fixture.equipmentFull)
			}
		})
	}
}

// TestEquipMoveThreadsChangedKeysOnMemoryRepository 在真仓储（memory）上跑完整
// Owner.Move，断言 slot/entry 档下不会退化成"整容器写"以外的东西：
// memory 不支持增量接口 ⇒ 每一步都必须走整容器写并记一条回退事件。
func TestEquipMoveThreadsChangedKeysOnMemoryRepository(t *testing.T) {
	t.Setenv(inventorySaveScopeEnv, "slot")
	t.Setenv(equipmentSaveScopeEnv, "entry")
	events := captureMoveSaveFallbacks(t)

	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:20": {ItemID: 100020, Count: 1, RawEntry: testRawEntry()},
		},
	})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 200012, RawEntry: testRawEntry()},
		},
	})
	owner, err := NewOwnerWithPlacementValidator(repos, PlacementValidatorFunc(func(context.Context, Placement) error { return nil }))
	if err != nil {
		t.Fatalf("NewOwnerWithPlacementValidator() error = %v", err)
	}
	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      77,
		SourceListType:           0,
		SourceSlotIndex:          20,
		SourceInstanceValue:      100020,
		MoveCount:                1,
		DestinationListType:      3,
		DestinationSlotIndex:     22,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if result.Mode != "equip" || !result.Changed {
		t.Fatalf("result = %+v, want equip/changed", result)
	}
	loaded := loadTestEquipment(t, ctx, repos, "77")
	if loaded.Entries["22"].ItemID != 100020 {
		t.Fatalf("worn slot 22 = %+v, want item 100020", loaded.Entries["22"])
	}
	if got := loadTestInventory(t, ctx, repos, "77").Slots; len(got) != 0 {
		t.Fatalf("bag slots = %+v, want the moved item removed", got)
	}
	if len(*events) != 2 {
		t.Fatalf("fallback events = %+v, want one per repository", *events)
	}
	for _, event := range *events {
		if event.Reason != "repository_does_not_support_delta" {
			t.Fatalf("fallback reason = %q, want repository_does_not_support_delta", event.Reason)
		}
		if !strings.Contains(event.Repo, "memory") {
			t.Fatalf("fallback repo = %q, want the memory implementation", event.Repo)
		}
	}
}

func testRawEntry() []byte {
	raw := make([]byte, 0x38)
	for idx := range raw {
		raw[idx] = byte(idx + 1)
	}
	return raw
}
