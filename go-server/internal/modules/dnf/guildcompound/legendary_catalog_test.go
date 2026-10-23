package guildcompound

import "testing"

func TestLegendaryMedalResultRarityIsNarrow(t *testing.T) {
	c, err := LoadCompoundCatalog(catalogTestSource{text: liveGuildCompoundCatalog})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []CompoundKind{CompoundGem, CompoundFlag} {
		for _, grade := range c.Ladder(kind).Rarities() {
			for count := uint32(0); count <= 5; count++ {
				recipe, _ := c.Ladder(kind).Recipe(grade)
				want, ok := c.Ladder(kind).NextRarity(grade)
				ok = ok && recipe.AcceptsMaterialCount(count)
				if kind == CompoundFlag && grade == 6 && count == 2 {
					want, ok = 6, true
				}
				if kind == CompoundGem && grade == 3 && count == 2 {
					want, ok = 3, true
				}
				got, allowed := c.ResultRarity(kind, grade, count)
				if allowed != ok || allowed && got != want {
					t.Fatalf("kind=%v grade=%d count=%d got=%d/%t want=%d/%t", kind, grade, count, got, allowed, want, ok)
				}
			}
		}
	}
	for _, kind := range []CompoundKind{CompoundGem, CompoundFlag, CompoundKind(99)} {
		if _, ok := c.ResultRarity(kind, 99, 2); ok {
			t.Fatal("unknown grade accepted")
		}
	}
	if _, ok := c.ResultRarity(CompoundKind(99), 6, 2); ok {
		t.Fatal("unknown kind accepted")
	}
	if _, ok := c.ResultRarity(CompoundFlag, 6, ^uint32(0)); ok {
		t.Fatal("oversized count accepted")
	}
	// The historical raw recipe and upward-only ladder retain their semantics.
	r, _ := c.Ladder(CompoundFlag).Recipe(6)
	if r.Compoundable() || r.SuccessRateFor(2) != 0 {
		t.Fatal("raw terminal rate changed")
	}
	if _, ok := c.Ladder(CompoundFlag).NextRarity(6); ok {
		t.Fatal("invented next grade")
	}
}

func TestLegendaryMedalDoesNotEnableOtherTerminalRows(t *testing.T) {
	for _, name := range []string{"other_grade", "max_three", "nonzero_rate", "not_terminal", "gem_grade_six"} {
		t.Run(name, func(t *testing.T) {
			c, err := LoadCompoundCatalog(catalogTestSource{text: liveGuildCompoundCatalog})
			if err != nil {
				t.Fatal(err)
			}
			kind, grade := CompoundFlag, uint32(6)
			switch name {
			case "other_grade":
				c.flag.rows[len(c.flag.rows)-1].Rarity = 7
				grade = 7
			case "max_three":
				c.flag.rows[len(c.flag.rows)-1].MaxMaterialCount = 3
			case "nonzero_rate":
				c.flag.rows[len(c.flag.rows)-1].RatePerMaterial = 1
			case "not_terminal":
				c.flag.rows = append(c.flag.rows, CompoundRecipe{Rarity: 7, MaxMaterialCount: 2})
			case "gem_grade_six":
				c.gem.rows[len(c.gem.rows)-1].Rarity = 6
				kind = CompoundGem
			}
			if _, ok := c.ResultRarity(kind, grade, 2); ok {
				t.Fatal("unverified terminal recipe enabled")
			}
		})
	}
}
