package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFGuildContributionCoinExchange(t *testing.T) {
	shop, items := realPVF535(t)
	for _, tc := range []struct {
		id   uint32
		cost int64
	}{{490003479, 2000}, {490003364, 1000}, {490003339, 1000}, {490003363, 300}, {490003340, 200}, {490003268, 40}, {490003267, 20}} {
		t.Run(fmt.Sprint(tc.id), func(t *testing.T) {
			pricing, err := resolveCurrentNPCShopPricing(shop, items, tc.id)
			if err != nil || !pricing.Buyable || pricing.BuyGold != 0 || pricing.NeedMaterialItem != uint32(guild.ContributionCoinID) || pricing.NeedMaterialCount != tc.cost {
				t.Fatal(pricing, err)
			}
			for _, mode := range []string{"buy", "insufficient", "rollback"} {
				t.Run(mode, func(t *testing.T) {
					count := tc.cost + 1
					if mode == "insufficient" {
						count = tc.cost - 1
					}
					repos := mustCurrentNPCShopRepositories(t, 1234, map[string]r.ItemStack{"0:177": {ItemID: guild.ContributionCoinID, Count: count, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[quest]", "pvf_path": "stackable/490003001/chn_490003262.stk"}}})
					before, _, _ := repos.Inventory.Load(context.Background(), "19")
					character, _, _ := repos.Character.Load(context.Background(), "19")
					if mode == "rollback" {
						repos.CharacterSettlement = rejectingCurrentNPCShopCommit{inner: repos.CharacterSettlement, err: errors.New("exchange commit failure")}
					}
					s := currentNPCShopTestService(repos, shop, items)
					_, err := s.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: tc.id, Count: 1})
					after, _, _ := repos.Inventory.Load(context.Background(), "19")
					afterCharacter, _, _ := repos.Character.Load(context.Background(), "19")
					if mode != "buy" {
						if err == nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(character, afterCharacter) {
							t.Fatal("failed exchange changed assets", err)
						}
						return
					}
					if err != nil || after.Slots["0:177"].Count != 1 || afterCharacter.Stats["gold"] != 1234 {
						t.Fatal(after, err)
					}
					assertCurrentNPCShopInventoryAmount(t, after, int64(tc.id), 1)
				})
			}
		})
	}
}
