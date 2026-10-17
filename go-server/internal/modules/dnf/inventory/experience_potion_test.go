package inventory

import (
	"context"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestExperiencePotionConsumptionAndEffectAreAtomic(t *testing.T) {
	now := time.Unix(1700000000, 0)
	ctx := context.Background()
	for _, tc := range []string{"normal", "unlimited", "wrong_owner", "low_level", "expired", "missing_transaction", "inventory_save_failure"} {
		t.Run(tc, func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			c := repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Level: 90}
			if tc == "low_level" {
				c.Level = 1
			}
			saveTestCharacter(t, ctx, repos, c)
			kind := "[waste]"
			if tc == "unlimited" {
				kind = "[unlimited waste]"
			}
			stack := repo.ItemStack{ItemID: 7440, Count: 3, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/potion.stk", "stackable_type": kind}}
			if tc == "expired" {
				stack.ExpireAt = now
			}
			saveTestInventory(t, ctx, repos, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:130": stack}})
			request := alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUseStackable), Body: useStackableBody(130, 7440, 4242), AccountID: "acc", SelectedCharacterID: 77, Repositories: repos, ExperienceBuffNow: now,
				ExperienceBuffResolver: func(int64) (experiencebuff.Effect, error) {
					return experiencebuff.Effect{Kind: experiencebuff.Clear, BasisPoints: 10000, DurationMillis: 1800000, CooldownMillis: 1800000, MinimumLevel: 50}, nil
				}}
			if tc == "wrong_owner" {
				request.AccountID = "foreign"
			}
			if tc == "missing_transaction" {
				request.Repositories.CharacterAssets = nil
			}
			if tc == "inventory_save_failure" {
				request.Repositories.CharacterAssets = failPotionInventoryUOW{repos.CharacterAssets}
			}
			if _, err := NewHandler().Handle(ctx, request); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Character.Load(ctx, "77")
			inv := loadTestInventory(t, ctx, repos, "77")
			success := tc == "normal" || tc == "unlimited"
			wantCount := int64(3)
			if tc == "normal" {
				wantCount = 2
			}
			if inv.Slots["0:130"].Count != wantCount {
				t.Fatalf("count=%d want=%d", inv.Slots["0:130"].Count, wantCount)
			}
			bonus := experiencebuff.Bonus(after, experiencebuff.Clear, 1000, now)
			if (bonus == 1000) != success {
				t.Fatalf("bonus=%d success=%v", bonus, success)
			}
			if success {
				NewHandler().Handle(ctx, request)
				inv = loadTestInventory(t, ctx, repos, "77")
				if inv.Slots["0:130"].Count != wantCount {
					t.Fatal("duplicate consumed potion")
				}
			}
		})
	}
}

type failPotionInventoryUOW struct{ repo.CharacterAssetUnitOfWork }

func (u failPotionInventoryUOW) WithinCharacterAssets(ctx context.Context, id string, f func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return f(c, failPotionInventory{i}, e)
	})
}

type failPotionInventory struct{ repo.InventoryRepository }

func (failPotionInventory) Save(context.Context, repo.InventoryRecord) error {
	return errors.New("injected inventory save failure")
}
