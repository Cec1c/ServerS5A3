package equipmentservice

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGrandiPrivateFixedPieceCost007(t *testing.T) {
	for _, equipmentType := range []string{"[weapon]", "[coat]", "[ring]", "[support]"} {
		for _, level := range []int64{70, 85, 90} {
			t.Run(fmt.Sprintf("%s%d", equipmentType, level), func(t *testing.T) {
				ctx := context.Background()
				g, o, catalog, config, _, cmd := fixture(t)
				d := catalog[1001]
				d.Rarity, d.MinimumLevel, d.EquipmentType, d.EpicPieceID = 4, level, equipmentType, 10300000
				catalog[1001] = d
				rule := config.Rules[7]
				rule.EquipmentTypes = map[string]bool{"weapon": true, "coat": true, "ring": true, "support": true}
				rule.EpicPieceFixedCost = 100
				config.Rules[7] = rule
				result, err := o.Transfer(ctx, catalog, config, cmd)
				if err != nil {
					t.Fatal(err)
				}
				if result.EpicBalance != 900 || result.EpicPieceID != 10300000 {
					t.Fatalf("fixed debit=%+v", result)
				}
				account, _, _ := g.Account.Load(ctx, "a")
				if account.Metadata["other"] != "keep" || account.Metadata[r.AccountEpicPieceMetadataKey(10300000)] != "900" {
					t.Fatal(account.Metadata)
				}
			})
		}
	}
	ctx := context.Background()
	g, o, catalog, config, _, cmd := fixture(t)
	d := catalog[1001]
	d.Rarity, d.EpicPieceID = 4, 10300000
	catalog[1001] = d
	rule := config.Rules[7]
	rule.EpicPieceFixedCost = 100
	config.Rules[7] = rule
	account, _, _ := g.Account.Load(ctx, "a")
	account.Metadata[r.AccountEpicPieceMetadataKey(10300000)] = "99"
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Inventory.Load(ctx, "19")
	shared, _, _ := g.AccountInventory.Load(ctx, "a")
	if _, err := o.Transfer(ctx, catalog, config, cmd); err == nil {
		t.Fatal("99 pieces accepted")
	}
	after, _, _ := g.Inventory.Load(ctx, "19")
	afterShared, _, _ := g.AccountInventory.Load(ctx, "a")
	afterAccount, _, _ := g.Account.Load(ctx, "a")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(shared, afterShared) || !reflect.DeepEqual(account, afterAccount) {
		t.Fatal("insufficient pieces changed assets")
	}
}
