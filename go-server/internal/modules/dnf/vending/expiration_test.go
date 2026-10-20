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

func TestVendingExpiredCoinDoesNotDebitOrGrant(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, deadline := range []time.Time{now.Add(-time.Second), now} {
		repos := memory.NewMemoryGroup()
		repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}})
		repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "a"})
		repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:121": {ItemID: 7454, Count: 1, ExpireAt: deadline}}})
		_, err := Use(ctx, repos, Command{AccountID: "a", CharacterID: "77", CoinID: 7454, CoinCount: 1, CoinSlot: 121, Now: now, Reward: dungeon.CardItemReward{Stack: repo.ItemStack{ItemID: 7463, Count: 1}, Stackable: true, SlotStart: 122, SlotEnd: 122}})
		if err == nil {
			t.Fatalf("expired coin accepted at %v", deadline)
		}
		inventory, _, _ := repos.Inventory.Load(ctx, "77")
		if len(inventory.Slots) != 1 || inventory.Slots["0:121"].Count != 1 {
			t.Fatalf("rejected draw changed inventory: %+v", inventory)
		}
	}
}

func TestVendingChecksLockedCoinBeforeDebit(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repos := memory.NewMemoryGroup()
	repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}})
	repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "a"})
	repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:121": {ItemID: 7454, Count: 1, Extra: map[string]string{"expire_time": "123"}}}})
	checked := false
	cmd := Command{AccountID: "a", CharacterID: "77", CoinID: 7454, CoinCount: 1, CoinSlot: 121, Now: now, Reward: dungeon.CardItemReward{Stack: repo.ItemStack{ItemID: 7463, Count: 1}, Stackable: true, SlotStart: 122, SlotEnd: 122}, ValidateCoin: func(stack repo.ItemStack, at time.Time) error {
		checked = true
		if stack.Extra["expire_time"] != "123" || !at.Equal(now) {
			t.Fatalf("validator not passed locked inventory/source time: %+v %v", stack, at)
		}
		return context.DeadlineExceeded
	}}
	if _, e := Use(ctx, repos, cmd); e != context.DeadlineExceeded || !checked {
		t.Fatalf("locked validation not enforced: checked=%v err=%v", checked, e)
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "77")
	if len(inventory.Slots) != 1 || inventory.Slots["0:121"].Count != 1 {
		t.Fatal("validator failure changed assets")
	}
	cmd.Now = time.Time{}
	checked = false
	if _, e := Use(ctx, repos, cmd); e == nil || checked {
		t.Fatal("zero timestamp reached transaction or accepted")
	}
}

func TestVendingLastCoinConcurrentDrawsCommitOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repos := memory.NewMemoryGroup()
	repos.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}})
	repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "a"})
	repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:121": {ItemID: 7454, Count: 1}}})
	cmd := Command{AccountID: "a", CharacterID: "77", CoinID: 7454, CoinCount: 1, CoinSlot: 121, Now: now, Reward: dungeon.CardItemReward{Stack: repo.ItemStack{ItemID: 7463, Count: 1}, Stackable: true, SlotStart: 122, SlotEnd: 122}}
	results := make(chan error, 16)
	for n := 0; n < 16; n++ {
		go func() { _, err := Use(ctx, repos, cmd); results <- err }()
	}
	successes := 0
	for n := 0; n < 16; n++ {
		if <-results == nil {
			successes++
		}
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "77")
	if successes != 1 || len(inventory.Slots) != 1 || inventory.Slots["0:122"].Count != 1 {
		t.Fatalf("last coin duplicated: successes=%d inventory=%+v", successes, inventory)
	}
}
