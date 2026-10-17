package inventory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
)

func TestAmplifyAuthoritativeFeeRollbackAndPortableBoundary(t *testing.T) {
	for _, name := range []string{"ordinary", "short gold", "quote failure", "late save failure", "shared late save failure", "portable"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			seedUpgradeLateFailureWalletFixture(t, ctx, repos)
			bag := loadTestInventory(t, ctx, repos, "77")
			target := bag.Slots["0:9"]
			target.Extra["amplify_type"] = "3"
			target.Extra["amplify_value"] = "5"
			bag.Slots["0:9"] = target
			cmd := Command{Mode: "amplify", AccountID: "acc", SelectedCharacterID: 77, TargetSlotIndex: 9, TargetItemTemplateID: 700, MaterialSlotIndex: 10, OptionalTicketSlot: -1, UpgradeSuccessWeight: 100000, UpgradeMaterialItemID: 3242, UpgradeMaterialCount: 10}
			if name == "portable" {
				bag.Slots["0:10"] = portableAmplifierStack(2)
				cmd.UpgradePortable = portableAmplifyConsumable()
			}
			saveTestInventory(t, ctx, repos, bag)
			before := loadTestInventory(t, ctx, repos, "77")
			var probe *upgradeLateFailureProbe
			if name == "late save failure" {
				probe = &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}
				repos.CharacterAssets = upgradeLateFailureAssets{base: repos.CharacterAssets, probe: probe}
			}
			if name == "shared late save failure" {
				if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "acc", Slots: map[string]dnfrepo.ItemStack{dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 12}}}); err != nil {
					t.Fatal(err)
				}
				probe = &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}
				repos.AccountAssets = upgradeLateFailureAccountAssets{base: repos.AccountAssets, probe: probe}
				cmd.MaterialSlotIndex = 358
				cmd.UpgradeMaterialItemID = 3037
			}
			calls := 0
			cmd.UpgradeCostResolver = func(mode string, item int64, level int, _ bool, _ uint16) (alignedcmd.UpgradeCostResolution, error) {
				calls++
				if mode != "amplify" || item != 700 || level != 0 {
					t.Fatalf("wrong quote context %s %d %d", mode, item, level)
				}
				if name == "quote failure" || name == "portable" {
					return alignedcmd.UpgradeCostResolution{}, errors.New("quote unavailable")
				}
				fee := int64(800)
				if name == "short gold" {
					fee = 12346
				}
				return alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: fee}, nil
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			got, err := owner.Upgrade(ctx, cmd)
			wallet, _, _ := repos.Character.Load(ctx, "77")
			after := loadTestInventory(t, ctx, repos, "77")
			if name == "portable" {
				if calls != 0 || err != nil || !got.Success || wallet.Stats["gold"] != 12345 || after.Slots["0:10"].Count != 1 {
					t.Fatalf("portable charged: calls=%d result=%+v err=%v wallet=%+v", calls, got, err, wallet)
				}
				return
			}
			if calls != 1 {
				t.Fatalf("quote calls=%d", calls)
			}
			if name == "ordinary" {
				if err != nil || !got.Success || wallet.Stats["gold"] != 11545 || after.Slots["0:10"].Count != 1 || upgradeLevelOf(after.Slots["0:9"]) != 1 {
					t.Fatalf("result=%+v err=%v wallet=%+v", got, err, wallet)
				}
				return
			}
			if got.Success || wallet.Stats["gold"] != 12345 || !reflect.DeepEqual(before.Slots, after.Slots) {
				t.Fatalf("failed amplify changed assets: %+v wallet=%+v bag=%+v", got, wallet, after)
			}
			if name == "short gold" {
				if got.ErrorCode != upgradeErrorInsufficientGold || got.UpgradeSuccessWeight != 0 {
					t.Fatalf("short gold reached RNG stage: %+v", got)
				}
			} else if err == nil {
				t.Fatal("lost quote/save error")
			}
			if probe != nil && (!probe.goldSeen || probe.goldAfterApply != 11545 || probe.bagSaveAttempts != 1) {
				t.Fatalf("late failure not reached after debit: %+v", probe)
			}
			if name == "shared late save failure" {
				shared, _, _ := repos.AccountInventory.Load(ctx, "acc")
				if shared.Slots[dnfrepo.AccountSharedInventorySlotKey(358)].Count != 12 {
					t.Fatal("shared material not rolled back")
				}
			}
		})
	}
}
