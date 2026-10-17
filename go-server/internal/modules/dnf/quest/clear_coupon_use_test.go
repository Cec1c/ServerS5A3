package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/profession"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skill"
)

func couponAwakeningResources(t *testing.T) (*profession.Profiles, *skill.Table) {
	t.Helper()
	source := catalogTestSource{
		profession.DefaultCharacterList: "2 `Gunner/Gunner.chr`\n",
		"character/Gunner/Gunner.chr":   "[initial value]\n[skill]\n1 1\n[/skill]\n[growtype 2]\n[skill]\n2 1\n[/skill]\n[awakening 1]\n[awakening skill]\n3 1\n[/awakening skill]\n[awakening 2]\n[awakening skill]\n4 1\n[/awakening skill]\n",
		skill.DefaultList:               "2 `GunnerSkill.lst`\n",
		"skill/GunnerSkill.lst":         "1 `Gunner/a.skl` 2 `Gunner/b.skl` 3 `Gunner/c.skl` 4 `Gunner/d.skl`\n",
		"skill/Gunner/a.skl":            "[name] `a`\n[type] `[active]`\n",
		"skill/Gunner/b.skl":            "[name] `b`\n[type] `[passive]`\n",
		"skill/Gunner/c.skl":            "[name] `c`\n[type] `[passive]`\n",
		"skill/Gunner/d.skl":            "[name] `d`\n[type] `[passive]`\n",
	}
	index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{skill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	skills, err := skill.Load(context.Background(), index, skill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := profession.LoadProfiles(context.Background(), source, skills)
	if err != nil {
		t.Fatal(err)
	}
	return profiles, skills
}

func TestFirstAwakeningCouponAtomicAndScoped(t *testing.T) {
	ctx := context.Background()
	profiles, skills := couponAwakeningResources(t)
	catalog := loadProfessionOverrideTestCatalog(t, professionOverrideTestSource())
	for _, scenario := range []string{"success", "foreign_owner", "under_level", "no_branch", "already_first", "already_second", "excluded_job", "job_limited", "expired", "wrong_item", "missing_profiles", "missing_terminal", "inventory_failure", "skill_failure", "quest_failure"} {
		t.Run(scenario, func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			input := UseClearCouponInput{AccountID: "account", CharacterID: "77", Slot: 87, Coupon: ClearCoupon{ItemID: 10164042, Kind: CouponFirstAwakening, MinimumLevel: 50}, Now: time.Unix(1700000000, 0), Profiles: profiles, Skills: skills}
			c := repo.CharacterRecord{CharacterID: "77", AccountID: "account", Job: "2", Level: 50, Stats: map[string]int64{"grow_type": 1, "exp": 12345, "gold": 789}}
			item := repo.ItemStack{ItemID: 10164042, Count: 2, RawEntry: make([]byte, 30)}
			switch scenario {
			case "foreign_owner":
				input.AccountID = "foreign"
			case "under_level":
				c.Level = 49
			case "no_branch":
				c.Stats["grow_type"] = 0
			case "already_first":
				c.Stats["grow_type"] = 0x11
			case "already_second":
				c.Stats["grow_type"] = 0x21
			case "excluded_job":
				c.Job = "9"
			case "job_limited":
				input.Coupon.Jobs = []int{4}
			case "expired":
				item.ExpireAt = input.Now
			case "wrong_item":
				item.ItemID = 1
			case "missing_profiles":
				input.Profiles = nil
			}
			points := repo.SkillPointState{TotalSP: 100, RemainingSP: 70, TotalTP: 10, RemainingTP: 5, SyncedLevel: c.Level}
			sk := repo.SkillRecord{CharacterID: "77", Points: points, Skills: map[int64]repo.SkillState{2: {Level: 5, Enabled: true}}}
			inv := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:87": item}}
			quests := repo.QuestRecord{CharacterID: "77", States: map[int64]repo.QuestState{6884: {Status: "active", ProgressValue: 3}, 3356: {Status: "active"}, 6888: {Status: "active"}, 6900: {Status: "active"}}}
			for _, err := range []error{repos.Character.Save(ctx, c), repos.Skill.Save(ctx, sk), repos.Inventory.Save(ctx, inv), repos.Quest.Save(ctx, quests)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			target := catalog
			if scenario == "missing_terminal" {
				source := professionOverrideTestSource()
				delete(source, "n_quest/awaken/first_3.qst")
				source[DefaultList] = "6884 `awaken/first_1.qst`\n"
				target = loadProfessionOverrideTestCatalog(t, source)
			}
			injected := repos
			if scenario == "inventory_failure" || scenario == "skill_failure" || scenario == "quest_failure" {
				injected.CharacterSettlement = couponFailUOW{repos.CharacterSettlement, scenario}
			}
			owner, err := NewOwner(injected)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.UseClearCoupon(ctx, target, input)
			after, _, _ := repos.Character.Load(ctx, "77")
			afterSkill, _, _ := repos.Skill.Load(ctx, "77")
			afterInv, _, _ := repos.Inventory.Load(ctx, "77")
			afterQuest, _, _ := repos.Quest.Load(ctx, "77")
			if scenario != "success" {
				if scenario == "already_first" || scenario == "already_second" {
					if err != nil || result.Changed {
						t.Fatalf("repeat=%+v err=%v", result, err)
					}
				} else if err == nil {
					t.Fatal("expected rejection")
				}
				if !reflect.DeepEqual(after, c) || !reflect.DeepEqual(afterSkill, sk) || !reflect.DeepEqual(afterInv, inv) || !reflect.DeepEqual(afterQuest, quests) {
					t.Fatal("rejected transaction mutated assets")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !result.Changed || !result.Awakened || after.Stats["grow_type"] != 0x11 || after.Stats["exp"] != 12345 || after.Stats["gold"] != 789 || afterInv.Slots["0:87"].Count != 1 {
				t.Fatalf("result=%+v character=%+v", result, after)
			}
			if afterSkill.Points != points || afterSkill.Skills[2].Level != 5 || afterSkill.Skills[3].Level != 1 || afterSkill.Skills[4].Level != 0 {
				t.Fatalf("skills=%+v", afterSkill)
			}
			for _, id := range []int64{6884, 6885, 6887} {
				if afterQuest.States[id].Status != "completed" || afterQuest.States[id].Extra["reward_state"] != "granted" {
					t.Fatalf("quest %d not completed without rewards: %+v", id, afterQuest.States[id])
				}
			}
			for _, id := range []int64{3356, 6888, 6900} {
				if !reflect.DeepEqual(afterQuest.States[id], quests.States[id]) {
					t.Fatalf("unrelated quest changed: %d", id)
				}
			}
			replay, err := owner.UseClearCoupon(ctx, target, input)
			if err != nil || replay.Changed {
				t.Fatalf("replay=%+v %v", replay, err)
			}
			inv2, _, _ := repos.Inventory.Load(ctx, "77")
			if !reflect.DeepEqual(afterInv, inv2) {
				t.Fatal("repeat consumed another coupon")
			}
		})
	}
}

type couponFailUOW struct {
	repo.CharacterSettlementUnitOfWork
	stage string
}

func (u couponFailUOW) WithinCharacterSettlement(ctx context.Context, id string, f func(repo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		switch u.stage {
		case "inventory_failure":
			tx.Inventory = couponFailInventory{tx.Inventory}
		case "skill_failure":
			tx.Skill = couponFailSkill{tx.Skill}
		case "quest_failure":
			tx.Quest = couponFailQuest{tx.Quest}
		}
		return f(tx)
	})
}

type couponFailInventory struct{ repo.InventoryRepository }

func (couponFailInventory) Save(context.Context, repo.InventoryRecord) error {
	return errors.New("injected inventory failure")
}

type couponFailSkill struct{ repo.SkillRepository }

func (couponFailSkill) Save(context.Context, repo.SkillRecord) error {
	return errors.New("injected skill failure")
}

type couponFailQuest struct{ repo.QuestRepository }

func (couponFailQuest) Save(context.Context, repo.QuestRecord) error {
	return errors.New("injected quest failure")
}

func TestMainCouponBoundsSubquestsAndRollsBackConsumption(t *testing.T) {
	ctx := context.Background()
	source := catalogTestSource{
		DefaultList:              "1 `parent.qst`\n2 `child.qst`\n3 `future.qst`\n4 `other_job.qst`\n5 `profession.qst`\n",
		"n_quest/parent.qst":     questCatalogTestDefinition("[epic]", 20, 99, "[all]", ""),
		"n_quest/child.qst":      questCatalogTestDefinition("[sub]", 30, 99, "[all]", "[main quest] 1\n"),
		"n_quest/future.qst":     questCatalogTestDefinition("[sub]", 40, 99, "[all]", "[main quest] 1\n"),
		"n_quest/other_job.qst":  questCatalogTestDefinition("[sub]", 20, 99, "[priest]", "[main quest] 1\n"),
		"n_quest/profession.qst": questCatalogTestDefinition("[epic]", 20, 99, "[all]", "[job change quest] 2\n"),
	}
	catalog := loadProfessionOverrideTestCatalog(t, source)
	for _, failure := range []bool{false, true} {
		repos := memory.NewMemoryGroup()
		c := repo.CharacterRecord{CharacterID: "77", AccountID: "account", Job: "2", Level: 35, Stats: map[string]int64{"grow_type": 1, "exp": 789, "gold": 321}}
		inv := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 10301410, Count: 2}}}
		qs := repo.QuestRecord{CharacterID: "77"}
		for _, err := range []error{repos.Character.Save(ctx, c), repos.Inventory.Save(ctx, inv), repos.Quest.Save(ctx, qs)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		input := UseClearCouponInput{AccountID: "account", CharacterID: "77", Slot: 87, Coupon: ClearCoupon{ItemID: 10301410, Kind: CouponMainStory, CutoffLevel: 90}, Now: time.Now()}
		injected := repos
		if failure {
			injected.CharacterSettlement = couponFailUOW{repos.CharacterSettlement, "inventory_failure"}
		}
		owner, _ := NewOwner(injected)
		result, err := owner.UseClearCoupon(ctx, catalog, input)
		after, _, _ := repos.Quest.Load(ctx, "77")
		afterInv, _, _ := repos.Inventory.Load(ctx, "77")
		afterChar, _, _ := repos.Character.Load(ctx, "77")
		if !reflect.DeepEqual(c, afterChar) {
			t.Fatal("main coupon granted rewards")
		}
		if failure {
			if err == nil || !reflect.DeepEqual(after, qs) || !reflect.DeepEqual(afterInv, inv) {
				t.Fatal("main coupon did not roll back")
			}
			continue
		}
		if err != nil || !result.Changed || result.CutoffLevel != 35 || afterInv.Slots["0:87"].Count != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if after.States[1].Status != "completed" || after.States[2].Status != "completed" {
			t.Fatal("eligible quest omitted")
		}
		for _, id := range []int64{3, 4, 5} {
			if _, found := after.States[id]; found {
				t.Fatalf("ineligible quest %d was completed", id)
			}
		}
	}
}
