package npcshop

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestRankNPCMaterialColorlessExcludesSellingAndFailedPurchase(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, g)
	if err := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "dnf:1", Slots: map[string]repo.ItemStack{"0:358": {ItemID: 3037, Count: 100}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	for _, mode := range []string{"purchase", "sell", "failed"} {
		err = owner.Mutate(ctx, Command{AccountID: "dnf:1", CharacterID: "77", UpdatedAt: at, Project: func(a *Assets) (Changes, error) {
			stack := a.AccountInventory.Slots["0:358"]
			stack.Count -= 10
			a.AccountInventory.Slots["0:358"] = stack
			change := Changes{AccountInventory: true}
			if mode != "sell" {
				change.ColorlessSpent = 10
			}
			if mode == "failed" {
				return change, errors.New("模拟购买失败")
			}
			return change, nil
		}})
		if (mode == "failed") != (err != nil) {
			t.Fatal(mode, err)
		}
	}
	s, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "dnf:1"})
	if err != nil || s.Metrics[repo.ActivityColorless] != 10 {
		t.Fatal(s, err)
	}
	stock, _, _ := g.AccountInventory.Load(ctx, "dnf:1")
	if stock.Slots["0:358"].Count != 80 {
		t.Fatal(stock)
	}
}
