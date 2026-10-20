package skill

import "testing"

// The shipped Script.pvf spells 燃烧之血 as skill/swordman/rejection.skl with
// [skill fitness growtype] = 1 even though its maximum-level owner enables
// only grow-type slots 3 (狂战士) and 5. The client and the reference A21
// BuySkill implementation use the maximum-level owner for availability; the
// fitness array is grouping metadata and cannot reject the Berserker branch.
func TestBurningBloodUsesGrowtypeMaximumLevelNotFitnessMetadata(t *testing.T) {
	definition := Skill{
		Job: 0, ID: 248, Path: "skill/swordman/rejection.skl", Name: "燃烧之血",
		Kind: "[passive]", RequiredLevel: 15, MaximumLevel: 1,
		GrowtypeMaximumLevels: []int{0, 0, 0, 1, 0, 1},
		GrowTypes:             []int{1},
	}
	if !definition.SupportsCharacterGrowth(3) {
		t.Fatalf("Berserker grow_type=3 must be available from the maximum-level owner; fitness=%v max=%v",
			definition.GrowTypes, definition.GrowtypeMaximumLevels)
	}
	if definition.SupportsCharacterGrowth(1) {
		t.Fatal("grow_type=1 must remain unavailable: its maximum-level slot is 0")
	}
	if got := definition.MaxLearnableLevel(3, 15); got != 1 {
		t.Fatalf("level-15 Berserker learnable cap = %d, want 1", got)
	}
	if got := definition.MaxLearnableLevel(3, 14); got != 0 {
		t.Fatalf("learnable cap before required level = %d, want 0", got)
	}
}
