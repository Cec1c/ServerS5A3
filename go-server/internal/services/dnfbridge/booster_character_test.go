package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCharacterBoosterFiltersEachJobAndLevelBlock(t *testing.T) {
	doc, err := pvf.Parse("box.stk", "[booster info]\n[etc]\n1 100 100000 10\n[/etc]\n[charactor]\n`[swordman]` 1 99 1 101 100000 1\n[/charactor]\n[charactor]\n`[at swordman]` 30 90 1 102 100000 2\n[/charactor]\n[charactor]\n`[at swordman]` 30 90 1 103 100000 1\n[/charactor]\n[/booster info]")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		job   string
		level int
		ids   []int64
	}{
		{"0", 1, []int64{100, 101}},
		{"11", 29, []int64{100}},
		{"11", 30, []int64{100, 102, 103}},
		{"11", 90, []int64{100, 102, 103}},
		{"11", 91, []int64{100}},
		{"14", 90, []int64{100}},
	} {
		groups, err := parseMagicBoxBoosterGroupsForCharacter(doc, &r.CharacterRecord{Job: tc.job, Level: tc.level})
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, g := range groups {
			if g.DrawCount != 1 || len(g.Entries) != 1 {
				t.Fatalf("bad groups: %+v", groups)
			}
			ids = append(ids, g.Entries[0].ItemID)
		}
		if !reflect.DeepEqual(ids, tc.ids) {
			t.Fatalf("job=%s level=%d got=%v want=%v", tc.job, tc.level, ids, tc.ids)
		}
	}
	if _, err := parseMagicBoxBoosterGroups(doc); err == nil {
		t.Fatal("unbound inventory caller received all jobs")
	}
}

func TestCharacterBoosterRejectsInvalidTablesAndChangedCharacterWithoutConsuming(t *testing.T) {
	for _, row := range []string{
		"`[at swordman]`",
		"`[at swordman]` 1",
		"`[unknown]` 1 99 1 100 100000 1",
		"`[at swordman]` 99 1 1 100 100000 1",
		"`[at swordman]` 1 99 1 100 100000",
		"`[at swordman]` 1 99 1 100 -1 1",
		"`[at swordman]` 1 99 1 100 100000 0",
	} {
		doc, err := pvf.Parse("box.stk", "[booster info]\n[charactor]\n"+row+"\n[/charactor]\n[/booster info]")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseMagicBoxBoosterGroupsForCharacter(doc, &r.CharacterRecord{Job: "0", Level: 90}); err == nil {
			t.Fatalf("invalid unmatched table accepted: %s", row)
		}
	}
	catalog := mustCurrentBoosterTestCatalog(t)
	for _, changed := range []r.CharacterRecord{
		{CharacterID: "19", AccountID: "account-1", Job: "11", Level: 90},
		{CharacterID: "19", AccountID: "account-1", Job: "0", Level: 91},
	} {
		g := memory.NewMemoryGroup()
		ctx := context.Background()
		if err := g.Character.Save(ctx, changed); err != nil {
			t.Fatal(err)
		}
		before := r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:75": {ItemID: 500, Count: 1}}}
		if err := g.Inventory.Save(ctx, before); err != nil {
			t.Fatal(err)
		}
		definition, err := resolveCurrentBoosterDefinition(catalog, 500, currentBoosterRequestRandom)
		if err != nil {
			t.Fatal(err)
		}
		definition.Random = alignedcmd.MagicBoxResolution{Kind: "random", Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 600, Weight: 100000, Count: 1}}}}}
		definition.RandomCharacter = &currentBoosterRewardCharacter{Job: "0", Level: 90}
		s := currentBoosterTestService(catalog, g)
		if _, err := s.commitCurrentBooster(ctx, &gameSession{selectedCharacterID: 19}, catalog, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75}); err == nil {
			t.Fatal("stale job/level consumed box")
		}
		after, _, err := g.Inventory.Load(ctx, "19")
		if err != nil || !reflect.DeepEqual(before.Slots, after.Slots) {
			t.Fatalf("failed open changed inventory: %+v %v", after, err)
		}
	}
}
