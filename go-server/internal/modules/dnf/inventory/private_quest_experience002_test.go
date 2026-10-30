package inventory

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPrivateQuestExperience002AtomicConsumption(t *testing.T) {
	for _, mode := range []string{"success", "save_failure", "foreign_owner", "expired", "stale_session", "missing_transaction"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			g := memory.NewMemoryGroup()
			saveTestCharacter(t, ctx, g, repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Level: 45})
			stack := repo.ItemStack{ItemID: 10304122, Count: 3, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/potion.stk"}}
			if mode == "expired" {
				stack.ExpireAt = now
			}
			saveTestInventory(t, ctx, g, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:130": stack}})
			if mode == "save_failure" {
				g.CharacterAssets = failPotionInventoryUOW{g.CharacterAssets}
			}
			if mode == "missing_transaction" {
				g.CharacterAssets = nil
			}
			owner, err := NewOwner(g)
			if err != nil {
				t.Fatal(err)
			}
			r := QuestExperiencePotionRequest{AccountID: "acc", CharacterID: "77", PVFPath: "stackable/potion.stk", SlotIndex: 130, ItemID: 10304122, Effect: experiencebuff.Effect{Kind: experiencebuff.Quest, BasisPoints: 10000, DurationMillis: 1800000, CooldownMillis: 1800000, MinimumLevel: 1}, Revalidate: func() bool { return mode != "stale_session" }}
			if mode == "foreign_owner" {
				r.AccountID = "foreign"
			}
			before, _, _ := g.Character.Load(ctx, "77")
			remaining, err := owner.UseQuestExperiencePotion(ctx, r, now)
			after, _, _ := g.Character.Load(ctx, "77")
			bag := loadTestInventory(t, ctx, g, "77")
			if mode != "success" {
				if err == nil || bag.Slots["0:130"].Count != 3 || !reflect.DeepEqual(before.Stats, after.Stats) {
					t.Fatalf("failed use not atomic: %v", err)
				}
				return
			}
			if err != nil || remaining != 2 || bag.Slots["0:130"].Count != 2 || experiencebuff.Bonus(after, experiencebuff.Quest, 1000, now) != 1000 {
				t.Fatalf("use: %v", err)
			}
			if _, err = owner.UseQuestExperiencePotion(ctx, r, now); err == nil {
				t.Fatal("cooldown replay accepted")
			}
			bag = loadTestInventory(t, ctx, g, "77")
			if bag.Slots["0:130"].Count != 2 {
				t.Fatal("replay consumed")
			}
		})
	}
}
