package dnfbridge

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	mem "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestGloryPackageRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real candidate PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentCeraPackageDefinition(catalog, 2683268)
	if err != nil {
		t.Fatalf("Glory of Light package: %v", err)
	}
	if len(definition.Rewards) != 15 {
		t.Fatalf("Glory rewards=%d", len(definition.Rewards))
	}
	ctx := context.Background()
	repos := mem.NewMemoryGroup()
	if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: "glory-test"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "glory-test", Level: 90, Stats: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:75": {ItemID: 2683268, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{options: options{accountID: "glory-test"}, pvfItemCatalog: catalog, repositoryProvider: func() (r.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 19}
	for range 2 {
		request := currentCeraPackageOpenRequest{SourceSlot: 75}
		prepared, err := service.prepareCurrentCeraPackage(ctx, session, catalog, request)
		if err != nil {
			t.Fatalf("prepare real Glory open: %v", err)
		}
		if _, err = service.commitCurrentCeraPackage(ctx, session, catalog, prepared, request); err != nil {
			t.Fatalf("commit real Glory open: %v", err)
		}
	}
	inventory, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatal("Glory inventory unavailable")
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID == 2683268 {
			t.Fatal("Glory source was not consumed")
		}
	}
}
