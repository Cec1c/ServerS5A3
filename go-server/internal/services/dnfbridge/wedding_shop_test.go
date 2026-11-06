package dnfbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingShopUsesNumericLovePointsAndAtomicGoldItemCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		for _, goldPrice := range []string{"0", "100"} {
			t.Run(goldPrice+map[bool]string{false: "/commit", true: "/rollback"}[fail], func(t *testing.T) {
				ctx := context.Background()
				shop, items := mustCurrentNPCShopTestCatalogs(t)
				source := shop.source.(bridgePVFSource)
				source["stackable/material_exchange.stk"] = strings.ReplaceAll(strings.Replace(source["stackable/material_exchange.stk"], "900 2", "10002606 2", 1), "[price]\n100\n", "[price]\n"+goldPrice+"\n")
				repos := mustCurrentNPCShopRepositories(t, 1000, nil)
				c, _, _ := repos.Character.Load(ctx, "19")
				c.Stats["wedding_v1_partner"] = 20
				c.Stats["wedding_v1_phase"] = int64(wedding.Married)
				c.Stats["wedding_v1_love_points"] = 10
				if err := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
					t.Fatal(err)
				}
				if fail {
					repos.AccountAssets = rejectingCurrentNPCShopAccountAssets{inner: repos.AccountAssets, err: errors.New("wedding purchase commit fault")}
				}
				s := currentNPCShopTestService(repos, shop, items)
				result, err := s.commitCurrentNPCShopBuy(ctx, &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: 602, Count: 3})
				if (err != nil) != fail {
					t.Fatalf("purchase %+v %v", result, err)
				}
				after, _, _ := repos.Character.Load(ctx, "19")
				state, err := wedding.Read(after)
				if err != nil {
					t.Fatal(err)
				}
				wantGold, wantPoints, wantItems := int64(1000), int64(4), int64(3)
				if goldPrice == "100" {
					wantGold = 700
				}
				if fail {
					wantGold, wantPoints, wantItems = 1000, 10, 0
				}
				bag, _, _ := repos.Inventory.Load(ctx, "19")
				if after.Stats["gold"] != wantGold || state.LovePoints != wantPoints {
					t.Fatalf("wallet %d points %d want %d/%d", after.Stats["gold"], state.LovePoints, wantGold, wantPoints)
				}
				assertCurrentNPCShopInventoryAmount(t, bag, 602, wantItems)
				assertCurrentNPCShopInventoryAmount(t, bag, wedding.LovePointItemID, 0)
			})
		}
	}
}
