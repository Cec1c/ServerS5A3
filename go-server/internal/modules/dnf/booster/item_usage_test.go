package booster

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemusage"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestBoosterUsageCountAndRewardAreAtomic(t *testing.T) {
	for _, mode := range []string{"success", "reward_failure", "exhausted", "counter_save_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := newOwnerTestRepositories(t, "dnf:1")
			d := itemusage.Definition{ItemID: 500, GroupID: -1, Daily: true, DailyLimit: 1, Weekly: true, WeeklyLimit: 2, WeeklyResetDay: time.Thursday}
			now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
			if mode == "exhausted" {
				c, _, _ := g.Character.Load(ctx, "77")
				if err := itemusage.Consume(&c, d, now); err != nil {
					t.Fatal(err)
				}
				if err := g.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			beforeC, _, _ := g.Character.Load(ctx, "77")
			beforeI, _, _ := g.Inventory.Load(ctx, "77")
			use := g
			if mode == "counter_save_failure" {
				use.AccountAssets = failBoosterUsageUOW{g.AccountAssets}
			}
			o, err := NewOwner(use)
			if err != nil {
				t.Fatal(err)
			}
			err = o.Open(ctx, Command{AccountID: "dnf:1", CharacterID: "77", UpdatedAt: now, ItemUsage: &d, Project: func(a *Assets) (Changes, error) {
				delete(a.Inventory.Slots, "0:5")
				a.Inventory.Slots["0:65"] = repo.ItemStack{ItemID: 600, Count: 1}
				if mode == "reward_failure" {
					return Changes{}, errors.New("reward placement failed")
				}
				return Changes{Inventory: true}, nil
			}})
			afterC, _, _ := g.Character.Load(ctx, "77")
			afterI, _, _ := g.Inventory.Load(ctx, "77")
			if mode == "success" {
				a, b := itemusage.Counts(afterC, d, now)
				if err != nil || a != 1 || b != 1 || afterI.Slots["0:65"].ItemID != 600 {
					t.Fatalf("counts=%d/%d err=%v", a, b, err)
				}
			} else if err == nil || !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatalf("failure leaked use or reward: %v", err)
			}
		})
	}
}

type failBoosterUsageUOW struct {
	repo.AccountCharacterAssetUnitOfWork
}

func (u failBoosterUsageUOW) WithinAccountCharacterAssets(ctx context.Context, a, c string, f func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, a, c, func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return f(a, failBoosterUsageCharacter{c}, i, e)
	})
}

type failBoosterUsageCharacter struct{ repo.CharacterRepository }

func (failBoosterUsageCharacter) Save(context.Context, repo.CharacterRecord) error {
	return errors.New("injected booster usage save failure")
}
