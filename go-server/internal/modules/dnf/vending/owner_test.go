package vending

import (
	"context"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"strconv"
	"testing"
	"time"
)

func TestVendingRequiresBlackDiamondAndRollsBackCoinWhenRewardCannotFit(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Now()
	account := repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{}}
	repos.Account.Save(ctx, account)
	repos.Character.Save(ctx, repo.CharacterRecord{AccountID: account.AccountID, CharacterID: "99"})
	repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:10": {ItemID: 7454, Count: 2}, "0:11": {ItemID: 7000, Count: 1}}})
	cmd := Command{AccountID: account.AccountID, CharacterID: "99", CoinID: 7454, CoinCount: 1, CoinSlot: 10, Now: now, Reward: dungeon.CardItemReward{Stack: repo.ItemStack{ItemID: 7463, Count: 4}, Stackable: true, SlotStart: 11, SlotEnd: 11}}
	if _, e := Use(ctx, repos, cmd); e == nil {
		t.Fatal("ordinary account used black diamond machine")
	}
	account.Metadata[premium.MetadataKey(premium.TypeBlackDiamond)] = strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	repos.Account.Save(ctx, account)
	if _, e := Use(ctx, repos, cmd); e == nil {
		t.Fatal("full destination did not reject")
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "99")
	if inventory.Slots["0:10"].Count != 2 {
		t.Fatal("failed reward consumed coin")
	}
	delete(inventory.Slots, "0:11")
	repos.Inventory.Save(ctx, inventory)
	result, e := Use(ctx, repos, cmd)
	if e != nil {
		t.Fatal(e)
	}
	inventory, _, _ = repos.Inventory.Load(ctx, "99")
	if result.CoinRemaining != 1 || result.RewardSlot != 11 || inventory.Slots["0:11"].ItemID != 7463 || inventory.Slots["0:11"].Count != 4 {
		t.Fatalf("incorrect reward commit: %+v %+v", result, inventory)
	}
}
