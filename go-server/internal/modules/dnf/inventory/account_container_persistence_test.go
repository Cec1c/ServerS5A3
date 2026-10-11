package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func stackableExtra(path string, limit string) map[string]string {
	return map[string]string{
		"item_kind":   "stackable",
		"pvf_path":    path,
		"stack_limit": limit,
	}
}

func TestPersistenceSortWireIncludesSharedRowsAfterAck(t *testing.T) {
	for _, category := range []byte{1, 99} {
		ctx := context.Background()
		r := dnfrepomemory.NewMemoryGroup()
		if err := r.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 200, Count: 1}, "0:358": {ItemID: 3037, Count: 1}}}); err != nil {
			t.Fatal(err)
		}
		if err := r.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: 5000}, "0:364": {ItemID: 111, Count: 27}, "12:3": {ItemID: 3227, Count: 7}}}); err != nil {
			t.Fatal(err)
		}
		got, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketSortItem), Body: []byte{0, category}, AccountID: "a", SelectedCharacterID: 77, Repositories: r})
		if err != nil || !got.ResponseAllowed || len(got.UpperResponses) != 2 {
			t.Fatal(got, err)
		}
		if got.UpperResponses[0].MsgID != uint16(dnfenum.CmdPacketSortItem) || got.UpperResponses[1].MsgID != msgItemListRefresh {
			t.Fatal("ack/refresh order", got)
		}
		body := got.UpperResponses[1].Body
		if binary.LittleEndian.Uint16(body[3:5]) != 3 {
			t.Fatal("wire omitted/mixed owner rows", body)
		}
		rows := map[int16]uint32{}
		for i := 5; i < len(body); i += currentItemListEntrySize {
			rows[int16(binary.LittleEndian.Uint16(body[i:i+2]))] = binary.LittleEndian.Uint32(body[i+6 : i+10])
		}
		if rows[358] != 5000 || rows[364] != 27 {
			t.Fatal("wrong crystal/soul wire count", rows)
		}
		c, _, _ := r.Inventory.Load(ctx, "77")
		if c.Slots["0:358"].Count != 1 {
			t.Fatal("sort rewrote legacy residue", c)
		}
	}
}
func TestPersistenceAccountCargoRefreshIncludesMergedBagAndCrystal(t *testing.T) {
	ctx := context.Background()
	r := dnfrepomemory.NewMemoryGroup()
	r.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8"}})
	r.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:21": pvfMoveStack(3227, 7, 20)}})
	r.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"12:3": pvfMoveStack(3227, 9, 20), "0:358": {ItemID: 3037, Count: 500}}})
	o, _ := NewOwner(r)
	cmd := Command{AccountID: "a", SelectedCharacterID: 77, SourceListType: 12, SourceSlotIndex: 3, DestinationListType: 0, DestinationSlotIndex: 25, MoveCount: 5}
	res, err := o.Move(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refresh[0]["0:21"].Count != 12 || res.Refresh[0]["0:358"].Count != 500 {
		t.Fatal("partial post-move snapshot", res)
	}
	got := ownerAppliedMoveResult(ctx, r.Settings, uint16(dnfenum.CmdPacketMoveItemspace), cmd, res)
	if len(got.UpperResponses) != 2 || len(got.PostActions) != 1 || got.PostActions[0] != alignedcmd.PostActionRefreshSelectedAccountCargo {
		t.Fatal("move refresh order", got)
	}
}
func TestPersistenceAccountCargoRejectsSharedWarehouseEndpoint(t *testing.T) {
	ctx := context.Background()
	r := dnfrepomemory.NewMemoryGroup()
	r.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8"}})
	r.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{}})
	r.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"12:3": pvfMoveStack(3227, 9, 20), "0:358": {ItemID: 3037, Count: 500}}})
	o, _ := NewOwner(r)
	_, err := o.Move(ctx, Command{AccountID: "a", SelectedCharacterID: 77, SourceListType: 12, SourceSlotIndex: 3, DestinationListType: 0, DestinationSlotIndex: 358, MoveCount: 5})
	if !errors.Is(err, ErrUnsupportedList) {
		t.Fatal("shared endpoint allowed", err)
	}
	c, _, _ := r.Inventory.Load(ctx, "77")
	a, _, _ := r.AccountInventory.Load(ctx, "a")
	if len(c.Slots) != 0 || a.Slots["12:3"].Count != 9 || a.Slots["0:358"].Count != 500 {
		t.Fatal("rejection changed ownership", c, a)
	}
}

func TestAccountCargoMergeGuardsAndCountMirrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target dnfrepo.ItemStack
		slot   int16
		mode   string
	}{
		{"same_template_different_count", pvfMoveStack(3227, 7, 20), 3, "auto_stack"},
		{"limit", pvfMoveStack(3227, 19, 20), 3, "split"},
		{"foreign_template", pvfMoveStack(3228, 7, 20), 3, "split"},
		{"outside_capacity", pvfMoveStack(3227, 7, 20), 9, "split"},
		{"equipment", dnfrepo.ItemStack{ItemID: 3227, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}, 3, "split"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := pvfMoveStack(3227, 9, 20)
			source.RawEntry = make([]byte, currentItemListEntrySize)
			source.RawEntry[90] = 0xAB
			source.Extra["amount_or_count"] = "9"
			source.Extra["quantity"] = "9"
			target := cloneStack(tc.target)
			target.RawEntry = make([]byte, currentItemListEntrySize)
			target.RawEntry[90] = 0xCD
			target.Extra["amount_or_count"] = strconv.FormatInt(target.Count, 10)
			src := map[string]dnfrepo.ItemStack{"0:21": source}
			dst := map[string]dnfrepo.ItemStack{slotKey(12, tc.slot): target}
			r, err := moveAccountCargoMaps("77", Command{SourceListType: 0, SourceSlotIndex: 21, DestinationListType: 12, DestinationSlotIndex: 4, MoveCount: 5}, src, dst, 8)
			if err != nil || r.Mode != tc.mode {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if src["0:21"].Count != 4 || src["0:21"].Extra["amount_or_count"] != "4" || src["0:21"].RawEntry[90] != 0xAB {
				t.Fatal("source count/identity diverged", src)
			}
			key := "12:4"
			want := int64(5)
			if tc.mode == "auto_stack" {
				key = "12:3"
				want = 12
			}
			got := dst[key]
			if got.Count != want || got.Extra["amount_or_count"] != strconv.FormatInt(want, 10) || binary.LittleEndian.Uint32(got.RawEntry[6:10]) != uint32(want) {
				t.Fatal("target count mirrors diverged", got)
			}
			if tc.mode == "auto_stack" && got.RawEntry[90] != 0xCD {
				t.Fatal("target identity overwritten")
			}
		})
	}
}

// Inject failure after staging the character save, before the account save.
type persistenceFailAccountSave struct {
	dnfrepo.AccountInventoryRepository
}

func (p persistenceFailAccountSave) Save(context.Context, dnfrepo.AccountInventoryRecord) error {
	return errors.New("persistence injected account save failure")
}

type persistenceFailUOW struct {
	dnfrepo.AccountCharacterItemUnitOfWork
}

func (p persistenceFailUOW) WithinAccountCharacterItems(ctx context.Context, a, c string, fn func(dnfrepo.AccountInventoryRepository, dnfrepo.InventoryRepository) error) error {
	return p.AccountCharacterItemUnitOfWork.WithinAccountCharacterItems(ctx, a, c, func(ar dnfrepo.AccountInventoryRepository, ir dnfrepo.InventoryRepository) error {
		return fn(persistenceFailAccountSave{ar}, ir)
	})
}
func TestAccountCargoMergeSaveFailureRollsBackBothOwners(t *testing.T) {
	ctx := context.Background()
	r := dnfrepomemory.NewMemoryGroup()
	r.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8"}})
	r.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:21": pvfMoveStack(3227, 9, 20)}})
	r.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"12:3": pvfMoveStack(3227, 7, 20), "0:358": {ItemID: 3037, Count: 500}}})
	r.AccountItems = persistenceFailUOW{r.AccountItems}
	o, _ := NewOwner(r)
	result, err := o.Move(ctx, Command{AccountID: "a", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 21, DestinationListType: 12, DestinationSlotIndex: 4, MoveCount: 5})
	if err == nil || result.Changed {
		t.Fatal("save failure exposed success", result, err)
	}
	ch, _, _ := r.Inventory.Load(ctx, "77")
	acc, _, _ := r.AccountInventory.Load(ctx, "a")
	if ch.Slots["0:21"].Count != 9 || acc.Slots["12:3"].Count != 7 || acc.Slots["0:358"].Count != 500 || len(acc.Slots) != 2 {
		t.Fatal("failed move changed owner", ch, acc)
	}
}
func TestSortRefreshErrorRollsBackCharacterAndWithholdsAck(t *testing.T) {
	ctx := context.Background()
	r := dnfrepomemory.NewMemoryGroup()
	r.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 200, Count: 1}}})
	r.AccountItems = persistenceReadFailUOW{r.AccountItems}
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketSortItem), Body: []byte{0, 1}, AccountID: "a", SelectedCharacterID: 77, Repositories: r})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseAllowed || len(result.UpperResponses) != 0 {
		t.Fatal("snapshot failure exposed ack", result)
	}
	ch, _, _ := r.Inventory.Load(ctx, "77")
	if ch.Slots["0:20"].ItemID != 200 || len(ch.Slots) != 1 {
		t.Fatal("failed refresh changed character", ch)
	}
}

type persistenceReadFailUOW struct {
	dnfrepo.AccountCharacterItemUnitOfWork
}

func (p persistenceReadFailUOW) WithinAccountCharacterItems(ctx context.Context, a, c string, fn func(dnfrepo.AccountInventoryRepository, dnfrepo.InventoryRepository) error) error {
	return p.AccountCharacterItemUnitOfWork.WithinAccountCharacterItems(ctx, a, c, func(ar dnfrepo.AccountInventoryRepository, ir dnfrepo.InventoryRepository) error {
		return fn(failingMoveAccountInventoryRepository{err: errors.New("persistence injected read failure")}, ir)
	})
}

// P61/P68 现场：往账号金库放入同样的物品，多次放入不同数量不会堆叠；
// 个人金库同一操作会堆叠。个人金库走 Owner.Move 的 auto_stack 分支，
// 账号金库（list 12）走 moveAccountCargo，两者当时只差这一条合并判据。
func TestAccountCargoDepositMergesIntoExistingCompatibleRow(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "dnf:auto-stack",
		Metadata:  map[string]string{"account_cargo_created": "true", "account_cargo_level": "8"},
	}); err != nil {
		t.Fatal(err)
	}
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 21): {ItemID: 3227, Count: 5, Extra: extra},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:auto-stack",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 7, Extra: extra},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID:           "dnf:auto-stack",
		SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType:       listTypeMain,
		SourceSlotIndex:      21,
		MoveCount:            5,
		DestinationListType:  listTypeAccountCargo,
		DestinationSlotIndex: 4,
	}))
	if err != nil {
		t.Fatalf("deposit into account cargo: %v", err)
	}
	if !result.Changed || result.Mode != "auto_stack" || result.MoveCount != 5 {
		t.Fatalf("deposit result=%+v, want auto_stack of 5", result)
	}

	character, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if _, exists := character.Slots[slotKey(listTypeMain, 21)]; exists {
		t.Fatalf("source row survived the merge: %+v", character.Slots)
	}
	cargo, found, err := repos.AccountInventory.Load(ctx, "dnf:auto-stack")
	if err != nil || !found {
		t.Fatalf("load account cargo found=%t err=%v", found, err)
	}
	if len(cargo.Slots) != 1 {
		t.Fatalf("account cargo rows=%+v, want exactly one merged row", cargo.Slots)
	}
	merged := cargo.Slots[slotKey(listTypeAccountCargo, 3)]
	if merged.ItemID != 3227 || merged.Count != 12 {
		t.Fatalf("merged row=%+v, want item 3227 count 12", merged)
	}
	if _, exists := cargo.Slots[slotKey(listTypeAccountCargo, 4)]; exists {
		t.Fatalf("dropped slot kept a duplicate row: %+v", cargo.Slots)
	}
}

// 部分数量：只把请求的数量并进已有行，源行保留剩余。
func TestAccountCargoDepositMergesRequestedCountOnly(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "dnf:auto-stack-partial",
		Metadata:  map[string]string{"account_cargo_created": "true", "account_cargo_level": "8"},
	}); err != nil {
		t.Fatal(err)
	}
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 21): {ItemID: 3227, Count: 9, Extra: extra},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:auto-stack-partial",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 7, Extra: extra},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID:           "dnf:auto-stack-partial",
		SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType:       listTypeMain,
		SourceSlotIndex:      21,
		MoveCount:            5,
		DestinationListType:  listTypeAccountCargo,
		DestinationSlotIndex: 4,
	}))
	if err != nil {
		t.Fatalf("partial deposit: %v", err)
	}
	if !result.Changed || result.Mode != "auto_stack" || result.MoveCount != 5 {
		t.Fatalf("partial deposit result=%+v", result)
	}
	character, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if got := character.Slots[slotKey(listTypeMain, 21)].Count; got != 4 {
		t.Fatalf("source remainder=%d, want 4", got)
	}
	cargo, _, err := repos.AccountInventory.Load(ctx, "dnf:auto-stack-partial")
	if err != nil {
		t.Fatal(err)
	}
	if got := cargo.Slots[slotKey(listTypeAccountCargo, 3)].Count; got != 12 {
		t.Fatalf("merged count=%d, want 12", got)
	}
	if _, exists := cargo.Slots[slotKey(listTypeAccountCargo, 4)]; exists {
		t.Fatalf("unexpected row on the release slot: %+v", cargo.Slots)
	}
}

// 反方向（取出）：账号金库 → 背包空槽，背包里已有同一行时同样并入。
func TestAccountCargoWithdrawMergesIntoExistingCompatibleRow(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "dnf:auto-stack-out",
		Metadata:  map[string]string{"account_cargo_created": "true", "account_cargo_level": "8"},
	}); err != nil {
		t.Fatal(err)
	}
	extra := stackableExtra("stackable/material/solvent_magicpower.stk", "1000")
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 30): {ItemID: 3227, Count: 7, Extra: extra},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:auto-stack-out",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeAccountCargo, 3): {ItemID: 3227, Count: 5, Extra: extra},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID:           "dnf:auto-stack-out",
		SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType:       listTypeAccountCargo,
		SourceSlotIndex:      3,
		MoveCount:            5,
		DestinationListType:  listTypeMain,
		DestinationSlotIndex: 25,
	}))
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if !result.Changed || result.Mode != "auto_stack" || result.MoveCount != 5 {
		t.Fatalf("withdraw result=%+v, want auto_stack of 5", result)
	}
	character, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if len(character.Slots) != 1 {
		t.Fatalf("character rows=%+v, want one merged row", character.Slots)
	}
	if got := character.Slots[slotKey(listTypeMain, 30)].Count; got != 12 {
		t.Fatalf("merged bag count=%d, want 12", got)
	}
	cargo, _, err := repos.AccountInventory.Load(ctx, "dnf:auto-stack-out")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cargo.Slots[slotKey(listTypeAccountCargo, 3)]; exists {
		t.Fatalf("cargo row survived the merge: %+v", cargo.Slots)
	}
}

// P66 现场：整理物品栏会让晶体仓库数量归零，重新选择角色后恢复。
// 整理 ACK 后发的 op13 是整表快照，客户端未列出的格子即空；list 0 快照
// 必须带上账号持有的 354..365 行（与会话快照、移动后刷新同一口径）。
func TestSortMainRefreshKeepsAccountSharedCrystalRows(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:sort-shared"}); err != nil {
		t.Fatal(err)
	}
	crystalExtra := stackableExtra("stackable/material/cubepiece_clear.stk", "100000")
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:sort-shared",
		Slots: map[string]dnfrepo.ItemStack{
			dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot + 4): {ItemID: 3037, Count: 5000, Extra: crystalExtra},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
			slotKey(listTypeMain, 20): {ItemID: 100, Count: 1, Extra: map[string]string{"item_kind": "avatar"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Sort(ctx, NewSortCommand(alignedcmd.Request{
		AccountID:           "dnf:sort-shared",
		SelectedCharacterID: 77,
	}, SortItemRequest{
		ListType: listTypeMain,
		Category: 1,
	}))
	if err != nil {
		t.Fatalf("Sort error = %v", err)
	}
	crystalKey := dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot + 4)
	if got, ok := result.Refresh[crystalKey]; !ok {
		t.Fatalf("sort refresh dropped the account crystal row: %+v", result.Refresh)
	} else if got.ItemID != 3037 || got.Count != 5000 {
		t.Fatalf("crystal row=%+v, want item 3037 count 5000", got)
	}
	// 账号持有行不能被整理写进角色侧（角色侧残留是另一条独立证据）。
	character, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := character.Slots[crystalKey]; exists {
		t.Fatalf("sort persisted an account-owned row into the character record: %+v", character.Slots)
	}
}

// 没有账号持有行的普通整理保持原状：刷新只包含角色自己的 list 0 行。
func TestSortMainRefreshWithoutAccountSharedRowsIsUnchanged(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
			slotKey(listTypeMain, 20): {ItemID: 100, Count: 1, Extra: map[string]string{"item_kind": "avatar"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Sort(ctx, NewSortCommand(alignedcmd.Request{
		AccountID:           "dnf:absent",
		SelectedCharacterID: 77,
	}, SortItemRequest{
		ListType: listTypeMain,
		Category: 1,
	}))
	if err != nil {
		t.Fatalf("Sort error = %v", err)
	}
	if len(result.Refresh) != 2 {
		t.Fatalf("refresh=%+v, want the two character rows only", result.Refresh)
	}
	if got := result.Refresh[slotKey(listTypeMain, 9)].ItemID; got != 100 {
		t.Fatalf("slot 9 item=%d, want 100", got)
	}
}
