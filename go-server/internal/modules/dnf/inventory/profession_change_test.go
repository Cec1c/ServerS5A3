package inventory

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/profession"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skill"
)

func professionChangeFixture(t *testing.T) (repo.Group, ProfessionChangeRequest, time.Time) {
	t.Helper()
	g, _, q, _, now := levelTicketFixture(t)
	ctx := context.Background()
	src := levelTicketSource{
		profession.DefaultCharacterList: "2 `test.chr`",
		"character/test.chr":            "[initial value]\n[skill]\n1 1\n[/skill]\n[growtype 1]\n[name] `base`\n[growtype 2]\n[skill]\n2 1\n[/skill]\n[awakening 1]\n[awakening skill]\n3 1\n[/awakening skill]\n[awakening 2]\n[awakening skill]\n4 1\n[/awakening skill]\n[growtype 3]\n[skill]\n5 1\n[/skill]\n[awakening 1]\n[awakening skill]\n6 1\n[/awakening skill]\n[awakening 2]\n[awakening skill]\n7 1\n[/awakening skill]",
		skill.DefaultList:               "2 `test.lst`",
		"skill/test.lst":                "1 `1.skl` 2 `2.skl` 3 `3.skl` 4 `4.skl` 5 `5.skl` 6 `6.skl` 7 `7.skl`",
	}
	for i := 1; i <= 7; i++ {
		src[fmt.Sprintf("skill/%d.skl", i)] = "[name] `skill`\n[skill type] `[passive]`\n[required level] 1"
	}
	index, err := pvf.Build(ctx, src, pvf.BuildOptions{Lists: []string{skill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	skills, err := skill.Load(ctx, index, skill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := profession.LoadProfiles(ctx, src, skills)
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := g.Character.Load(ctx, "2")
	c.Level = 25
	c.Stats["grow_type"] = 1
	c.Stats["gold"] = 3000000
	s, _, _ := g.Skill.Load(ctx, "2")
	s.Skills = map[int64]repo.SkillState{99: {Level: 8, Enabled: true}}
	s.Points = repo.SkillPointState{SyncedLevel: 25, TotalSP: 257, RemainingSP: 200, TotalTP: 10, RemainingTP: 4}
	s.Cooldowns = map[int64]time.Time{99: now.Add(time.Hour)}
	s.Trees = repo.SkillTreeState{Expanded: true, Active: 1, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{99: {Level: 3, Enabled: true}}, Points: s.Points}}
	s.Layouts = map[int]repo.SkillLayout{0: {54: 99}, 1: {54: 99}}
	b, _, _ := g.Inventory.Load(ctx, "2")
	b.Slots["0:4"] = repo.ItemStack{ItemID: 2683467, Count: 2, Extra: map[string]string{"item_kind": "stackable"}}
	for _, e := range []error{g.Character.Save(ctx, c), g.Skill.Save(ctx, s), g.Inventory.Save(ctx, b)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	req := ProfessionChangeRequest{AccountID: "test", CharacterID: "2", Target: 2, Profiles: profiles, Skills: skills, Quests: q, Config: ProfessionChangeConfig{MinimumLevel: 15, MaximumLevel: 49, GoldCosts: []int64{0, 100113, 133484, 177979, 237305, 316406, 421875, 562500, 750000, 1000000}}, PointBaseline: func(c repo.CharacterRecord) (repo.SkillPointState, error) {
		return repo.SkillPointState{SyncedLevel: c.Level, TotalSP: c.Level*10 + int(c.Stats["bonus_sp"]), TotalTP: 10}, nil
	}, Revalidate: func() bool { return true }}
	return g, req, now
}

func professionBookRequest(req ProfessionChangeRequest) ProfessionChangeRequest {
	req.Slot = 4
	req.Book = &ProfessionChangeBook{ItemID: 2683467, PVFPath: "stackable/test.stk", MinimumLevel: 15, MaximumLevel: 49, Jobs: []string{"[all]"}, Places: []string{"[village]"}, SlotPolicy: itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 120, AllowQuickSlots: true}, ProjectSource: func(s repo.ItemStack, _ time.Time) (repo.ItemStack, error) { return s, nil }}
	return req
}

func TestProfessionChangeSystemFreePaidAndCountPersisted(t *testing.T) {
	g, req, now := professionChangeFixture(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		req.Target = byte(2 - i%2)
		before, _, _ := g.Character.Load(ctx, "2")
		r, err := ApplyProfessionChange(ctx, g, req, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(i, err)
		}
		cost, _ := req.Config.GoldCost(int64(i))
		if r.GoldCost != cost || r.Count != int64(i+1) || r.Character.Stats["gold"] != before.Stats["gold"]-cost {
			t.Fatal("费用或次数错误", i, r)
		}
		if r.Skill.Points.RemainingSP != r.Skill.Points.TotalSP || r.Skill.Points.RemainingTP != r.Skill.Points.TotalTP || r.Skill.Trees.Other.Points.RemainingSP != r.Skill.Trees.Other.Points.TotalSP || len(r.Skill.Cooldowns) != 0 || r.Skill.Skills[99].Level != 0 || r.Skill.Trees.Other.Skills[99].Level != 0 {
			t.Fatal("双技能页未完整重建", r.Skill)
		}
		if i == 0 {
			// 新请求结构代表重登；次数完全来自仓库，不从连接初始化为零。
			req2 := req
			req = req2
		}
		if i == 7 {
			c, _, _ := g.Character.Load(ctx, "2")
			c.Stats["gold"] = 5000000
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestProfessionChangeBookPreservesAwakeningAndDoesNotSpendFreeCount(t *testing.T) {
	for stage := byte(0); stage <= 2; stage++ {
		t.Run(fmt.Sprint(stage), func(t *testing.T) {
			g, req, now := professionChangeFixture(t)
			req = professionBookRequest(req)
			req.Book.PreserveAwakening = true
			req.Book.MaximumLevel = 90
			c, _, _ := g.Character.Load(context.Background(), "2")
			c.Stats["grow_type"] = int64(stage<<4 | 1)
			if err := g.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			r, err := ApplyProfessionChange(context.Background(), g, req, now)
			if err != nil {
				t.Fatal(err)
			}
			bag, _, _ := g.Inventory.Load(context.Background(), "2")
			if r.Transition.NewGrowType != stage<<4|2 || r.Count != 0 || r.Character.Stats["gold"] != c.Stats["gold"] || bag.Slots["0:4"].Count != 1 || bag.Slots["0:5"].Count != 8 {
				t.Fatal("扣书或觉醒保留不正确", r, bag)
			}
			if stage >= 1 && r.Skill.Skills[6].Level != 1 || stage == 2 && r.Skill.Skills[7].Level != 1 || r.Skill.Skills[3].Level != 0 {
				t.Fatal("混入旧分支觉醒技能", r.Skill)
			}
		})
	}
}

func TestProfessionChangeRejectsInvalidStateWithoutMutation(t *testing.T) {
	for _, kind := range []string{"owner", "no_grow", "bad_grow", "same_target", "bad_target", "base", "awakened_system", "low_level", "high_level", "negative_count", "overflow_count", "poor", "stale", "expired", "locked", "wrong_item", "wrong_path", "cooldown", "book_level", "book_job", "book_branch", "novice_awakened", "advanced_unawakened", "return_without_receipt"} {
		t.Run(kind, func(t *testing.T) {
			g, req, now := professionChangeFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "2")
			bag, _, _ := g.Inventory.Load(ctx, "2")
			if kind == "expired" || kind == "locked" || kind == "wrong_item" || kind == "wrong_path" || kind == "cooldown" || kind == "book_level" || kind == "book_job" || kind == "book_branch" || kind == "novice_awakened" || kind == "advanced_unawakened" || kind == "return_without_receipt" {
				req = professionBookRequest(req)
			}
			st := bag.Slots["0:4"]
			switch kind {
			case "owner":
				req.AccountID = "other"
			case "no_grow":
				delete(c.Stats, "grow_type")
			case "bad_grow":
				c.Stats["grow_type"] = 257
			case "same_target":
				req.Target = 1
			case "bad_target":
				req.Target = 9
			case "base":
				c.Stats["grow_type"] = 0
			case "awakened_system":
				c.Stats["grow_type"] = 17
			case "low_level":
				c.Level = 14
			case "high_level":
				c.Level = 50
			case "negative_count":
				c.Stats[ProfessionChangeCountKey] = -1
			case "overflow_count":
				c.Stats[ProfessionChangeCountKey] = math.MaxInt32 + 1
			case "poor":
				c.Stats[ProfessionChangeCountKey] = 1
				c.Stats["gold"] = 100112
			case "stale":
				req.Revalidate = func() bool { return false }
			case "expired":
				st.ExpireAt = now
			case "locked":
				st.Extra["equipment_lock_state"] = "1"
			case "wrong_item":
				st.ItemID++
			case "wrong_path":
				st.Extra["pvf_path"] = "stackable/other.stk"
			case "cooldown":
				c.Stats["profession_book_next_use_ms_2683467"] = now.Add(time.Second).UnixMilli()
			case "book_level":
				c.Level = 50
			case "book_job":
				req.Book.Jobs = []string{"[mage]"}
			case "book_branch":
				req.Book.AllowedBranches = []byte{2}
			case "novice_awakened":
				c.Stats["grow_type"] = 17
			case "advanced_unawakened":
				req.Book.RequireAwakening = true
				req.Book.PreserveAwakening = true
			case "return_without_receipt":
				req.Book.ReturnBranch = 2
				req.Book.ReturnGroup = 10002875
			}
			bag.Slots["0:4"] = st
			for _, err := range []error{g.Character.Save(ctx, c), g.Inventory.Save(ctx, bag)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			before := levelTicketSnapshot(t, g)
			if _, err := ApplyProfessionChange(ctx, g, req, now); err == nil {
				t.Fatal("非法请求被接受")
			}
			if !reflect.DeepEqual(before, levelTicketSnapshot(t, g)) {
				t.Fatal("失败仍有资产或职业变化")
			}
		})
	}
}

func TestProfessionChangeAtomicFailuresAndConcurrentDuplicates(t *testing.T) {
	for phase := 0; phase < 4; phase++ {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			g, req, now := professionChangeFixture(t)
			req = professionBookRequest(req)
			before := levelTicketSnapshot(t, g)
			g.CharacterSettlement = levelTicketFailUOW{g.CharacterSettlement, phase}
			if _, err := ApplyProfessionChange(context.Background(), g, req, now); err == nil {
				t.Fatal("写失败未返回错误")
			}
			if !reflect.DeepEqual(before, levelTicketSnapshot(t, g)) {
				t.Fatal("发生部分提交")
			}
		})
	}
	g, req, now := professionChangeFixture(t)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ApplyProfessionChange(context.Background(), g, req, now); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	c, _, _ := g.Character.Load(context.Background(), "2")
	if successes.Load() != 1 || c.Stats[ProfessionChangeCountKey] != 1 || c.Stats["gold"] != 3000000 {
		t.Fatal("重复请求消耗多次", successes.Load(), c)
	}
}

func TestProfessionChangeBonusFailureAndReturnReceipt(t *testing.T) {
	g, req, now := professionChangeFixture(t)
	req = professionBookRequest(req)
	req.Book.GrantBonus = func(b *repo.InventoryRecord, _ time.Time) ([]int16, error) {
		b.Slots["0:6"] = repo.ItemStack{ItemID: 2683471, Count: 1}
		return nil, fmt.Errorf("测试奖励发放失败")
	}
	before := levelTicketSnapshot(t, g)
	if _, err := ApplyProfessionChange(context.Background(), g, req, now); err == nil {
		t.Fatal("奖励失败没有拒绝")
	}
	if !reflect.DeepEqual(before, levelTicketSnapshot(t, g)) {
		t.Fatal("奖励失败仍扣书或转职")
	}
	req.Book.GrantBonus = nil
	req.Book.RecordReturnGroup = 10002875
	if _, err := ApplyProfessionChange(context.Background(), g, req, now); err != nil {
		t.Fatal(err)
	}
	req.Target = 1
	req.Book.RecordReturnGroup = 0
	req.Book.ReturnBranch = 1
	req.Book.ReturnGroup = 10002875
	if _, err := ApplyProfessionChange(context.Background(), g, req, now); err != nil {
		t.Fatal("有效回归凭据未接受", err)
	}
	c, _, _ := g.Character.Load(context.Background(), "2")
	bag, _, _ := g.Inventory.Load(context.Background(), "2")
	if c.Stats["grow_type"] != 1 || c.Stats["profession_return_group"] != 0 || bag.Slots["0:4"].Count != 0 {
		t.Fatal("回归没有原子消费凭据和来源", c, bag)
	}
}
