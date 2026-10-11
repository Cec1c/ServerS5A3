package dnfbridge

import (
	"context"
	"os"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/revivecoin"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFReviveBoxDoesNotChargeNPCExchangePriceAgain(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires runtime PVF")
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentBoosterDefinition(catalog, 490005029, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	// The real PVF retains its NPC purchase cost, while opening must charge
	// only the source box. No production account or asset is touched.
	if definition.MaterialItemID != 490005025 || definition.MaterialCount != 2 || definition.Random.MaterialItemID != 0 {
		t.Fatalf("exchange and opening prices conflated: %+v", definition)
	}
	for _, alreadyHasExchangeCoins := range []bool{false, true} {
		repos := memory.NewMemoryGroup()
		ctx := context.Background()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}))
		slots := map[string]dnfrepo.ItemStack{"0:106": {ItemID: 490005029, Count: 2, ExpireAt: time.Now().Add(time.Hour)}}
		if alreadyHasExchangeCoins {
			slots["0:122"] = dnfrepo.ItemStack{ItemID: 490005025, Count: 10}
		}
		must(repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: slots}))
		s := currentBoosterTestService(catalog, repos)
		result, err := s.commitCurrentBooster(ctx, &gameSession{selectedCharacterID: 19}, catalog, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 106})
		must(err)
		if result.SourceRemaining != 1 || len(result.Rewards) != 1 || result.Rewards[0].ItemID != 1 || result.Rewards[0].Count != 1 {
			t.Fatalf("result=%+v", result)
		}
		inv, found, err := repos.Inventory.Load(ctx, "19")
		must(err)
		if !found || inv.Slots["0:106"].Count != 1 || inv.Slots[revivecoin.WalletKey()].Count != 1 {
			t.Fatalf("grant=%+v", inv.Slots)
		}
		if alreadyHasExchangeCoins && inv.Slots["0:122"].Count != 10 {
			t.Fatal("opening charged the purchase price twice")
		}
	}
}
