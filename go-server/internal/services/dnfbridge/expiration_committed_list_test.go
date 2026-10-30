package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type expirationLegacyItems struct {
	items []dnfrepo.LegacyInventoryItem
}

func (r *expirationLegacyItems) SelectItems(context.Context, string, byte) ([]dnfrepo.LegacyInventoryItem, error) {
	return append([]dnfrepo.LegacyInventoryItem(nil), r.items...), nil
}

type expirationMissingInventory struct {
	dnfrepo.InventoryRepository
}

func (expirationMissingInventory) Load(context.Context, string) (dnfrepo.InventoryRecord, bool, error) {
	return dnfrepo.InventoryRecord{}, false, nil
}

type expirationMissingAssetInventory struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
}

func (u expirationMissingAssetInventory) WithinAccountCharacterAssets(ctx context.Context, accountID, characterID string, apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, accountID, characterID,
		func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
			return apply(a, c, expirationMissingInventory{InventoryRepository: i}, e)
		})
}

func TestExpirationCommittedListKeepsEmptyCanonicalAuthoritative(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	ctx := context.Background()
	legacy := &expirationLegacyItems{items: []dnfrepo.LegacyInventoryItem{{
		ListType: dnfrepo.MainInventoryListType, SlotIndex: 121, ItemTemplateID: 3227, StackCount: 99,
	}}}
	group := fixture.group
	group.LegacyInventory = legacy
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }

	inventory, found, err := group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load canonical inventory found=%t err=%v", found, err)
	}
	inventory.Slots = map[string]dnfrepo.ItemStack{}
	if err := group.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	body, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(ctx, fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("empty canonical list build failed")
	}
	if item, found := currentMainListBodySlots(t, body)[121]; found {
		t.Fatalf("empty canonical record revived legacy item %d", item)
	}

	missing := group
	missing.Inventory = expirationMissingInventory{InventoryRepository: group.Inventory}
	missing.AccountAssets = expirationMissingAssetInventory{AccountCharacterAssetUnitOfWork: group.AccountAssets}
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return missing, true }
	body, _, _, ok = fixture.service.buildCurrentItemListBodyForSession(ctx, fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("missing canonical list build failed")
	}
	if item := currentMainListBodySlots(t, body)[121]; item != 3227 {
		t.Fatalf("missing canonical slot 121 item=%d, want legacy fallback 3227", item)
	}
}

func TestExpirationCommittedListReadsUpdatesOnNextBuild(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	ctx := context.Background()
	first, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(ctx, fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("first list build failed")
	}
	firstRows := currentMainListBodySlots(t, first)
	firstWallet, found := pair914Row(t, first, 0)
	if !found || firstRows[1] != 3227 || firstRows[354] != 3033 || binary.LittleEndian.Uint32(firstWallet[6:10]) != 123456 {
		t.Fatalf("first list did not expose fixture values: rows=%v wallet=%x", firstRows, firstWallet)
	}

	inventory, found, err := fixture.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	stack := inventory.Slots["0:1"]
	stack.Count = 777
	inventory.Slots["0:1"] = stack
	if err := fixture.group.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	account, found, err := fixture.group.AccountInventory.Load(ctx, "dnf:1")
	if err != nil || !found {
		t.Fatalf("load account inventory found=%t err=%v", found, err)
	}
	account.Slots["0:354"] = dnfrepo.ItemStack{ItemID: 3034, Count: 2}
	if err := fixture.group.AccountInventory.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	character, found, err := fixture.group.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["gold"] = 876543
	if err := fixture.group.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}

	fixture.counter.reset()
	second, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(ctx, fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("second list build failed")
	}
	secondRows := currentMainListBodySlots(t, second)
	mainRow, _ := pair914Row(t, second, 1)
	wallet, _ := pair914Row(t, second, 0)
	if secondRows[354] != 3034 || binary.LittleEndian.Uint32(mainRow[6:10]) != 777 ||
		binary.LittleEndian.Uint32(wallet[6:10]) != 876543 {
		t.Fatalf("second list reused stale values: rows=%v main=%x wallet=%x calls=%s",
			secondRows, mainRow, wallet, fixture.counter.summary())
	}
}

func TestExpirationCommittedWalletSurvivesOuterCharacterLoadFailure(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	fixture.buffer.write.Reset()
	group := fixture.group
	group.Character = pair916FailingCharacterRepository{
		CharacterRepository: group.Character,
		err:                 errors.New("outer character load failed"),
	}
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	frame := pair916SendMarkedList0Refresh(t, fixture)
	if frame.Header.Classification != 0 || frame.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) {
		t.Fatalf("first frame = class%d msg0x%04X, want authoritative list0", frame.Header.Classification, frame.Header.MsgID)
	}
	wallet, found := pair914Row(t, frame.Body, 0)
	if !found || binary.LittleEndian.Uint32(wallet[6:10]) != 123456 {
		t.Fatalf("committed wallet row=%x found=%t, want 123456", wallet, found)
	}
}
