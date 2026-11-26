package itemtrade

import (
	"context"
	"errors"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

func TestExchangeRejectsWeaponSkinCatalogProducts(t *testing.T) {
	for _, id := range []uint32{weaponskin.AccountProductID, weaponskin.BeiyunProductID, 490013604, 10309604} {
		t.Run(strconv.FormatUint(uint64(id), 10), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			for _, bag := range []dnfrepo.InventoryRecord{
				{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:97": {ItemID: int64(id), Count: 1}}},
				{CharacterID: "5", Slots: map[string]dnfrepo.ItemStack{}},
			} {
				if err := repos.Inventory.Save(ctx, bag); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.Exchange(ctx,
				Participant{CharacterID: "1", Offers: []Offer{{SourceList: 0, SourceSlot: 97, Count: 1, ExpectedItem: int64(id)}}},
				Participant{CharacterID: "5"},
			)
			if !errors.Is(err, ErrItemNotTradeable) {
				t.Fatalf("外观成品应被拒绝：%v", err)
			}
			source, _, _ := repos.Inventory.Load(ctx, "1")
			target, _, _ := repos.Inventory.Load(ctx, "5")
			if source.Slots["0:97"].Count != 1 || len(target.Slots) != 0 {
				t.Fatalf("拒绝后背包发生改变：来源=%+v 目标=%+v", source.Slots, target.Slots)
			}
		})
	}
}
