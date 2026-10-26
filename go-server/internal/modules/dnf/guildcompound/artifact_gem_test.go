package guildcompound

import (
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestArtifactGemTerminalRecipeIsNarrow(t *testing.T) {
	for _, name := range []string{"verified", "other_grade", "max_three", "nonzero_rate", "not_terminal", "flag_grade_three"} {
		t.Run(name, func(t *testing.T) {
			c, err := LoadCompoundCatalog(catalogTestSource{text: liveGuildCompoundCatalog})
			if err != nil {
				t.Fatal(err)
			}
			kind, grade := CompoundGem, uint32(3)
			switch name {
			case "other_grade":
				c.gem.rows[len(c.gem.rows)-1].Rarity, grade = 4, 4
			case "max_three":
				c.gem.rows[len(c.gem.rows)-1].MaxMaterialCount = 3
			case "nonzero_rate":
				c.gem.rows[len(c.gem.rows)-1].RatePerMaterial = 1
			case "not_terminal":
				c.gem.rows = append(c.gem.rows, CompoundRecipe{Rarity: 4, MaxMaterialCount: 2})
			case "flag_grade_three":
				kind = CompoundFlag
				c.flag.rows = []CompoundRecipe{{Rarity: 3, MaxMaterialCount: 2}}
			}
			got, ok := c.ResultRarity(kind, grade, 2)
			if ok != (name == "verified") || ok && got != 3 {
				t.Fatalf("result=%d/%t", got, ok)
			}
			if name == "verified" {
				for _, count := range []uint32{0, 1, 3, 4, 5, ^uint32(0)} {
					if _, ok := c.ResultRarity(CompoundGem, 3, count); ok {
						t.Fatal("invalid top-grade count", count)
					}
				}
				r, _ := c.Ladder(CompoundGem).Recipe(3)
				if r.Compoundable() || r.SuccessRateFor(2) != 0 {
					t.Fatal("raw zero-rate row changed")
				}
				if _, ok := c.Ladder(CompoundGem).NextRarity(3); ok {
					t.Fatal("invented upward grade")
				}
			}
		})
	}
}

func TestArtifactGemTwoInputsOneRandomSameGrade(t *testing.T) {
	for _, layout := range []string{"different_templates", "same_template_two_slots", "same_stack"} {
		for pick := uint32(0); pick < 2; pick++ {
			t.Run(layout+string(rune('0'+pick)), func(t *testing.T) {
				entries := map[string]repo.ItemStack{"38:49": {ItemID: 90003, Count: 1}, "38:50": {ItemID: 90013, Count: 1}}
				refs := []uint32{49, 50}
				if layout == "same_template_two_slots" {
					entries["38:50"] = repo.ItemStack{ItemID: 90003, Count: 1}
				}
				if layout == "same_stack" {
					entries = map[string]repo.ItemStack{"38:49": {ItemID: 90003, Count: 2}}
					refs[1] = 49
				}
				f := newOwnerTestFixture(t, entries)
				f.rules.rolls = []uint32{pick}
				cmd := f.command("acct", "77", CompoundGem, 3, 2, refs...)
				result, err := f.owner.Apply(f.ctx, cmd)
				want := []uint32{90003, 90013}[pick]
				if err != nil || !result.Success || result.NextRarity != 3 || result.ResultItem != want || result.ResultSlot != 49 || f.rules.rollIndex != 1 {
					t.Fatalf("result=%+v rolls=%d err=%v", result, f.rules.rollIndex, err)
				}
				inv := f.inventory(t)
				if len(inv.Slots) != 1 || inv.Slots["38:49"].ItemID != int64(want) || inv.Slots["38:49"].Count != 1 {
					t.Fatal("not exactly two-to-one", inv)
				}
				// Same-template output is valid, but one remaining gem cannot satisfy
				// the stale two-material request and must not be granted again.
				if _, err := f.owner.Apply(f.ctx, cmd); !errors.Is(err, ErrInvalid) {
					t.Fatal("stale request accepted", err)
				}
				if !reflect.DeepEqual(inv, f.inventory(t)) || f.rules.rollIndex != 1 {
					t.Fatal("stale request mutated or rerolled")
				}
			})
		}
	}
}

func TestArtifactGemGuardsAndRollback(t *testing.T) {
	for _, name := range []string{"count_one", "count_three", "count_four", "short_stack", "wrong_page", "mixed_grade", "wrong_family", "wrong_request_grade", "locked", "sort_locked", "expired", "foreign_owner", "empty_pool", "duplicate_pool", "wrong_pool_grade", "wrong_pool_family", "roll_out_of_range", "no_space", "grant_error", "grant_count_two", "save_failure"} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerTestFixture(t, map[string]repo.ItemStack{"38:49": {ItemID: 90003, Count: 1}, "38:50": {ItemID: 90013, Count: 1}})
			f.rules.rolls = []uint32{0}
			inv := f.inventory(t)
			cmd := f.command("acct", "77", CompoundGem, 3, 2, 49, 50)
			want := ErrInvalid
			key := ownerTestPoolKey{kind: CompoundGem, rarity: 3}
			stack := inv.Slots["38:50"]
			switch name {
			case "count_one":
				cmd.Request.Count = 1
			case "count_three":
				cmd.Request.Count = 3
			case "count_four":
				cmd.Request.Count = 4
			case "short_stack":
				cmd.Request.MaterialRefs[1] = 49
			case "wrong_page":
				cmd.Request.MaterialRefs[1] = 0
			case "mixed_grade":
				stack.ItemID = 90002
			case "wrong_family":
				stack.ItemID = 100380031
			case "wrong_request_grade":
				cmd.Request.Grade = 2
			case "locked":
				stack.Extra = map[string]string{"equipment_lock_state": "1"}
			case "sort_locked":
				stack.Extra = map[string]string{repo.SortLockExtraKey: "1"}
			case "expired":
				stack.ExpireAt = ownerTestNow
			case "foreign_owner":
				cmd.AccountID = "foreign"
			case "empty_pool":
				f.rules.pools[key] = nil
			case "duplicate_pool":
				f.rules.pools[key] = []uint32{90003, 90003}
			case "wrong_pool_grade":
				f.rules.pools[key] = []uint32{90002}
			case "wrong_pool_family":
				f.rules.pools[key] = []uint32{100380031}
			case "roll_out_of_range":
				f.rules.rolls = []uint32{2}
			case "no_space":
				f.rules.grantErr, want = ErrNoSpace, ErrNoSpace
			case "grant_error":
				want = errors.New("gem grant failure")
				f.rules.grantErr = want
			case "grant_count_two":
				f.rules.grantDelta = 1
			case "save_failure":
				f.owner = &Owner{characterAssets: ownerTestFailingSaveAssets{characters: f.repos.Character, inventory: f.repos.Inventory}}
				want = errOwnerTestSave
			}
			inv.Slots["38:50"] = stack
			if err := f.repos.Inventory.Save(f.ctx, inv); err != nil {
				t.Fatal(err)
			}
			before := f.inventory(t)
			result, err := f.owner.Apply(f.ctx, cmd)
			if !errors.Is(err, want) || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, want)
			}
			if !reflect.DeepEqual(before, f.inventory(t)) {
				t.Fatal("failed compound changed inventory")
			}
		})
	}
}
