package dungeon

import (
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerGrantPickupGoldAdjustmentFailurePreservesWallet(t *testing.T) {
	for _, mode := range []string{"error", "zero", "success", "no_equipment"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seedDungeonOwnerCharacter(t, ctx, repos, 100)
			if mode != "no_equipment" {
				if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{"22": {ItemID: 100341606, SlotIndex: 22}}}); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("reject adjustment")
			called := 0
			result, err := owner.GrantPickupGold(ctx, GoldPickupCommand{CharacterID: "19", Amount: 100, Adjust: func(eq dnfrepo.EquipmentRecord, amount uint32) (uint32, error) {
				called++
				if eq.CharacterID != "19" || amount != 100 {
					t.Fatalf("wrong adjustment inputs: equipment=%+v amount=%d", eq, amount)
				}
				if mode == "no_equipment" {
					if len(eq.Entries) != 0 {
						t.Fatal("unexpected equipment")
					}
					return amount, nil
				}
				if eq.Entries["22"].ItemID != 100341606 {
					t.Fatalf("equipment not loaded: %+v", eq)
				}
				if mode == "error" {
					return 0, sentinel
				}
				if mode == "zero" {
					return 0, nil
				}
				return 130, nil
			}})
			if called != 1 {
				t.Fatalf("callback calls=%d", called)
			}
			want := int64(100)
			switch mode {
			case "error":
				if !errors.Is(err, sentinel) {
					t.Fatalf("error=%v", err)
				}
			case "zero":
				if !errors.Is(err, ErrRewardInvalid) {
					t.Fatalf("error=%v", err)
				}
			default:
				want = 230
				if mode == "no_equipment" {
					want = 200
				}
				if err != nil || result.GoldBefore != 100 || result.GoldAfter != want {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			}
			character, _, err := repos.Character.Load(ctx, "19")
			if err != nil || character.Stats["gold"] != want {
				t.Fatalf("wallet=%d want=%d error=%v", character.Stats["gold"], want, err)
			}
		})
	}
}
