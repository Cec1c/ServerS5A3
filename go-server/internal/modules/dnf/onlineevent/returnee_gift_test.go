package onlineevent

import (
	"context"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestReturneeGiftConsumptionAndLimit(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	if e := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "7", AccountID: "a", Level: 20, Stats: map[string]int64{"is_return_user": 1, "seria_returnee_active_until": now.Add(30 * 24 * time.Hour).Unix()}}); e != nil {
		t.Fatal(e)
	}
	if e := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"0:354": {ItemID: 3037, Count: 600}}}); e != nil {
		t.Fatal(e)
	}
	r := ReturneeGiftRules{NPC: 2, Level: 20, Count: 100, Limit: 5, MaxPoints: 40000, Points: map[int64][2]int64{3037: {100, 300}}}
	for i := 1; i <= 5; i++ {
		got, e := GiveReturneeNPCGift(ctx, repos, r, "a", "7", 36, 354, now)
		if e != nil {
			t.Fatal(e)
		}
		if got.Remaining != int64(600-100*i) || got.Used != int64(i) {
			t.Fatalf("result=%+v", got)
		}
	}
	if _, e := GiveReturneeNPCGift(ctx, repos, r, "a", "7", 36, 354, now); e == nil {
		t.Fatal("daily limit ignored")
	}
	ch, _, _ := repos.Character.Load(ctx, "7")
	if ch.Stats[ReturneeGiftCountKey] != 5 {
		t.Fatal("gift mission counter")
	}
	if _, e := GiveReturneeNPCGift(ctx, repos, r, "foreign", "7", 36, 354, now.Add(24*time.Hour)); e == nil {
		t.Fatal("ownership ignored")
	}
	if _, e := GiveReturneeNPCGift(ctx, repos, r, "a", "7", 36, 354, now.Add(24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e := GiveReturneeNPCGift(ctx, repos, r, "a", "7", 36, 354, now.Add(24*time.Hour)); e == nil {
		t.Fatal("spent absent cubes")
	}
}

func TestNPCGiftBackpackAndFailuresAreAtomic(t *testing.T) {
	ctx := context.Background()
	r := ReturneeGiftRules{NPC: 2, Level: 20, Count: 100, Limit: 5, MaxPoints: 40000, Points: map[int64][2]int64{3033: {200, 600}}}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name              string
		level             int
		count, item, used int64
		want              error
	}{
		{"success", 37, 100, 3033, 0, nil},
		{"too few", 37, 99, 3033, 0, ErrNPCGiftCount},
		{"wrong item", 37, 100, 100, 0, ErrNPCGiftItem},
		{"low level", 19, 100, 3033, 0, ErrNPCGiftLevel},
		{"daily limit", 37, 100, 3033, 5, ErrNPCGiftLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			ch := dnfrepo.CharacterRecord{AccountID: "a", CharacterID: "7", Level: tc.level, Stats: map[string]int64{"npc_favor_2_day": 20260912, "npc_favor_2_gifts": tc.used, "npc_favor_2_points": 39900}}
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "7", Slots: map[string]dnfrepo.ItemStack{"0:61": {ItemID: tc.item, Count: tc.count}}}); err != nil {
				t.Fatal(err)
			}
			got, err := GiveReturneeNPCGift(ctx, repos, r, "a", "7", 0, 61, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "7")
			stored, _, _ := repos.Character.Load(ctx, "7")
			if tc.want != nil {
				if inventory.Slots["0:61"].Count != tc.count || stored.Stats["npc_favor_2_gifts"] != tc.used || stored.Stats["npc_favor_2_points"] != 39900 {
					t.Fatal("rejection changed assets")
				}
			} else if got.Added != 100 || got.Points != 40000 || len(inventory.Slots) != 0 || got.Used != 1 {
				t.Fatalf("result=%+v slots=%v", got, inventory.Slots)
			}
		})
	}
}
