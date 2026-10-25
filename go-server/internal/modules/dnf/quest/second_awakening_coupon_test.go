package quest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func secondAwakeningCouponFixture(t *testing.T) (repo.Group, *Catalog, UseClearCouponInput) {
	t.Helper()
	g := memory.NewMemoryGroup()
	profiles, skills := couponAwakeningResources(t)
	in := UseClearCouponInput{AccountID: "account", CharacterID: "77", Slot: 87, Coupon: ClearCoupon{ItemID: 10162929, Kind: CouponSecondAwakening, MinimumLevel: 75, DisallowHardcore: true}, Now: time.Unix(1700000000, 0), Profiles: profiles, Skills: skills}
	ctx := context.Background()
	c := repo.CharacterRecord{CharacterID: "77", AccountID: "account", Job: "2", Level: 90, Stats: map[string]int64{"grow_type": 0x11, "gold": 321, "exp": 456}}
	sk := repo.SkillRecord{CharacterID: "77", Skills: map[int64]repo.SkillState{2: {Level: 5, Enabled: true}, 3: {Level: 1, Enabled: true}}, Points: repo.SkillPointState{TotalSP: 100, RemainingSP: 70, TotalTP: 10, RemainingTP: 5, SyncedLevel: 90}}
	for _, err := range []error{g.Character.Save(ctx, c), g.Skill.Save(ctx, sk), g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 10162929, Count: 2}}}), g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "77", States: map[int64]repo.QuestState{3356: {Status: "active"}, 6884: {Status: "completed"}, 6888: {Status: "active"}}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return g, loadProfessionOverrideTestCatalog(t, professionOverrideTestSource()), in
}

func secondAwakeningSnapshot(t *testing.T, g repo.Group) []any {
	t.Helper()
	ctx := context.Background()
	c, _, e := g.Character.Load(ctx, "77")
	if e != nil {
		t.Fatal(e)
	}
	i, _, e := g.Inventory.Load(ctx, "77")
	if e != nil {
		t.Fatal(e)
	}
	s, _, e := g.Skill.Load(ctx, "77")
	if e != nil {
		t.Fatal(e)
	}
	q, _, e := g.Quest.Load(ctx, "77")
	if e != nil {
		t.Fatal(e)
	}
	return []any{c, i, s, q}
}

func TestSecondAwakeningCouponTransaction(t *testing.T) {
	for _, scenario := range []string{"unaccepted", "active", "completed_tasks", "foreign_owner", "under_level", "not_first", "already_second", "no_branch", "external", "unknown_job", "job_limit", "excluded_job", "excluded_branch", "hardcore", "deleted", "expired", "wrong_item", "missing_item", "source_rejected", "missing_profile", "missing_terminal", "missing_skill", "skill_ledger", "inventory_failure", "skill_failure", "quest_failure"} {
		t.Run(scenario, func(t *testing.T) {
			g, catalog, in := secondAwakeningCouponFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "77")
			bag, _, _ := g.Inventory.Load(ctx, "77")
			sk, _, _ := g.Skill.Load(ctx, "77")
			q, _, _ := g.Quest.Load(ctx, "77")
			switch scenario {
			case "active":
				q.States[6900] = repo.QuestState{Status: "active", ProgressValue: 1}
			case "completed_tasks":
				q.States[6900] = repo.QuestState{Status: "completed"}
				q.States[6905] = repo.QuestState{Status: "completed"}
			case "foreign_owner":
				in.AccountID = "other"
			case "under_level":
				c.Level = 74
				sk.Points.SyncedLevel = 74
			case "not_first":
				c.Stats["grow_type"] = 1
			case "already_second":
				c.Stats["grow_type"] = 0x21
			case "no_branch":
				c.Stats["grow_type"] = 0x10
			case "external":
				c.Job = "9"
			case "unknown_job":
				c.Job = "99"
			case "job_limit":
				in.Coupon.Jobs = []int{0}
			case "excluded_job":
				in.Coupon.ExcludedJobs = []int{2}
			case "excluded_branch":
				in.Coupon.ExcludedBranches = [][2]int{{2, 1}}
			case "hardcore":
				c.Stats["is_hardcore_mode"] = 1
			case "deleted":
				c.Stats["delete_flag"] = 1
			case "expired":
				v := bag.Slots["0:87"]
				v.ExpireAt = in.Now
				bag.Slots["0:87"] = v
			case "wrong_item":
				v := bag.Slots["0:87"]
				v.ItemID++
				bag.Slots["0:87"] = v
			case "missing_item":
				delete(bag.Slots, "0:87")
			case "source_rejected":
				in.ValidateSource = func(repo.CharacterRecord, repo.ItemStack) error { return errors.New("source rejected") }
			case "missing_profile":
				in.Profiles = nil
			case "missing_terminal":
				src := professionOverrideTestSource()
				src[DefaultList] = "6900 `awaken/second_1.qst`\n"
				catalog = loadProfessionOverrideTestCatalog(t, src)
			case "missing_skill":
				in.Skills = nil
			case "skill_ledger":
				sk.Points.SyncedLevel = 89
			}
			for _, e := range []error{g.Character.Save(ctx, c), g.Inventory.Save(ctx, bag), g.Skill.Save(ctx, sk), g.Quest.Save(ctx, q)} {
				if e != nil {
					t.Fatal(e)
				}
			}
			before := secondAwakeningSnapshot(t, g)
			injected := g
			if scenario == "inventory_failure" || scenario == "skill_failure" || scenario == "quest_failure" {
				injected.CharacterSettlement = couponFailUOW{g.CharacterSettlement, scenario}
			}
			o, _ := NewOwner(injected)
			r, e := o.UseClearCoupon(ctx, catalog, in)
			after := secondAwakeningSnapshot(t, g)
			success := scenario == "unaccepted" || scenario == "active" || scenario == "completed_tasks"
			if !success {
				if scenario == "already_second" {
					if e != nil || r.Changed {
						t.Fatal("repeat must not consume", e)
					}
				} else if e == nil {
					t.Fatal("invalid use accepted")
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected/failed transaction changed state")
				}
				return
			}
			if e != nil || !r.Changed || !r.Awakened || r.Remaining != 1 {
				t.Fatalf("use: %+v %v", r, e)
			}
			ac := after[0].(repo.CharacterRecord)
			ai := after[1].(repo.InventoryRecord)
			as := after[2].(repo.SkillRecord)
			aq := after[3].(repo.QuestRecord)
			if ac.Stats["grow_type"] != 0x21 || ac.Stats["gold"] != 321 || ac.Stats["exp"] != 456 || ai.Slots["0:87"].Count != 1 {
				t.Fatal("transition/consumption incorrect")
			}
			if as.Points != sk.Points || as.Skills[2].Level != 5 || as.Skills[3].Level != 1 || as.Skills[4].Level != 1 {
				t.Fatal("learned skills/points or awakening grant incorrect")
			}
			for _, id := range []int64{6900, 6905} {
				if aq.States[id].Status != "completed" {
					t.Fatal("second quest omitted", id)
				}
			}
			for _, id := range []int64{3356, 6884, 6888} {
				if !reflect.DeepEqual(aq.States[id], q.States[id]) {
					t.Fatal("unrelated task changed", id)
				}
			}
			r, e = o.UseClearCoupon(ctx, catalog, in)
			if e != nil || r.Changed || !reflect.DeepEqual(after, secondAwakeningSnapshot(t, g)) {
				t.Fatal("replay mutated state", e)
			}
		})
	}
}

func TestSecondAwakeningCouponConcurrentReplay(t *testing.T) {
	g, catalog, in := secondAwakeningCouponFixture(t)
	o, _ := NewOwner(g)
	var wg sync.WaitGroup
	var changed atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := o.UseClearCoupon(context.Background(), catalog, in)
			if e != nil {
				t.Error(e)
			}
			if r.Changed {
				changed.Add(1)
			}
		}()
	}
	wg.Wait()
	if changed.Load() != 1 {
		t.Fatalf("commits=%d", changed.Load())
	}
	bag, _, _ := g.Inventory.Load(context.Background(), "77")
	if bag.Slots["0:87"].Count != 1 {
		t.Fatal("duplicate consumption")
	}
}
