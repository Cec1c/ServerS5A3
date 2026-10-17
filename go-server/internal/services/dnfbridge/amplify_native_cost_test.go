package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// This uses the real archive and the ordinary owner with no legacy Extra fee.
// The old resolver leaves amplification unresolved and this test fails first.
func TestRealPVFOrdinaryAmplifyAuthoritativePayment(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{initialEquipmentArchive: archive}
	for _, setting := range []string{"", "candidate"} {
		t.Run("env="+setting, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", setting)
			resolver := service.candidateUpgradeCostResolver(nil)
			if resolver == nil {
				t.Fatal("ordinary amplification resolver missing")
			}
			quote, err := resolver("amplify", 28309, 0, true, 1)
			if err != nil || !quote.Resolved || quote.GoldCost <= 0 || quote.ConversionCount != 0 {
				t.Fatalf("ordinary amplification quote=%+v err=%v", quote, err)
			}
			t.Logf("real PVF item28309 amplification gold=%d", quote.GoldCost)
			for _, short := range []bool{false, true} {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				gold := quote.GoldCost + 100
				if short {
					gold = quote.GoldCost - 1
				}
				character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "a", Stats: map[string]int64{"gold": gold}}
				bag := dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
					"0:54":  {ItemID: 28309, Count: 1, Extra: map[string]string{"item_kind": "equipment", "durability": "45", "max_durability": "45", "amplify_type": "3", "amplify_value": "5"}},
					"0:135": {ItemID: 3242, Count: 20},
				}}
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				if err := repos.Inventory.Save(ctx, bag); err != nil {
					t.Fatal(err)
				}
				owner, err := inventory.NewOwner(repos)
				if err != nil {
					t.Fatal(err)
				}
				got, err := owner.Upgrade(ctx, inventory.Command{Mode: "amplify", AccountID: "a", SelectedCharacterID: 77, TargetSlotIndex: 54, TargetItemTemplateID: 28309, MaterialSlotIndex: 135, OptionalTicketSlot: -1, UpgradeSuccessWeight: 100000, UpgradeMaterialItemID: 3242, UpgradeMaterialCount: 10, UpgradeNPCPresent: true, UpgradeNPCID: 1, UpgradeCostResolver: resolver})
				wallet, _, _ := repos.Character.Load(ctx, "77")
				after, _, _ := repos.Inventory.Load(ctx, "77")
				if short {
					if got.Success || wallet.Stats["gold"] != gold || !reflect.DeepEqual(after.Slots, bag.Slots) {
						t.Fatalf("short gold changed assets: result=%+v wallet=%+v bag=%+v", got, wallet, after)
					}
				} else if err != nil || !got.Success || wallet.Stats["gold"] != 100 || after.Slots["0:135"].Count != 10 {
					t.Fatalf("amplify payment result=%+v err=%v wallet=%+v bag=%+v", got, err, wallet, after)
				}
			}
		})
	}
}
