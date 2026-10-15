package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// DOC-067 物品栏位置锁定回归。
//
// 旧代码（基线 5fc6807）里 713/714 没有 handler、Owner.Sort 也不认位置锁：
// 客户端点锁定后服务端既不落库也不回包，整理照旧把物品搬走。
// 本文件的用例在旧代码上全部 RED（见 Docs/D33_SORT_LOCK_20261014.md 的 RED→GREEN 一节）。

func sortLockBody(listType byte, slot int16) []byte {
	body := make([]byte, 3)
	body[0] = listType
	binary.LittleEndian.PutUint16(body[1:3], uint16(slot))
	return body
}

func sortLockStack(itemID int64, extra map[string]string) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{ItemID: itemID, Count: 1, Extra: extra}
}

// newSortLockFixture 建一条角色 + 主背包 20（装备页）/ 21 与 4（快捷槽）的物品。
func newSortLockFixture(t *testing.T) (context.Context, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc"})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 20): sortLockStack(1001, map[string]string{"item_kind": "equipment", "pvf_path": "equipment/x.equ"}),
			slotKey(listTypeMain, 4):  sortLockStack(1002, map[string]string{"item_kind": "stackable"}),
			// 无物品行（ItemID=0 但 Count>0）：必须按空槽拒绝，不能被放行。
			slotKey(listTypeMain, 30): {ItemID: 0, Count: 5, Extra: map[string]string{"item_kind": "stackable"}},
			// 永久装扮：真实占格但 Count==0，必须允许锁定。
			slotKey(listTypeAvatar, 30): {ItemID: 2002, Count: 0, Extra: map[string]string{"item_kind": "avatar"}},
		},
	})
	return ctx, repos
}

func handleSortLock(t *testing.T, ctx context.Context, repos dnfrepo.Group, opcode uint16, listType byte, slot int16) alignedcmd.Result {
	t.Helper()
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              opcode,
		Body:                sortLockBody(listType, slot),
		AccountID:           "acc",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	return result
}

func storedSortLock(t *testing.T, ctx context.Context, repos dnfrepo.Group, listType byte, slot int16) (dnfrepo.ItemStack, bool) {
	t.Helper()
	record := loadTestInventory(t, ctx, repos, "77")
	items, _ := itemMapForList(&record, listType)
	stack, ok := items[slotKey(listType, slot)]
	return stack, ok
}

func TestSortLockSetAndClearPersistExplicitState(t *testing.T) {
	ctx, repos := newSortLockFixture(t)

	lock := handleSortLock(t, ctx, repos, uint16(dnfenum.CmdPacketRequestItemSortLock), listTypeMain, 20)
	// 713 回包 = class1 ACK + class0/0x000D 整容器刷新（见 sort_lock_refresh_test.go）。
	if !lock.Handled || !lock.ResponseAllowed || len(lock.UpperResponses) != 2 {
		t.Fatalf("lock result = %+v", lock)
	}
	if refresh := lock.UpperResponses[1]; refresh.MsgID != msgItemListRefresh || refresh.Classification != 0 {
		t.Fatalf("lock refresh envelope = %+v", refresh)
	}
	ack := lock.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketRequestItemSortLock) || ack.Classification != 1 {
		t.Fatalf("lock ack envelope = %+v", ack)
	}
	// 713 成功体 = u8 1 + u8 list + u16 slot（不是 toggle）。
	if want := []byte{1, listTypeMain, 20, 0}; string(ack.Body) != string(want) {
		t.Fatalf("lock ack body = %v want %v", ack.Body, want)
	}
	stack, ok := storedSortLock(t, ctx, repos, listTypeMain, 20)
	if !ok || !IsSortLocked(stack) {
		t.Fatalf("lock not persisted: %+v", stack)
	}
	if stack.Extra["item_kind"] != "equipment" || stack.Extra["pvf_path"] != "equipment/x.equ" {
		t.Fatalf("lock write clobbered other extra keys: %+v", stack.Extra)
	}

	unlock := handleSortLock(t, ctx, repos, uint16(dnfenum.CmdPacketRequestItemSortUnlock), listTypeMain, 20)
	if !unlock.ResponseAllowed || len(unlock.UpperResponses) != 2 {
		t.Fatalf("unlock result = %+v", unlock)
	}
	if refresh := unlock.UpperResponses[1]; refresh.MsgID != msgItemListRefresh || refresh.Classification != 0 {
		t.Fatalf("unlock refresh envelope = %+v", refresh)
	}
	if ack := unlock.UpperResponses[0]; ack.MsgID != uint16(dnfenum.CmdPacketRequestItemSortUnlock) || ack.Classification != 1 {
		t.Fatalf("unlock ack envelope = %+v", ack)
	}
	stack, ok = storedSortLock(t, ctx, repos, listTypeMain, 20)
	if !ok || IsSortLocked(stack) {
		t.Fatalf("unlock not persisted: %+v", stack)
	}
	if _, present := stack.Extra[SortLockExtraKey]; present {
		t.Fatalf("unlock left the key behind: %+v", stack.Extra)
	}

	// 显式 0 也必须能被客户端读到：投影值由 IsSortLocked 决定，而不是残留 raw。
	if got := SortLockRowValue(stack); got != SortLockRowUnlocked {
		t.Fatalf("unlocked row value = %d want 0", got)
	}

	// 未锁时再解锁：幂等成功，且不再写库。
	recordBefore := loadTestInventory(t, ctx, repos, "77")
	again := handleSortLock(t, ctx, repos, uint16(dnfenum.CmdPacketRequestItemSortUnlock), listTypeMain, 20)
	if !again.ResponseAllowed {
		t.Fatalf("idempotent unlock refused: %+v", again)
	}
	recordAfter := loadTestInventory(t, ctx, repos, "77")
	if !recordAfter.UpdatedAt.Equal(recordBefore.UpdatedAt) {
		t.Fatalf("idempotent unlock rewrote the record: before=%s after=%s", recordBefore.UpdatedAt, recordAfter.UpdatedAt)
	}
}

func TestSortLockRejectsUnsupportedListSharedSlotEmptySlotAndBadBody(t *testing.T) {
	ctx, repos := newSortLockFixture(t)
	before := loadTestInventory(t, ctx, repos, "77")

	for _, tc := range []struct {
		name     string
		listType byte
		slot     int16
	}{
		{"equipment_list", listTypeEquipment, 0},
		{"pet_list", listTypePet, 0},
		{"account_cargo", listTypeAccountCargo, 0},
		{"account_shared_slot", listTypeMain, dnfrepo.CrystalWarehouseFirstSlot},
		{"empty_slot", listTypeMain, 999},
		{"no_item_row", listTypeMain, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := handleSortLock(t, ctx, repos, uint16(dnfenum.CmdPacketRequestItemSortLock), tc.listType, tc.slot)
			if result.ResponseAllowed || len(result.UpperResponses) != 0 {
				t.Fatalf("unsupported target produced a reply: %+v", result)
			}
		})
	}

	for _, body := range [][]byte{{}, {0}, {0, 20}} {
		result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketRequestItemSortLock), Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos})
		if err != nil {
			t.Fatal(err)
		}
		if result.ResponseAllowed {
			t.Fatalf("body %v produced a reply: %+v", body, result)
		}
	}

	after := loadTestInventory(t, ctx, repos, "77")
	if !after.UpdatedAt.Equal(before.UpdatedAt) || len(after.Slots) != len(before.Slots) {
		t.Fatalf("rejected requests wrote state: before=%+v after=%+v", before, after)
	}
}

func TestSortLockRejectsForeignCharacterAccount(t *testing.T) {
	ctx, repos := newSortLockFixture(t)
	record := loadTestInventory(t, ctx, repos, "77")
	// 角色 77 属于 acc；用别的账号请求必须被拒，且不改状态。
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketRequestItemSortLock),
		Body:                sortLockBody(listTypeMain, 20),
		AccountID:           "other",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseAllowed {
		t.Fatalf("foreign account produced a reply: %+v", result)
	}
	if stack, ok := storedSortLock(t, ctx, repos, listTypeMain, 20); !ok || IsSortLocked(stack) {
		t.Fatalf("foreign account changed state: %+v", stack)
	}
	_ = record
}

// 永久装扮（Count==0）是真实占格：位置锁必须能加、也必须能投影出去。
func TestSortLockAcceptsPermanentAvatarRowWithZeroCount(t *testing.T) {
	ctx, repos := newSortLockFixture(t)
	result := handleSortLock(t, ctx, repos, uint16(dnfenum.CmdPacketRequestItemSortLock), listTypeAvatar, 30)
	if !result.ResponseAllowed || len(result.UpperResponses) != 2 {
		t.Fatalf("avatar lock refused: %+v", result)
	}
	stack, ok := storedSortLock(t, ctx, repos, listTypeAvatar, 30)
	if !ok || !IsSortLocked(stack) || stack.Count != 0 {
		t.Fatalf("avatar lock not persisted: %+v", stack)
	}
	if got := SortLockRowValue(stack); got != SortLockRowLocked {
		t.Fatalf("avatar row value = %d want 1", got)
	}
}

// failingInventoryStore 让 Save 失败，用来证明落库失败不产生半截状态、也不给成功包。
type failingInventoryStore struct {
	dnfrepo.InventoryRepository
	err       error
	saveCalls int
}

func (s *failingInventoryStore) Save(context.Context, dnfrepo.InventoryRecord) error {
	s.saveCalls++
	return s.err
}

func TestSortLockSaveFailureReturnsErrorWithoutPartialState(t *testing.T) {
	ctx, repos := newSortLockFixture(t)
	store := &failingInventoryStore{InventoryRepository: repos.Inventory, err: errors.New("injected save failure")}
	owner := &Owner{repo: store, characters: repos.Character, inItemTransaction: true}
	_, err := owner.ApplySortLock(ctx, Command{
		Operation:           "request_item_sort_lock",
		AccountID:           "acc",
		SelectedCharacterID: 77,
		SourceListType:      listTypeMain,
		SourceSlotIndex:     20,
		SortLock:            true,
		SortLockRequested:   true,
	})
	if err == nil {
		t.Fatal("save failure was swallowed")
	}
	if store.saveCalls != 1 {
		t.Fatalf("save attempts = %d want 1", store.saveCalls)
	}
	if stack, ok := storedSortLock(t, ctx, repos, listTypeMain, 20); !ok || IsSortLocked(stack) {
		t.Fatalf("failed save leaked state: %+v", stack)
	}
}

// 整理必须跳过锁定物品，并且不把别的物品排进被锁槽位（A21 InventorySortService）。
func TestSortSkipsLockedItemAndItsSlot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	record := dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 10): sortLockStack(2001, map[string]string{"item_kind": "equipment"}),
		slotKey(listTypeMain, 11): sortLockStack(3003, map[string]string{"item_kind": "equipment", SortLockExtraKey: "1"}),
		slotKey(listTypeMain, 12): sortLockStack(1001, map[string]string{"item_kind": "equipment"}),
	}}
	saveTestInventory(t, ctx, repos, record)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Sort(ctx, NewSortCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77}, SortItemRequest{ListType: listTypeMain, Category: 1}))
	if err != nil {
		t.Fatalf("sort error = %v", err)
	}
	if !result.Changed || result.MovedCount != 2 {
		t.Fatalf("sort result = %+v", result)
	}
	stored := loadTestInventory(t, ctx, repos, "77")
	if stack, ok := stored.Slots[slotKey(listTypeMain, 11)]; !ok || stack.ItemID != 3003 || !IsSortLocked(stack) {
		t.Fatalf("locked item moved: %+v", stack)
	}
	if stack := stored.Slots[slotKey(listTypeMain, 9)]; stack.ItemID != 1001 {
		t.Fatalf("slot 9 = %+v want item 1001", stack)
	}
	if stack := stored.Slots[slotKey(listTypeMain, 10)]; stack.ItemID != 2001 {
		t.Fatalf("slot 10 = %+v want item 2001", stack)
	}
	if _, taken := stored.Slots[slotKey(listTypeMain, 12)]; taken {
		t.Fatal("slot 12 should be empty after packing around the locked slot")
	}
}

func TestSortLockMoveRulesMatchReference(t *testing.T) {
	for _, tc := range []struct {
		name            string
		sourceList      byte
		sourceSlot      int16
		destinationList byte
		destinationSlot int16
		want            bool
	}{
		{"same_equipment_page", listTypeMain, 20, listTypeMain, 30, true},
		{"different_page", listTypeMain, 20, listTypeMain, 70, false},
		{"into_quick_slot", listTypeMain, 20, listTypeMain, 4, false},
		{"across_lists", listTypeMain, 20, listTypePersonalCargo, 20, false},
		{"into_equipment", listTypeMain, 20, listTypeEquipment, 12, false},
		{"out_of_equipment", listTypeEquipment, 12, listTypeMain, 20, false},
		{"inside_cargo", listTypePersonalCargo, 10, listTypePersonalCargo, 40, true},
		{"inside_avatar", listTypeAvatar, 10, listTypeAvatar, 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sortLockSurvivesMove(tc.sourceList, tc.sourceSlot, tc.destinationList, tc.destinationSlot); got != tc.want {
				t.Fatalf("survives = %t want %t", got, tc.want)
			}
		})
	}
	locked := sortLockStack(1001, map[string]string{"item_kind": "equipment", SortLockExtraKey: "1"})
	if got := applySortLockOnMove(locked, listTypeMain, 20, listTypeMain, 30); !IsSortLocked(got) {
		t.Fatal("same-kind same-list move dropped the lock")
	}
	if got := applySortLockOnMove(locked, listTypeMain, 20, listTypeMain, 4); IsSortLocked(got) {
		t.Fatal("quick-slot move kept the lock")
	}
	if got := applySortLockOnMove(locked, listTypeMain, 20, listTypeEquipment, 12); IsSortLocked(got) {
		t.Fatal("equip move kept the lock")
	}
	// 清锁只动 sort_lock 键，其余 Extra 原样保留。
	if got := applySortLockOnMove(locked, listTypeMain, 20, listTypeEquipment, 12); got.Extra["item_kind"] != "equipment" {
		t.Fatalf("clearing the lock dropped other extra keys: %+v", got.Extra)
	}
}

func TestMovePreservesOrClearsSortLockOnPersistedRow(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name            string
		destinationSlot int16
		destinationList byte
		wantLocked      bool
	}{
		{"same_page_move", 21, listTypeMain, true},
		{"quick_slot_move", 4, listTypeMain, false},
		{"cargo_move", 10, listTypePersonalCargo, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			if tc.destinationList == listTypePersonalCargo {
				settings := capacityTestSettings("77", 0)
				settings.Values["personal_cargo_list_param16"] = "24"
				if err := repos.Settings.Save(ctx, settings); err != nil {
					t.Fatal(err)
				}
			}
			saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc"})
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
				slotKey(listTypeMain, 20): sortLockStack(1001, map[string]string{"item_kind": "equipment", SortLockExtraKey: "1"}),
			}})
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Move(ctx, Command{
				Operation:            "move_itemspace",
				AccountID:            "acc",
				SelectedCharacterID:  77,
				SourceListType:       listTypeMain,
				SourceSlotIndex:      20,
				DestinationListType:  tc.destinationList,
				DestinationSlotIndex: tc.destinationSlot,
				MoveCount:            1,
			}); err != nil {
				t.Fatalf("move error = %v", err)
			}
			stack, ok := storedSortLock(t, ctx, repos, tc.destinationList, tc.destinationSlot)
			if !ok {
				t.Fatalf("destination slot empty after move")
			}
			if IsSortLocked(stack) != tc.wantLocked {
				t.Fatalf("destination lock = %t want %t (extra=%+v)", IsSortLocked(stack), tc.wantLocked, stack.Extra)
			}
		})
	}
}
