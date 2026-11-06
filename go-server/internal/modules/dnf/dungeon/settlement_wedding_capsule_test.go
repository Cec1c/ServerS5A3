package dungeon

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"testing"
	"time"
)

func TestSettlementWeddingCapsuleSharesClearReceiptAndRollback(t *testing.T) {
	for _, mode := range []string{"married", "single", "not-recommended", "late-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g, cmd := luckyStarSettlementFixture(t, "0")
			cmd.LuckyStar = nil
			cmd.CharacterID = "1"
			cmd.UpdatedAt = time.Unix(1793707200, 0)
			c, _, _ := g.Character.Load(ctx, "c")
			c.CharacterID = "1"
			c.Stats["wedding_v1_phase"] = int64(wedding.Married)
			c.Stats["wedding_v1_partner"] = 2
			c.Stats["wedding_v1_married_at"] = cmd.UpdatedAt.Unix() - 100
			if mode == "single" {
				c.Stats["wedding_v1_phase"], c.Stats["wedding_v1_partner"] = 0, 0
			}
			if err := r.SaveCharacterFields(ctx, g.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
				t.Fatal(err)
			}
			sk, _, _ := g.Skill.Load(ctx, "c")
			sk.CharacterID = "1"
			if err := g.Skill.Save(ctx, sk); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1"}); err != nil {
				t.Fatal(err)
			}
			rules := wedding.CapsuleRules{MaximumOwn: 10, MaximumTaken: 10, Gain: 2, MaximumGauge: 10, ItemID: 10002910}
			cmd.WeddingCapsule = &rules
			if mode == "not-recommended" {
				cmd.RecommendedDungeonClear = false
			}
			if mode == "late-failure" {
				g.CharacterSettlement = weddingCapsuleFailSettlement{g.CharacterSettlement}
			}
			owner, _ := NewOwner(g)
			result, err := owner.CommitSettlement(ctx, cmd)
			if mode == "late-failure" {
				if err == nil {
					t.Fatal("late fault accepted")
				}
				fresh, _, _ := g.Character.Load(ctx, "1")
				state, e := wedding.ReadCapsule(fresh, rules)
				if e != nil || state.Progress != 0 || fresh.Stats["exp"] != 90 {
					t.Fatal("capsule escaped failed settlement", state, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if mode == "married" {
				want = 2
			}
			state, e := wedding.ReadCapsule(result.Character, rules)
			if e != nil || state.Progress != want {
				t.Fatal("unexpected clear capsule", state, e)
			}
			replayed, err := owner.CommitSettlement(ctx, cmd)
			if err != nil || !replayed.Replayed {
				t.Fatal("clear replay failed", err)
			}
			state, e = wedding.ReadCapsule(replayed.Character, rules)
			if e != nil || state.Progress != want {
				t.Fatal("replay granted capsule twice", state, e)
			}
			fresh, _, _ := g.Character.Load(ctx, "1")
			state, e = wedding.ReadCapsule(fresh, rules)
			if e != nil || state.Progress != want {
				t.Fatal("capsule not durably saved with wedding field", state, e)
			}
		})
	}
}

type weddingCapsuleFailSettlement struct {
	r.CharacterSettlementUnitOfWork
}

func (u weddingCapsuleFailSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(r.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx r.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return errors.New("fault after staged capsule and experience writes")
	})
}
