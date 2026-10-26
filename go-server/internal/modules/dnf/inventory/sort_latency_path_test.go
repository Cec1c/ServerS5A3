package inventory

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type sortAccountInventoryLoadCounter struct {
	dnfrepo.AccountInventoryRepository
	loads *int
}

func (r *sortAccountInventoryLoadCounter) Load(ctx context.Context, accountID string) (dnfrepo.AccountInventoryRecord, bool, error) {
	(*r.loads)++
	return r.AccountInventoryRepository.Load(ctx, accountID)
}

type sortAccountItemsCounter struct {
	inner dnfrepo.AccountCharacterItemUnitOfWork
	loads *int
}

func (u *sortAccountItemsCounter) WithinAccountCharacterItems(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountInventoryRepository, dnfrepo.InventoryRepository) error,
) error {
	return u.inner.WithinAccountCharacterItems(ctx, accountID, characterID, func(accounts dnfrepo.AccountInventoryRepository, inventory dnfrepo.InventoryRepository) error {
		return apply(&sortAccountInventoryLoadCounter{AccountInventoryRepository: accounts, loads: u.loads}, inventory)
	})
}

type sortSettingsLoadCounter struct {
	dnfrepo.SettingsRepository
	loads int
}

func (r *sortSettingsLoadCounter) Load(ctx context.Context, scope string) (dnfrepo.SettingsRecord, bool, error) {
	r.loads++
	return r.SettingsRepository.Load(ctx, scope)
}

func TestSortChangedInventoryReadsAccountSnapshotOnceAfterTransactionPreimage(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
			slotKey(listTypeMain, 20): {ItemID: 100, Count: 1, Extra: map[string]string{"item_kind": "avatar"}},
		},
	})
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "acc",
		Slots: map[string]dnfrepo.ItemStack{
			dnfrepo.AccountSharedInventorySlotKey(354): {ItemID: 3033, Count: 7},
		},
	}); err != nil {
		t.Fatal(err)
	}
	loads := 0
	repos.AccountItems = &sortAccountItemsCounter{inner: repos.AccountItems, loads: &loads}

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Sort(ctx, NewSortCommand(alignedcmd.Request{
		AccountID:           "acc",
		SelectedCharacterID: 77,
	}, SortItemRequest{ListType: listTypeMain, Category: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("fixture did not change slot order")
	}
	// The unit-of-work before-image read happens before apply and is not counted
	// here. A changed sort freezes its refresh once after mutation and before
	// Save; the former pre-save and post-Save snapshots read this repository twice.
	if loads != 1 {
		t.Fatalf("account inventory loads=%d, want 1 (frozen refresh only)", loads)
	}
	if got := result.Refresh[dnfrepo.AccountSharedInventorySlotKey(354)]; got.ItemID != 3033 || got.Count != 7 {
		t.Fatalf("shared account row missing from frozen refresh: %+v", got)
	}
}

func TestSortHandlerReusesContainerStateAlreadyReadByOwner(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
			slotKey(listTypeMain, 20): {ItemID: 100, Count: 1, Extra: map[string]string{"item_kind": "avatar"}},
		},
	})
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: dnfrepo.CharacterContainerStateScope("77"),
		Values: map[string]string{
			"main_list_param16":           "24",
			"avatar_list_param16":         "0",
			"personal_cargo_list_param16": "8",
		},
	}); err != nil {
		t.Fatal(err)
	}
	settings := &sortSettingsLoadCounter{SettingsRepository: repos.Settings}
	repos.Settings = settings

	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketSortItem),
		Body:                []byte{listTypeMain, 1},
		AccountID:           "acc",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil || !result.ResponseAllowed || len(result.UpperResponses) != 2 {
		t.Fatalf("sort result=%+v err=%v", result, err)
	}
	if settings.loads != 1 {
		t.Fatalf("settings loads=%d, want 1 (owner read reused by refresh builder)", settings.loads)
	}
	body := result.UpperResponses[1].Body
	if len(body) < 3 || body[0] != listTypeMain || body[1] != 24 {
		t.Fatalf("refresh did not retain authoritative main capacity: % X", body[:min(len(body), 5)])
	}
}
