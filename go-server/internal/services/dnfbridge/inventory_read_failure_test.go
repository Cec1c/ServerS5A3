package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type r70OnceFailCharacterInventory struct {
	dnfrepo.InventoryRepository
	failNext bool
	loads    int
}

func (r *r70OnceFailCharacterInventory) Load(ctx context.Context, key string) (dnfrepo.InventoryRecord, bool, error) {
	r.loads++
	if r.failNext {
		r.failNext = false
		return dnfrepo.InventoryRecord{}, false, errors.New("injected first character inventory snapshot failure")
	}
	return r.InventoryRepository.Load(ctx, key)
}

func TestExistingCharacterInventoryReadFailureCannotBecomeAccountOnlyMainSnapshot(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	accountID := "dnf:inventory-read-failure"
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: accountID}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 1001, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: accountID, Slots: map[string]dnfrepo.ItemStack{"0:354": {ItemID: 3033, Count: 13}}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Inventory.Load(ctx, "77")
	accountBefore, _, _ := repos.AccountInventory.Load(ctx, accountID)
	failing := &r70OnceFailCharacterInventory{InventoryRepository: repos.Inventory, failNext: true}
	repos.Inventory = failing
	service := &Service{options: options{accountID: accountID}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{connID: "character-inventory-read-failure", selectedCharacterID: 77, accountID: accountID, itemListMigrationsDone: true}
	body, source, count, ok := service.buildCurrentSelectInventoryBootstrapBody(ctx, session, 0)
	t.Logf("ok=%t source=%s rows=%d bytes=%d character_loads=%d", ok, source, count, len(body), failing.loads)
	if len(body) >= 5 {
		for _, row := range decodeCurrentItemListTestRows(t, body[5:]) {
			t.Logf("projected_row=%x", row[:10])
		}
	}
	if ok || len(body) != 0 {
		t.Fatal("character inventory error installed account-only list0 and discarded true character rows")
	}
	if failing.loads != 1 || failing.failNext {
		t.Fatal("fault missed the inventory snapshot boundary")
	}
	body, _, count, ok = service.buildCurrentSelectInventoryBootstrapBody(ctx, session, 0)
	if !ok || count != 2 {
		t.Fatalf("healthy retry rejected: ok=%t count=%d", ok, count)
	}
	rows := decodeCurrentItemListTestRows(t, body[5:])
	assertCurrentItemListTestRow(t, rows[0], 3, 1001, 2)
	assertCurrentItemListTestRow(t, rows[1], 354, 3033, 13)
	after, _, err := failing.InventoryRepository.Load(ctx, "77")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("retry changed inventory")
	}
	accountAfter, _, err := repos.AccountInventory.Load(ctx, accountID)
	if err != nil || !reflect.DeepEqual(accountBefore, accountAfter) {
		t.Fatal("retry changed account warehouse")
	}
}

func TestAbsentCharacterInventoryReadErrorCannotBecomeHealthyEmptyOtherContainer(t *testing.T) {
	for _, listType := range []byte{1, 2} {
		t.Run(map[byte]string{1: "avatar", 2: "personal_cargo"}[listType], func(t *testing.T) {
			ctx := context.Background()
			repos := testRepositoryGroup()
			failing := &r70OnceFailCharacterInventory{InventoryRepository: repos.Inventory, failNext: true}
			repos.Inventory = failing
			service := &Service{options: options{accountID: "dnf:absent-inventory-error"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{connID: "absent-other-container", selectedCharacterID: 77, itemListMigrationsDone: true}
			body, source, count, ok := service.buildCurrentSelectInventoryBootstrapBody(ctx, session, listType)
			t.Logf("list=%d ok=%t source=%s rows=%d bytes=%d loads=%d", listType, ok, source, count, len(body), failing.loads)
			if ok || len(body) != 0 || failing.loads != 1 || failing.failNext {
				t.Fatal("snapshot error was replaced by a second-read healthy empty container")
			}
			body, _, count, ok = service.buildCurrentSelectInventoryBootstrapBody(ctx, session, listType)
			if !ok || count != 0 || len(body) == 0 || body[0] != listType {
				t.Fatalf("healthy absent retry rejected: list=%d ok=%t rows=%d bytes=%d", listType, ok, count, len(body))
			}
			_, found, err := failing.InventoryRepository.Load(ctx, "77")
			if err != nil || found {
				t.Fatal("snapshot/retry invented inventory")
			}
		})
	}
}

func TestLegacyInventoryThreeValueWrapperRetainsFailureSourceCompatibility(t *testing.T) {
	repos := testRepositoryGroup()
	failing := &r70OnceFailCharacterInventory{InventoryRepository: repos.Inventory, failNext: true}
	entries, source, known := currentItemListEntriesFromInventory(context.Background(), failing, "77", 1)
	if entries != nil || source != "" || known || failing.loads != 1 {
		t.Fatal("legacy snapshot wrapper failure behavior changed")
	}
}
