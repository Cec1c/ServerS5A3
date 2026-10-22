package aikenupgrade

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

var initialRepairTestNow = time.Unix(1800000000, 0)

func initialRepairTestStack(slot int16, identity string, state [5]byte) repo.ItemStack {
	raw := make([]byte, 119)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	copy(raw[:5], state[:])
	extra := map[string]string{
		"source":            "dungeon_pvf_drop_pickup",
		"sort_lock":         "1",
		"test_instance_tag": identity,
	}
	if identity != "" {
		extra["native_buff_instance"] = identity
	}
	return repo.ItemStack{
		ItemID:   20000 + int64(slot),
		Count:    1,
		Bind:     true,
		ExpireAt: initialRepairTestNow.Add(time.Hour),
		RawEntry: raw,
		Extra:    extra,
	}
}

func initialRepairTestEntry(slot int16, identity string, state [5]byte) repo.EquipmentEntry {
	stack := initialRepairTestStack(slot, identity, state)
	return repo.EquipmentEntry{SlotIndex: slot, ItemID: stack.ItemID, Bind: stack.Bind, ExpireAt: stack.ExpireAt, RawEntry: stack.RawEntry, Extra: stack.Extra}
}

func initialRepairTestCommand(t *testing.T, rollCalls *int) InitialRepairCommand {
	t.Helper()
	catalog := mustInitialTestCatalog(t)
	return InitialRepairCommand{
		AccountID:   "acct",
		CharacterID: "77",
		Now:         initialRepairTestNow,
		Catalog: func(int64) (*InitialCatalog, error) {
			return catalog, nil
		},
		ReadState: func(s repo.ItemStack) ([5]byte, error) {
			var state [5]byte
			if len(s.RawEntry) < len(state) {
				return state, fmt.Errorf("short initial state")
			}
			copy(state[:], s.RawEntry[:len(state)])
			return state, nil
		},
		WriteState: func(s repo.ItemStack, _ int16, state [5]byte) (repo.ItemStack, error) {
			if len(s.RawEntry) != 119 {
				return s, fmt.Errorf("unexpected raw entry length")
			}
			raw := append([]byte(nil), s.RawEntry...)
			copy(raw[:5], state[:])
			s.RawEntry = raw
			return s, nil
		},
		Roll: func(limit uint32) (uint32, error) {
			if limit == 0 {
				return 0, fmt.Errorf("empty roll bound")
			}
			(*rollCalls)++
			return 0, nil
		},
	}
}

func initialRepairTestOwner(t *testing.T, slots, warehouse, account map[string]repo.ItemStack, worn map[string]repo.EquipmentEntry) (repo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "acct"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: slots, Warehouse: warehouse}); err != nil {
		t.Fatal(err)
	}
	if err := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "acct", Slots: account}); err != nil {
		t.Fatal(err)
	}
	if err := g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "77", Entries: worn}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	return g, o
}

func initialRepairTestRecords(t *testing.T, g repo.Group) (repo.InventoryRecord, repo.AccountInventoryRecord, repo.EquipmentRecord) {
	t.Helper()
	ctx := context.Background()
	inv, ok, err := g.Inventory.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load inventory: ok=%t err=%v", ok, err)
	}
	account, ok, err := g.AccountInventory.Load(ctx, "acct")
	if err != nil || !ok {
		t.Fatalf("load account inventory: ok=%t err=%v", ok, err)
	}
	worn, ok, err := g.Equipment.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load equipment: ok=%t err=%v", ok, err)
	}
	return inv, account, worn
}

func initialRepairTestAssertUnchanged(t *testing.T, beforeInv repo.InventoryRecord, beforeAccount repo.AccountInventoryRecord, beforeWorn repo.EquipmentRecord, g repo.Group) {
	t.Helper()
	inv, account, worn := initialRepairTestRecords(t, g)
	if !reflect.DeepEqual(inv, beforeInv) || !reflect.DeepEqual(account, beforeAccount) || !reflect.DeepEqual(worn, beforeWorn) {
		t.Fatal("ambiguous historical assets were mutated")
	}
}

func initialRepairTestAssertRepaired(t *testing.T, before, after repo.ItemStack) {
	t.Helper()
	if after.RawEntry[0] != 1 || after.RawEntry[1] != 1 || after.RawEntry[2] != before.RawEntry[2] || after.RawEntry[3] != before.RawEntry[3] || after.RawEntry[4] != before.RawEntry[4] {
		t.Fatalf("only the two option bytes may change: before=%x after=%x", before.RawEntry, after.RawEntry)
	}
	if !bytes.Equal(after.RawEntry[5:], before.RawEntry[5:]) || after.ItemID != before.ItemID || after.Count != before.Count || after.Bind != before.Bind || !after.ExpireAt.Equal(before.ExpireAt) || !reflect.DeepEqual(after.Extra, before.Extra) {
		t.Fatalf("repair changed non-option instance state: before=%+v after=%+v", before, after)
	}
}

func TestRepairInitialOptionsSkipsEndpointAliasesInEveryContainer(t *testing.T) {
	state := [5]byte{0, 0, 0, 0x44, 2}
	inventory := map[string]repo.ItemStack{
		"0:9":   initialRepairTestStack(9, "inventory-a", state),
		"00:09": initialRepairTestStack(9, "inventory-b", state),
	}
	warehouse := map[string]repo.ItemStack{
		"2:30":  initialRepairTestStack(30, "warehouse-a", state),
		"02:30": initialRepairTestStack(30, "warehouse-b", state),
	}
	account := map[string]repo.ItemStack{
		"12:40":  initialRepairTestStack(40, "account-a", state),
		"012:40": initialRepairTestStack(40, "account-b", state),
	}
	worn := map[string]repo.EquipmentEntry{
		"5":  initialRepairTestEntry(5, "worn-a", state),
		"05": initialRepairTestEntry(5, "worn-b", state),
	}
	g, owner := initialRepairTestOwner(t, inventory, warehouse, account, worn)
	beforeInv, beforeAccount, beforeWorn := initialRepairTestRecords(t, g)
	calls := 0
	count, err := owner.RepairInitialOptions(context.Background(), initialRepairTestCommand(t, &calls))
	if err != nil || count != 0 || calls != 0 {
		t.Fatalf("ambiguous endpoints repaired: count=%d rolls=%d err=%v", count, calls, err)
	}
	initialRepairTestAssertUnchanged(t, beforeInv, beforeAccount, beforeWorn, g)
}

func TestRepairInitialOptionsSkipsDuplicateInstancesAcrossContainers(t *testing.T) {
	state := [5]byte{0, 0, 0, 0x44, 2}
	inventory := map[string]repo.ItemStack{"0:10": initialRepairTestStack(10, "duplicate", state)}
	warehouse := map[string]repo.ItemStack{"2:20": initialRepairTestStack(20, "duplicate", state)}
	account := map[string]repo.ItemStack{"12:40": initialRepairTestStack(40, "duplicate", state)}
	worn := map[string]repo.EquipmentEntry{"7": initialRepairTestEntry(7, "duplicate", state)}
	g, owner := initialRepairTestOwner(t, inventory, warehouse, account, worn)
	beforeInv, beforeAccount, beforeWorn := initialRepairTestRecords(t, g)
	calls := 0
	count, err := owner.RepairInitialOptions(context.Background(), initialRepairTestCommand(t, &calls))
	if err != nil || count != 0 || calls != 0 {
		t.Fatalf("duplicate native instance repaired: count=%d rolls=%d err=%v", count, calls, err)
	}
	initialRepairTestAssertUnchanged(t, beforeInv, beforeAccount, beforeWorn, g)
}

func TestRepairInitialOptionsRepairsDistinctInstancesAndPreservesOtherState(t *testing.T) {
	state := [5]byte{0, 0, 0, 0x44, 2}
	inventory := map[string]repo.ItemStack{
		"0:11": initialRepairTestStack(11, "inventory-one", state),
		"0:12": initialRepairTestStack(12, "inventory-two", state),
		"0:13": initialRepairTestStack(13, "already-selected", [5]byte{1, 2, 0, 0x44, 2}),
	}
	warehouse := map[string]repo.ItemStack{"2:31": initialRepairTestStack(31, "warehouse", state)}
	account := map[string]repo.ItemStack{"12:41": initialRepairTestStack(41, "account", state)}
	worn := map[string]repo.EquipmentEntry{"8": initialRepairTestEntry(8, "worn", state)}
	g, owner := initialRepairTestOwner(t, inventory, warehouse, account, worn)
	beforeInv, beforeAccount, beforeWorn := initialRepairTestRecords(t, g)
	calls := 0
	count, err := owner.RepairInitialOptions(context.Background(), initialRepairTestCommand(t, &calls))
	if err != nil || count != 5 || calls != 15 {
		t.Fatalf("distinct instances not repaired: count=%d rolls=%d err=%v", count, calls, err)
	}
	afterInv, afterAccount, afterWorn := initialRepairTestRecords(t, g)
	if !afterInv.UpdatedAt.Equal(initialRepairTestNow) || !afterAccount.UpdatedAt.Equal(initialRepairTestNow) || !afterWorn.UpdatedAt.Equal(initialRepairTestNow) {
		t.Fatal("repair did not persist the expected container timestamp")
	}
	for _, key := range []string{"0:11", "0:12"} {
		initialRepairTestAssertRepaired(t, beforeInv.Slots[key], afterInv.Slots[key])
	}
	if !reflect.DeepEqual(beforeInv.Slots["0:13"], afterInv.Slots["0:13"]) {
		t.Fatal("repair changed an existing option selection")
	}
	initialRepairTestAssertRepaired(t, beforeInv.Warehouse["2:31"], afterInv.Warehouse["2:31"])
	initialRepairTestAssertRepaired(t, beforeAccount.Slots["12:41"], afterAccount.Slots["12:41"])
	beforeEntry, afterEntry := beforeWorn.Entries["8"], afterWorn.Entries["8"]
	initialRepairTestAssertRepaired(t,
		repo.ItemStack{ItemID: beforeEntry.ItemID, Count: 1, Bind: beforeEntry.Bind, ExpireAt: beforeEntry.ExpireAt, RawEntry: beforeEntry.RawEntry, Extra: beforeEntry.Extra},
		repo.ItemStack{ItemID: afterEntry.ItemID, Count: 1, Bind: afterEntry.Bind, ExpireAt: afterEntry.ExpireAt, RawEntry: afterEntry.RawEntry, Extra: afterEntry.Extra},
	)
}
