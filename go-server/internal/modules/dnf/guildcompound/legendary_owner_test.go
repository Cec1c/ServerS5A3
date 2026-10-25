package guildcompound

import (
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func legendaryOwnerFixture(t *testing.T, sameID bool) *ownerTestFixture {
	t.Helper()
	second := int64(100380052)
	if sameID {
		second = 100380051
	}
	return newOwnerTestFixture(t, map[string]repo.ItemStack{
		"38:0": {ItemID: 100380051, Count: 1},
		"38:1": {ItemID: second, Count: 1},
	}, func(r *ownerTestRules) {
		for _, id := range []uint32{100380051, 100380052, 100380053} {
			r.defs[id] = MaterialDefinition{Family: MaterialFamilyGuildMedal, Rarity: 6}
		}
		r.pools[ownerTestPoolKey{kind: CompoundFlag, rarity: 6}] = []uint32{100380051, 100380052, 100380053}
	})
}

func TestLegendaryMedalTwoInputsOneRandomSameGrade(t *testing.T) {
	for _, sameID := range []bool{false, true} {
		for pick := uint32(0); pick < 3; pick++ {
			f := legendaryOwnerFixture(t, sameID)
			f.rules.rolls = []uint32{pick}
			result, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundFlag, 6, 2, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			want := uint32(100380051) + pick
			if !result.Success || result.NextRarity != 6 || result.ResultItem != want || result.ResultSlot != 0 || f.rules.rollIndex != 1 {
				t.Fatalf("sameID=%t pick=%d result=%+v rolls=%d", sameID, pick, result, f.rules.rollIndex)
			}
			inv := f.inventory(t)
			if len(inv.Slots) != 1 || inv.Slots["38:0"].ItemID != int64(want) || inv.Slots["38:0"].Count != 1 {
				t.Fatalf("two-to-one inventory: %+v", inv)
			}
			if len(result.Changed) != 2 || result.Changed[0].ItemID != int64(want) || result.Changed[1].ItemID != 0 {
				t.Fatalf("changed rows=%+v", result.Changed)
			}
			// The selected source template remains a legitimate random outcome.
			// A repeated request now lacks its second distinct material instance.
			before := f.inventory(t)
			if _, err := f.owner.Apply(f.ctx, f.command("acct", "77", CompoundFlag, 6, 2, 0, 1)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("repeat error=%v", err)
			}
			if !reflect.DeepEqual(before, f.inventory(t)) {
				t.Fatal("repeat mutated inventory")
			}
		}
	}
}

func TestLegendaryMedalGuardsAndRollback(t *testing.T) {
	for _, name := range []string{
		"count_one", "count_three", "count_four", "duplicate_slot", "wrong_page", "missing_slot",
		"wrong_request_grade", "mixed_grade", "wrong_family", "stacked_medal", "locked", "sort_locked",
		"expired", "custom_expired", "foreign_owner", "empty_pool", "duplicate_pool", "wrong_pool_grade",
		"wrong_pool_family", "roll_out_of_range", "grant_no_space", "grant_error", "grant_count_two", "save_failure",
	} {
		t.Run(name, func(t *testing.T) {
			f := legendaryOwnerFixture(t, false)
			inv := f.inventory(t)
			cmd := f.command("acct", "77", CompoundFlag, 6, 2, 0, 1)
			want := ErrInvalid
			poolKey := ownerTestPoolKey{kind: CompoundFlag, rarity: 6}
			switch name {
			case "count_one":
				cmd.Request.Count = 1
			case "count_three":
				cmd.Request.Count = 3
			case "count_four":
				cmd.Request.Count = 4
			case "duplicate_slot":
				cmd.Request.MaterialRefs[1] = 0
			case "wrong_page":
				cmd.Request.MaterialRefs[1] = 49
			case "missing_slot":
				delete(inv.Slots, "38:1")
			case "wrong_request_grade":
				cmd.Request.Grade = 3
			case "mixed_grade":
				s := inv.Slots["38:1"]
				s.ItemID = 100380031
				inv.Slots["38:1"] = s
			case "wrong_family":
				s := inv.Slots["38:1"]
				s.ItemID = 90003
				inv.Slots["38:1"] = s
			case "stacked_medal":
				s := inv.Slots["38:1"]
				s.Count = 2
				inv.Slots["38:1"] = s
			case "locked":
				s := inv.Slots["38:1"]
				s.Extra = map[string]string{"equipment_lock_state": "1"}
				inv.Slots["38:1"] = s
			case "sort_locked":
				s := inv.Slots["38:1"]
				s.Extra = map[string]string{repo.SortLockExtraKey: "1"}
				inv.Slots["38:1"] = s
			case "expired":
				s := inv.Slots["38:1"]
				s.ExpireAt = ownerTestNow
				inv.Slots["38:1"] = s
			case "custom_expired":
				f.rules.expiredAll = true
			case "foreign_owner":
				cmd.AccountID = "someone-else"
			case "empty_pool":
				f.rules.pools[poolKey] = nil
			case "duplicate_pool":
				f.rules.pools[poolKey] = []uint32{100380051, 100380051}
			case "wrong_pool_grade":
				f.rules.pools[poolKey] = []uint32{100380031}
			case "wrong_pool_family":
				f.rules.pools[poolKey] = []uint32{90003}
			case "roll_out_of_range":
				f.rules.rolls = []uint32{3}
			case "grant_no_space":
				f.rules.grantErr = ErrNoSpace
				want = ErrNoSpace
			case "grant_error":
				want = errors.New("legendary grant failure")
				f.rules.grantErr = want
			case "grant_count_two":
				f.rules.grantDelta = 1
			case "save_failure":
				f.owner = &Owner{characterAssets: ownerTestFailingSaveAssets{characters: f.repos.Character, inventory: f.repos.Inventory}}
				want = errOwnerTestSave
			}
			if err := f.repos.Inventory.Save(f.ctx, inv); err != nil {
				t.Fatal(err)
			}
			before := f.inventory(t)
			result, err := f.owner.Apply(f.ctx, cmd)
			if !errors.Is(err, want) || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, want)
			}
			if !reflect.DeepEqual(before, f.inventory(t)) {
				t.Fatal("failure changed durable inventory")
			}
		})
	}
}
