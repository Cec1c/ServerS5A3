package skillcmd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

func TestDarkKnightLearnLayoutNewSkillUsesTree(t *testing.T) {
	ctx := context.Background()
	catalog := darkKnightLearnCatalog(t)
	for tree := byte(0); tree < 2; tree++ {
		t.Run(fmt.Sprintf("tree_%d", tree), func(t *testing.T) {
			states, layout := darkKnightLearnRoots()
			repos := darkKnightLearnSeed(t, darkcombo.Job, tree, states, layout)
			result, err := darkKnightLearnOwner(t, repos, catalog).ApplyBuy(ctx, NewBuyCommand(alignedRequest("dark-knight-layout", 31), BuySkillRequest{
				RawSkillTree: tree, Count: 1, Entries: []BuySkillEntry{{SkillID: 1000, LevelDelta: 1}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Entries) != 1 || result.Entries[0].Slot != 150 || result.Entries[0].Level != 1 {
				t.Fatalf("new skill = %+v, want skill-tree slot 150", result.Entries)
			}
			got := darkKnightLearnLoad(t, repos)
			if got.Layouts[int(tree)][150] != 1000 || got.Points.RemainingSP != 90 {
				t.Fatalf("persisted new skill = %+v", got)
			}
			darkKnightLearnCheckLayout(t, got.Layouts[int(tree)], 1)
		})
	}
}

func TestDarkKnightLearnLayoutWholePageAndInitial(t *testing.T) {
	catalog := darkKnightLearnCatalog(t)
	for _, count := range []int{1, 49, 192} {
		for tree := 0; tree < 2; tree++ {
			for _, initial := range []bool{false, true} {
				t.Run(fmt.Sprintf("count_%d/tree_%d/initial_%t", count, tree, initial), func(t *testing.T) {
					states, _ := darkKnightLearnRoots()
					for i := 0; i < count; i++ {
						states[int64(1000+i)] = dnfrepo.SkillState{Level: 1, Enabled: true}
					}
					before := dnfrepo.CloneSkill(dnfrepo.SkillRecord{Skills: states}).Skills
					var layout dnfrepo.SkillLayout
					var err error
					if initial {
						layout, err = BuildInitialSkillLayout(catalog, darkcombo.Job, tree, states)
					} else {
						layout, err = BuildCurrentSkillLayout(catalog, darkcombo.Job, tree, states, nil)
					}
					if err != nil {
						t.Fatal(err)
					}
					darkKnightLearnCheckLayout(t, layout, count)
					if layout[150] != 1000 || (count > 48 && layout[6] != 1048) {
						t.Fatalf("active-group preference/fallback = %+v", layout)
					}
					again, err := BuildCurrentSkillLayout(catalog, darkcombo.Job, tree, states, layout)
					if err != nil || !reflect.DeepEqual(again, layout) || !reflect.DeepEqual(states, before) {
						t.Fatalf("rebuild/input changed: err=%v layout=%+v again=%+v", err, layout, again)
					}
				})
			}
		}
	}
}

func TestDarkKnightLearnLayoutManualExtensionSurvivesReload(t *testing.T) {
	ctx := context.Background()
	catalog := darkKnightLearnCatalog(t)
	for tree := byte(0); tree < 2; tree++ {
		t.Run(fmt.Sprintf("tree_%d", tree), func(t *testing.T) {
			states, layout := darkKnightLearnRoots()
			for i := 0; i < 6; i++ {
				states[int64(1000+i)] = dnfrepo.SkillState{Level: 1, Enabled: true}
				layout[150+i] = uint16(1000 + i)
			}
			repos := darkKnightLearnSeed(t, darkcombo.Job, tree, states, layout)
			record := darkKnightLearnLoad(t, repos)
			record.Cooldowns = map[int64]time.Time{1000: time.Unix(1234, 0).UTC()}
			if err := repos.Skill.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			owner := darkKnightLearnOwner(t, repos, catalog)
			for i := 0; i < 6; i++ {
				_, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("dark-knight-layout", 31), ChangeSkillSlotRequest{
					SkillTree: tree, From: byte(150 + i), To: byte(198 + i), ContextIndex: -1, Mode: 2,
				}))
				if err != nil {
					t.Fatalf("manual slot %d: %v", 198+i, err)
				}
			}
			missing := darkKnightLearnLoad(t, repos)
			missing.Skills[1006] = dnfrepo.SkillState{Level: 1, Enabled: true}
			assigned, err := BuildCurrentSkillLayout(catalog, darkcombo.Job, int(tree), missing.Skills, missing.Layouts[int(tree)])
			if err != nil || assigned[150] != 1006 || len(assigned) != 13 {
				t.Fatalf("unassigned skill with manual extension bindings: layout=%+v err=%v", assigned, err)
			}
			for i := 0; i < 6; i++ {
				if assigned[198+i] != uint16(1000+i) {
					t.Fatalf("assignment changed manual slot %d: %+v", 198+i, assigned)
				}
			}
			// Recreate the owner and read persisted state as the login path does;
			// no previous owner or request carries the manual bindings forward.
			owner = darkKnightLearnOwner(t, repos, catalog)
			result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("dark-knight-layout", 31), BuySkillRequest{
				RawSkillTree: tree, Count: 2, Entries: []BuySkillEntry{{SkillID: 1000, LevelDelta: 1}, {SkillID: 1006, LevelDelta: 1}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Entries) != 2 || result.Entries[0].Slot != 198 || result.Entries[1].Slot != 150 {
				t.Fatalf("upgrade/new learn slots = %+v", result.Entries)
			}
			persisted := darkKnightLearnLoad(t, repos)
			rebuilt, err := BuildCurrentSkillLayout(catalog, darkcombo.Job, int(tree), persisted.Skills, persisted.Layouts[int(tree)])
			if err != nil || !reflect.DeepEqual(rebuilt, persisted.Layouts[int(tree)]) {
				t.Fatalf("reload layout changed: %v, %+v", err, rebuilt)
			}
			for i := 0; i < 6; i++ {
				if rebuilt[198+i] != uint16(1000+i) {
					t.Fatalf("manual extension %d lost: %+v", 198+i, rebuilt)
				}
			}
			if len(rebuilt) != 13 || rebuilt[150] != 1006 || !reflect.DeepEqual(persisted.Cooldowns, record.Cooldowns) {
				t.Fatalf("learn/reload changed layout or cooldowns: %+v", persisted)
			}
		})
	}
}

func TestDarkKnightLearnLayoutFullTreeRollsBackBatch(t *testing.T) {
	ctx := context.Background()
	catalog := darkKnightLearnCatalog(t)
	states, layout := darkKnightLearnRoots()
	for slot := 6; slot < 198; slot++ {
		id := uint16(1000 + slot - 6)
		states[int64(id)] = dnfrepo.SkillState{Level: 1, Enabled: true}
		layout[slot] = id
	}
	repos := darkKnightLearnSeed(t, darkcombo.Job, 0, states, layout)
	before := darkKnightLearnLoad(t, repos)
	// The first mutation can succeed; the following new skill has no tree
	// slot even though every extension shortcut is free. Roll back both.
	_, err := darkKnightLearnOwner(t, repos, catalog).ApplyBuy(ctx, NewBuyCommand(alignedRequest("dark-knight-layout", 31), BuySkillRequest{
		Count: 2, Entries: []BuySkillEntry{{SkillID: 1000, LevelDelta: 1}, {SkillID: 1192, LevelDelta: 1}},
	}))
	if !errors.Is(err, ErrSkillSlot) {
		t.Fatalf("full-tree learn error = %v, want ErrSkillSlot", err)
	}
	if got := darkKnightLearnLoad(t, repos); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed batch persisted data: before=%+v after=%+v", before, got)
	}
	states[1192] = dnfrepo.SkillState{Level: 1, Enabled: true}
	stateBefore := dnfrepo.CloneSkill(dnfrepo.SkillRecord{Skills: states}).Skills
	layoutBefore := dnfrepo.CloneSkill(dnfrepo.SkillRecord{Layouts: map[int]dnfrepo.SkillLayout{0: layout}}).Layouts[0]
	if _, err := BuildCurrentSkillLayout(catalog, darkcombo.Job, 0, states, layout); !errors.Is(err, ErrSkillSlot) {
		t.Fatalf("full-tree rebuild error = %v", err)
	}
	if _, err := BuildInitialSkillLayout(catalog, darkcombo.Job, 0, states); !errors.Is(err, ErrSkillSlot) {
		t.Fatalf("full-tree initial layout error = %v", err)
	}
	if !reflect.DeepEqual(states, stateBefore) || !reflect.DeepEqual(layout, layoutBefore) {
		t.Fatal("failed layout build mutated its input")
	}
}

func TestDarkKnightLearnLayoutFixedRootsUnchanged(t *testing.T) {
	ctx := context.Background()
	catalog := darkKnightLearnCatalog(t)
	states, layout := darkKnightLearnRoots()
	layout[0], layout[1] = layout[1], layout[0]
	got, err := BuildCurrentSkillLayout(catalog, darkcombo.Job, 0, states, layout)
	if err != nil {
		t.Fatal(err)
	}
	darkKnightLearnCheckLayout(t, got, 0)
	repos := darkKnightLearnSeed(t, darkcombo.Job, 0, states, layout)
	_, err = darkKnightLearnOwner(t, repos, catalog).ApplySlot(ctx, NewSlotCommand(alignedRequest("dark-knight-layout", 31), ChangeSkillSlotRequest{
		From: 1, To: 0, ContextIndex: -1, Mode: 2,
	}))
	if !errors.Is(err, ErrSkillSlot) {
		t.Fatalf("root swap error = %v, want ErrSkillSlot", err)
	}
}

func TestDarkKnightLearnLayoutWithoutRootsStillUsesTree(t *testing.T) {
	catalog := darkKnightLearnCatalog(t)
	states := map[int64]dnfrepo.SkillState{1000: {Level: 1, Enabled: true}}
	for _, initial := range []bool{false, true} {
		var layout dnfrepo.SkillLayout
		var err error
		if initial {
			layout, err = BuildInitialSkillLayout(catalog, darkcombo.Job, 0, states)
		} else {
			layout, err = BuildCurrentSkillLayout(catalog, darkcombo.Job, 0, states, nil)
		}
		if err != nil || !reflect.DeepEqual(layout, dnfrepo.SkillLayout{150: 1000}) {
			t.Fatalf("without roots initial=%t: layout=%+v err=%v", initial, layout, err)
		}
	}
}

func TestDarkKnightLearnLayoutOtherJobKeepsQuickSlots(t *testing.T) {
	ctx := context.Background()
	catalog := darkKnightLearnCatalog(t)
	states := make(map[int64]dnfrepo.SkillState)
	for i := 0; i < 7; i++ {
		states[int64(1000+i)] = dnfrepo.SkillState{Level: 1, Enabled: true}
	}
	for tree := 0; tree < 2; tree++ {
		t.Run(fmt.Sprintf("tree_%d", tree), func(t *testing.T) {
			current, err := BuildCurrentSkillLayout(catalog, 0, tree, states, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 7; i++ {
				slot := i
				if i == 6 {
					slot = 198
				}
				if current[slot] != uint16(1000+i) {
					t.Fatalf("non-Dark-Knight current layout = %+v", current)
				}
			}
			initial, err := BuildInitialSkillLayout(catalog, 0, tree, states)
			if err != nil || !reflect.DeepEqual(initial, dnfrepo.SkillLayout{0: 1000, 1: 1001, 2: 1002, 150: 1003, 151: 1004, 152: 1005, 153: 1006}) {
				t.Fatalf("non-Dark-Knight initial layout = %+v, err=%v", initial, err)
			}
			repos := darkKnightLearnSeed(t, 0, byte(tree), states, current)
			result, err := darkKnightLearnOwner(t, repos, catalog).ApplyBuy(ctx, NewBuyCommand(alignedRequest("dark-knight-layout", 31), BuySkillRequest{
				RawSkillTree: byte(tree), Count: 1, Entries: []BuySkillEntry{{SkillID: 1007, LevelDelta: 1}},
			}))
			if err != nil || len(result.Entries) != 1 || result.Entries[0].Slot != 199 {
				t.Fatalf("non-Dark-Knight learn = %+v, err=%v", result, err)
			}
		})
	}
}

func darkKnightLearnCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	active := "[type]\n`[active]`\n[required level]\n1\n[maximum level]\n20\n[skill fitness growtype]\n0\n[/skill fitness growtype]\n[purchase cost]\n10\n[/purchase cost]\n"
	source := skillRuleTextSource{"skill/skilllist.lst": "0 `swordman/skilllist.lst`\n9 `demonicswordman/skilllist.lst`\n"}
	for _, folder := range []string{"swordman", "demonicswordman"} {
		var list strings.Builder
		if folder == "demonicswordman" {
			for i := 0; i < 6; i++ {
				fmt.Fprintf(&list, "%d `comboset%d.skl`\n", 2000+i, i)
				source[fmt.Sprintf("skill/%s/comboset%d.skl", folder, i)] = active
			}
		}
		for i := 0; i < 200; i++ {
			fmt.Fprintf(&list, "%d `ordinary%d.skl`\n", 1000+i, i)
			source[fmt.Sprintf("skill/%s/ordinary%d.skl", folder, i)] = active
		}
		source["skill/"+folder+"/skilllist.lst"] = list.String()
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func darkKnightLearnRoots() (map[int64]dnfrepo.SkillState, dnfrepo.SkillLayout) {
	states := make(map[int64]dnfrepo.SkillState)
	layout := make(dnfrepo.SkillLayout)
	for i := 0; i < 6; i++ {
		states[int64(2000+i)] = dnfrepo.SkillState{Level: 1, Enabled: true}
		layout[i] = uint16(2000 + i)
	}
	return states, layout
}

func darkKnightLearnSeed(t *testing.T, job, tree byte, states map[int64]dnfrepo.SkillState, layout dnfrepo.SkillLayout) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "31", AccountID: "dark-knight-layout", Job: fmt.Sprint(job), Level: 10,
		Stats: map[string]int64{"grow_type": 0, currentEXESkillTreeIndexStat: int64(tree)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "31", Skills: states, Layouts: map[int]dnfrepo.SkillLayout{int(tree): layout},
		Points: dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100, TotalTP: 10, RemainingTP: 10, SyncedLevel: 10},
		Trees:  dnfrepo.SkillTreeState{Expanded: tree == 1, Active: tree},
	}); err != nil {
		t.Fatal(err)
	}
	return repos
}

func darkKnightLearnOwner(t *testing.T, repos dnfrepo.Group, catalog *dnfskill.Table) *Owner {
	t.Helper()
	owner, err := NewOwner(repos, OwnerOptions{Catalog: catalog, InitialLevels: map[uint16]int{}})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func darkKnightLearnLoad(t *testing.T, repos dnfrepo.Group) dnfrepo.SkillRecord {
	t.Helper()
	record, found, err := repos.Skill.Load(context.Background(), "31")
	if err != nil || !found {
		t.Fatalf("load skills found=%t err=%v", found, err)
	}
	return record
}

func darkKnightLearnCheckLayout(t *testing.T, layout dnfrepo.SkillLayout, count int) {
	t.Helper()
	if len(layout) != 6+count {
		t.Fatalf("layout length=%d, want %d", len(layout), 6+count)
	}
	seen := make(map[uint16]bool)
	for slot, id := range layout {
		if seen[id] {
			t.Fatalf("duplicate skill %d: %+v", id, layout)
		}
		seen[id] = true
		if slot < 6 {
			if slot < 0 || id != uint16(2000+slot) {
				t.Fatalf("fixed root changed at slot %d: %+v", slot, layout)
			}
		} else if slot >= 198 {
			t.Fatalf("automatic skill %d used extension slot %d", id, slot)
		}
	}
	for i := 0; i < 6; i++ {
		if layout[i] != uint16(2000+i) {
			t.Fatalf("missing fixed root %d: %+v", i, layout)
		}
	}
	for i := 0; i < count; i++ {
		if !seen[uint16(1000+i)] {
			t.Fatalf("lost learned skill %d", 1000+i)
		}
	}
}
