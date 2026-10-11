package collectbox

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场缺陷：「收集箱直接无法交互 无法放置宝珠」。本文件的用例钉住"放进去"这
// 一步的持久化语义：宝珠从背包扣掉、登记进箱子、同一次事务落库、重登后还在。
const (
	testCharacterID uint16 = 19
	testBoxIndex    int32  = 2
	testBeadItemID  int32  = 490007143
	testBagSlot     int16  = 30
)

func testPolicy(expiresAt time.Time, allowed ...int32) Policy {
	return Policy{
		BoxIndex:       testBoxIndex,
		SlotCount:      9,
		AllowedItemIDs: allowed,
		ExpiresAt:      expiresAt,
		HasExpiry:      true,
	}
}

func newCollectboxTestGroup(t *testing.T, slots map[string]dnfrepo.ItemStack) dnfrepo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       make(map[string]int64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       slots,
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func loadTestSlots(t *testing.T, repositories dnfrepo.Group) map[string]dnfrepo.ItemStack {
	t.Helper()
	record, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return record.Slots
}

// TestPutMovesBeadFromBagIntoBoxAndPersists 是本缺陷由红转绿的主用例：
// 一次 Put 之后，宝珠必须同时满足 ①背包里没了 ②箱子里有了 ③再读一次库还在。
func TestPutMovesBeadFromBagIntoBoxAndPersists(t *testing.T) {
	repositories := newCollectboxTestGroup(t, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(testBeadItemID), Count: 1},
	})
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID,
		SourceList:          dnfrepo.MainInventoryListType,
		SourceSlot:          testBagSlot,
		ItemID:              testBeadItemID,
	}, testPolicy(time.Now().Add(24*time.Hour), testBeadItemID, 490007144))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if result.BoxIndex != testBoxIndex || result.SlotIndex != 0 || !result.SourceEmptied || result.SourceRemaining != 0 {
		t.Fatalf("result = %+v", result)
	}

	// 重登 = 重新从仓储读一次，而不是复用内存里的 map。
	slots := loadTestSlots(t, repositories)
	if stack, still := slots["0:30"]; still {
		t.Fatalf("bag slot 30 must be emptied, got %+v", stack)
	}
	registered := slots[RegisteredSlotKey(testBoxIndex, 0)]
	if registered.ItemID != int64(testBeadItemID) || registered.Count != 1 {
		t.Fatalf("registered stack = %+v", registered)
	}
	if registered.Extra["collectbox_box_index"] != "2" || registered.Extra["collectbox_slot_index"] != "0" {
		t.Fatalf("registered extras = %+v", registered.Extra)
	}
	ids := RegisteredItemIDs(dnfrepo.InventoryRecord{Slots: slots}, testBoxIndex)
	if len(ids) != 1 || ids[0] != testBeadItemID {
		t.Fatalf("RegisteredItemIDs = %v want [%d]", ids, testBeadItemID)
	}
	// 别的箱子不受影响。
	if other := RegisteredItemIDs(dnfrepo.InventoryRecord{Slots: slots}, 3); len(other) != 0 {
		t.Fatalf("box 3 must stay empty, got %v", other)
	}
}

// TestPutKeepsRemainingStackInBag 钉住堆叠物品只扣一个。
func TestPutKeepsRemainingStackInBag(t *testing.T) {
	repositories := newCollectboxTestGroup(t, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(testBeadItemID), Count: 5},
	})
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID,
		SourceSlot:          testBagSlot,
		ItemID:              testBeadItemID,
	}, testPolicy(time.Now().Add(time.Hour), testBeadItemID))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if result.SourceEmptied || result.SourceRemaining != 4 {
		t.Fatalf("result = %+v want remaining=4", result)
	}
	slots := loadTestSlots(t, repositories)
	if slots["0:30"].Count != 4 {
		t.Fatalf("bag stack = %+v want count 4", slots["0:30"])
	}
	if slots[RegisteredSlotKey(testBoxIndex, 0)].Count != 1 {
		t.Fatalf("registered stack must be a single item, got %+v", slots[RegisteredSlotKey(testBoxIndex, 0)])
	}
}

// TestPutRejectsItemOutsidePVFBoxSlots 钉住"只收 PVF 槛位上的宝珠"：
// 任何不在 [Item Position Info] 里的 id 都必须在写库之前被拒。
func TestPutRejectsItemOutsidePVFBoxSlots(t *testing.T) {
	repositories := newCollectboxTestGroup(t, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: 999999, Count: 1},
	})
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID,
		SourceSlot:          testBagSlot,
		ItemID:              999999,
	}, testPolicy(time.Now().Add(time.Hour), testBeadItemID))
	if !errors.Is(err, ErrItemNotAllowed) {
		t.Fatalf("err = %v want ErrItemNotAllowed", err)
	}
	if slots := loadTestSlots(t, repositories); slots["0:30"].ItemID != 999999 {
		t.Fatalf("rejected put must leave the bag untouched, got %+v", slots["0:30"])
	}
}

// TestPutRejectsMismatchedSourceSlot 钉住来源校验：槽位上的物品必须就是请求里的 id。
func TestPutRejectsMismatchedSourceSlot(t *testing.T) {
	repositories := newCollectboxTestGroup(t, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(testBeadItemID), Count: 1},
	})
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID,
		SourceSlot:          31,
		ItemID:              testBeadItemID,
	}, testPolicy(time.Now().Add(time.Hour), testBeadItemID))
	if !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("err = %v want ErrSourceNotFound", err)
	}
}

// TestPutRejectsExpiredBox 钉住过期箱不可登记（与 op1021 的 selectable 口径一致）。
func TestPutRejectsExpiredBox(t *testing.T) {
	repositories := newCollectboxTestGroup(t, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(testBeadItemID), Count: 1},
	})
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID,
		SourceSlot:          testBagSlot,
		ItemID:              testBeadItemID,
	}, testPolicy(time.Now().Add(-time.Hour), testBeadItemID))
	if !errors.Is(err, ErrBoxExpired) {
		t.Fatalf("err = %v want ErrBoxExpired", err)
	}
}

// TestPutRejectsLockedItem 钉住锁定的宝珠不能被搬进箱子。
func TestPutRejectsLockedItem(t *testing.T) {
	repositories := newCollectboxTestGroup(t, map[string]dnfrepo.ItemStack{
		"0:30": {
			ItemID: int64(testBeadItemID),
			Count:  1,
			Extra:  map[string]string{"equipment_lock_state": "locked"},
		},
	})
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID,
		SourceSlot:          testBagSlot,
		ItemID:              testBeadItemID,
	}, testPolicy(time.Now().Add(time.Hour), testBeadItemID))
	if !errors.Is(err, ErrSourceLocked) {
		t.Fatalf("err = %v want ErrSourceLocked", err)
	}
}

// TestPutFillsBoxThenReportsFull 钉住箱满拒绝，且已登记的条目保持原样。
func TestPutFillsBoxThenReportsFull(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{"0:30": {ItemID: int64(testBeadItemID), Count: 1}}
	// SlotCount=1 的箱子，第一个放进去，第二个必须满。
	policy := Policy{BoxIndex: testBoxIndex, SlotCount: 1, AllowedItemIDs: []int32{testBeadItemID}}
	repositories := newCollectboxTestGroup(t, slots)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID, SourceSlot: testBagSlot, ItemID: testBeadItemID,
	}, policy); err != nil {
		t.Fatalf("first put: %v", err)
	}
	// 再放一颗（先补进背包）。
	refilled := loadTestSlots(t, repositories)
	refilled["0:31"] = dnfrepo.ItemStack{ItemID: int64(testBeadItemID), Count: 1}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19", Slots: refilled,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = owner.Put(context.Background(), PutCommand{
		SelectedCharacterID: testCharacterID, SourceSlot: 31, ItemID: testBeadItemID,
	}, policy)
	if !errors.Is(err, ErrBoxFull) {
		t.Fatalf("err = %v want ErrBoxFull", err)
	}
}

// TestRegisteredSlotKeyRoundTrips 钉住伪容器槽位编码可逆（box/slot 不能串味）。
func TestRegisteredSlotKeyRoundTrips(t *testing.T) {
	for _, boxIndex := range []int32{1, 2, 5, 127} {
		for _, slotIndex := range []int32{0, 1, 8, 255} {
			key := RegisteredSlotKey(boxIndex, slotIndex)
			gotBox, gotSlot, ok := parseRegisteredSlotKey(key)
			if !ok || gotBox != boxIndex || gotSlot != slotIndex {
				t.Fatalf("%s -> box=%d slot=%d ok=%t want box=%d slot=%d",
					key, gotBox, gotSlot, ok, boxIndex, slotIndex)
			}
		}
	}
	// 主背包 / 称号簿的键绝不能被当成收集箱条目。
	for _, key := range []string{"0:30", "100:0", "101", "101:", "1:5"} {
		if _, _, ok := parseRegisteredSlotKey(key); ok {
			t.Fatalf("key %q must not parse as a collectbox slot", key)
		}
	}
}

// TestRegisteredItemIDsOrdersBySlotIndex 钉住下发顺序按槛位号升序（客户端按顺序铺格）。
func TestRegisteredItemIDsOrdersBySlotIndex(t *testing.T) {
	record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{
		RegisteredSlotKey(testBoxIndex, 4): {ItemID: 444},
		RegisteredSlotKey(testBoxIndex, 0): {ItemID: 111},
		RegisteredSlotKey(testBoxIndex, 2): {ItemID: 333},
		"0:9":                              {ItemID: 999},
	}}
	ids := RegisteredItemIDs(record, testBoxIndex)
	want := []int32{111, 333, 444}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v want %v", ids, want)
	}
	for idx := range want {
		if ids[idx] != want[idx] {
			t.Fatalf("ids = %v want %v", ids, want)
		}
	}
	grouped := RegisteredItemIDsByBox(record)
	if len(grouped[testBoxIndex]) != 3 {
		t.Fatalf("RegisteredItemIDsByBox = %+v", grouped)
	}
}

// TestNewOwnerRequiresSettlementUnitOfWork 钉住 owner 必须拿到事务边界。
func TestNewOwnerRequiresSettlementUnitOfWork(t *testing.T) {
	if _, err := NewOwner(dnfrepo.Group{}); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("err = %v want ErrOwnerUnavailable", err)
	}
}
