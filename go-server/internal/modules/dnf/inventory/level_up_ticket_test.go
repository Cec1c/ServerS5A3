package inventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type levelTicketSource map[string]string

func (s levelTicketSource) ReadText(p string) (string, error) {
	if v, ok := s[p]; ok {
		return v, nil
	}
	return "", fmt.Errorf("测试PVF缺少%s", p)
}
func levelTicketFixture(t *testing.T) (repo.Group, *progression.Tables, *quest.Catalog, LevelUpTicketRequest, time.Time) {
	t.Helper()
	ctx := context.Background()
	source := levelTicketSource{}
	var exp, sp, tp strings.Builder
	for i := 1; i <= 90; i++ {
		fmt.Fprintf(&exp, "%d ", i*100)
		fmt.Fprintf(&sp, "%d 10\n", i)
		if i >= 50 {
			fmt.Fprintf(&tp, "%d 1\n", i)
		}
	}
	source[progression.ExperienceTablePath] = exp.String()
	source[progression.SkillPointTablePath] = "[sp table]\n" + sp.String() + "[/sp table]\n[tp table]\n" + tp.String() + "[/tp table]"
	source[progression.QuestParameterPath] = "[difficulty] `N` 100\n[/difficulty]\n[exp reward table]\n100 -1\n[gold reward table]\n10 -1\n[green level penalty]\n80\n[grey level penalty]\n30\n"
	source[quest.DefaultList] = "100 `ticket_a.qst`\n101 `ticket_b.qst`\n102 `ticket_future.qst`"
	for i := 100; i <= 102; i++ {
		p := map[int]string{100: "n_quest/ticket_a.qst", 101: "n_quest/ticket_b.qst", 102: "n_quest/ticket_future.qst"}[i]
		level := 10
		if i == 102 {
			level = 80
		}
		source[p] = fmt.Sprintf("[grade] `[epic]`\n[type] `[quest clear]`\n[level] %d 90\n[exposed by npc] 1\n", level)
	}
	tables, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	index, err := pvf.Build(ctx, source, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := quest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	for _, err := range []error{
		g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "2", AccountID: "test", Job: "2", Level: 50, Stats: map[string]int64{"exp": 4923, "grow_type": 0, "gold": 456, "bonus_sp": 7, "marker": 9}, Location: repo.CharacterLocation{TownID: 39}}),
		g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "2", Skills: map[int64]repo.SkillState{17: {Level: 3, Enabled: true}}, Points: repo.SkillPointState{SyncedLevel: 50, TotalSP: 500, RemainingSP: 425, TotalTP: 10, RemainingTP: 7}}),
		g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "2", Slots: map[string]repo.ItemStack{"0:4": {ItemID: 10151463, Count: 2}, "0:5": {ItemID: 10002567, Count: 8}, "1:4": {ItemID: 10151463, Count: 9}}}),
		g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "2", States: map[int64]repo.QuestState{100: {Status: "active"}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	req := LevelUpTicketRequest{AccountID: "test", CharacterID: "2", SlotIndex: 4, LevelCap: 90, Revalidate: func() bool { return true }, Definition: LevelUpTicketDefinition{ItemID: 10151463, PVFPath: "stackable/ticket.stk", MinimumLevel: 1, MaximumLevel: 89, MaximumGain: 1, Jobs: []string{"[all]"}, CoolTimeMS: 1000}}
	return g, tables, catalog, req, now
}
func levelTicketSnapshot(t *testing.T, g repo.Group) []any {
	t.Helper()
	ctx := context.Background()
	c, _, e := g.Character.Load(ctx, "2")
	if e != nil {
		t.Fatal(e)
	}
	s, _, e := g.Skill.Load(ctx, "2")
	if e != nil {
		t.Fatal(e)
	}
	i, _, e := g.Inventory.Load(ctx, "2")
	if e != nil {
		t.Fatal(e)
	}
	q, _, e := g.Quest.Load(ctx, "2")
	if e != nil {
		t.Fatal(e)
	}
	return []any{c, s, i, q}
}

func TestLevelUpTicketAtomicProgressionKeepsSpentPointsAndPurchaseMaterials(t *testing.T) {
	for _, levels := range []int{1, 10} {
		t.Run(fmt.Sprint(levels), func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			req.Definition.MaximumGain = levels
			r, err := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now)
			if err != nil {
				t.Fatal(err)
			}
			if r.Character.Level != 50+levels || r.Character.Stats["exp"] != int64((49+levels)*100) || r.Progression.Experience.Gain != uint32(levels*100-23) || r.Skill.Points.TotalSP-r.Skill.Points.RemainingSP != 75 || r.Skill.Points.TotalTP-r.Skill.Points.RemainingTP != 3 {
				t.Fatal("升级或已用点数不正确", r)
			}
			if r.Character.Stats["gold"] != 456 || r.Character.Stats["bonus_sp"] != 7 || r.Character.Location.TownID != 39 || r.Skill.Skills[17].Level != 3 {
				t.Fatal("无关角色状态被修改")
			}
			for level := 51; level <= 50+levels; level++ {
				if r.Character.Stats[repo.LevelUpEventKey(level)] <= 0 {
					t.Fatal("没有沿用真实升级事件入口", level)
				}
			}
			bag, _, _ := g.Inventory.Load(context.Background(), "2")
			if bag.Slots["0:4"].Count != 1 || bag.Slots["0:5"].Count != 8 || bag.Slots["1:4"].Count != 9 {
				t.Fatal("扣除来源或兑换材料错误", bag)
			}
			if len(r.QuestPlan.ChangedQuestIDs) != 2 || r.QuestPlan.Record.States[102].Status == "completed" {
				t.Fatal("任务范围错误", r.QuestPlan)
			}
			if _, err := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			bag, _, _ = g.Inventory.Load(context.Background(), "2")
			if _, found := bag.Slots["0:4"]; found {
				t.Fatal("最后一张券没有删除")
			}
		})
	}
}

func TestLevelUpTicketHistoricalExperienceBelowFloorAcrossJobs(t *testing.T) {
	for job := 0; job <= 15; job++ {
		t.Run(fmt.Sprint(job), func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "2")
			c.Job = fmt.Sprint(job)
			c.Stats["exp"] = 10
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			r, err := ApplyLevelUpTicket(ctx, g, tables, q, req, now)
			if err != nil {
				t.Fatal(err)
			}
			if r.Character.Level != 51 || r.Character.Stats["exp"] != 5000 || r.Progression.Experience.Gain != 4990 || r.Progression.Experience.LevelsGained != 1 || r.Progression.SkillPoints.SPGain != 10 || r.Skill.Points.TotalSP-r.Skill.Points.RemainingSP != 75 {
				t.Fatal("旧经验补足错误或重复授予历史点数", r)
			}
			if r.Character.Stats[repo.LevelUpEventKey(51)] <= 0 || r.Character.Stats[repo.LevelUpEventKey(50)] != 0 {
				t.Fatal("重放了历史升级事件")
			}
			if r.Remaining != 1 || r.Character.Stats["bonus_sp"] != 7 || r.Skill.Skills[17].Level != 3 {
				t.Fatal("扣券或既有资产错误")
			}
		})
	}
}

func TestLevelUpTicketHistoricalExperienceWriteFailureRollsBack(t *testing.T) {
	for phase := 0; phase < 4; phase++ {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "2")
			c.Stats["exp"] = 0
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			before := levelTicketSnapshot(t, g)
			g.CharacterSettlement = levelTicketFailUOW{g.CharacterSettlement, phase}
			if _, err := ApplyLevelUpTicket(ctx, g, tables, q, req, now); err == nil {
				t.Fatal("写入失败没有拒绝")
			}
			if !reflect.DeepEqual(before, levelTicketSnapshot(t, g)) {
				t.Fatal("失败后补足了经验或扣除了升级券")
			}
		})
	}
}

func TestLevelUpTicketRejectsInvalidStateWithoutMutation(t *testing.T) {
	for _, kind := range []string{"owner", "minimum", "maximum", "cap", "expired", "locked", "missing", "wrong_item", "wrong_path", "wrong_kind", "job", "grow", "deleted", "hardcore", "missing_exp", "negative_exp", "high_exp", "ledger", "early_binding", "late_binding", "missing_transaction", "bad_roll", "roll_error"} {
		t.Run(kind, func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			ctx := context.Background()
			c, _, _ := g.Character.Load(ctx, "2")
			bag, _, _ := g.Inventory.Load(ctx, "2")
			s, _, _ := g.Skill.Load(ctx, "2")
			item := bag.Slots["0:4"]
			switch kind {
			case "owner":
				req.AccountID = "other"
			case "minimum":
				req.Definition.MinimumLevel = 51
			case "maximum":
				req.Definition.MaximumLevel = 49
			case "cap":
				c.Level = 90
			case "expired":
				item.ExpireAt = now
			case "locked":
				item.Extra = map[string]string{"equipment_lock_state": "1"}
			case "missing":
				req.SlotIndex = 9
			case "wrong_item":
				item.ItemID++
			case "wrong_path":
				item.Extra = map[string]string{"pvf_path": "other"}
			case "wrong_kind":
				item.Extra = map[string]string{"item_kind": "equipment"}
			case "job":
				req.Definition.Jobs = []string{"[no_such_job]"}
			case "grow":
				c.Stats["grow_type"] = -1
			case "deleted":
				c.Stats["delete_flag"] = 1
			case "hardcore":
				req.Definition.DisallowHardcore = true
				c.Stats["is_hardcore_mode"] = 1
			case "missing_exp":
				delete(c.Stats, "exp")
			case "negative_exp":
				c.Stats["exp"] = -1
			case "high_exp":
				c.Stats["exp"] = 5000
			case "ledger":
				s.Points.SyncedLevel = 49
			case "early_binding":
				req.Revalidate = func() bool { return false }
			case "late_binding":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls == 1 }
			case "missing_transaction":
				g.CharacterSettlement = nil
			case "bad_roll", "roll_error":
				req.Definition.Outcomes = []LevelUpTicketOutcome{{Levels: 1, Weight: 1}}
				req.Roll = func(int64) (int64, error) {
					if kind == "roll_error" {
						return 0, errors.New("随机源失败")
					}
					return 1, nil
				}
			}
			bag.Slots["0:4"] = item
			for _, err := range []error{g.Character.Save(ctx, c), g.Inventory.Save(ctx, bag), g.Skill.Save(ctx, s)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			before := levelTicketSnapshot(t, g)
			if _, err := ApplyLevelUpTicket(ctx, g, tables, q, req, now); err == nil {
				t.Fatal("非法状态没有被拒绝", kind)
			}
			if after := levelTicketSnapshot(t, g); !reflect.DeepEqual(before, after) {
				t.Fatal("失败后仍改变持久状态", kind)
			}
		})
	}
}

type levelTicketFailUOW struct {
	repo.CharacterSettlementUnitOfWork
	phase int
}

func (u levelTicketFailUOW) WithinCharacterSettlement(ctx context.Context, id string, f func(repo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(g repo.Group) error {
		switch u.phase {
		case 0:
			g.Inventory = levelTicketFailInventory{g.Inventory}
		case 1:
			g.Character = levelTicketFailCharacter{g.Character}
		case 2:
			g.Skill = levelTicketFailSkill{g.Skill}
		case 3:
			g.Quest = levelTicketFailQuest{g.Quest}
		}
		return f(g)
	})
}

type levelTicketFailInventory struct{ repo.InventoryRepository }

func (levelTicketFailInventory) Save(context.Context, repo.InventoryRecord) error {
	return errors.New("测试背包写入失败")
}

type levelTicketFailCharacter struct{ repo.CharacterRepository }

func (levelTicketFailCharacter) Save(context.Context, repo.CharacterRecord) error {
	return errors.New("测试角色写入失败")
}

type levelTicketFailSkill struct{ repo.SkillRepository }

func (levelTicketFailSkill) Save(context.Context, repo.SkillRecord) error {
	return errors.New("测试技能写入失败")
}

type levelTicketFailQuest struct{ repo.QuestRepository }

func (levelTicketFailQuest) Save(context.Context, repo.QuestRecord) error {
	return errors.New("测试任务写入失败")
}
func TestLevelUpTicketEveryWriteFailureRollsBackAllAggregates(t *testing.T) {
	for phase := 0; phase < 4; phase++ {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			before := levelTicketSnapshot(t, g)
			g.CharacterSettlement = levelTicketFailUOW{g.CharacterSettlement, phase}
			if _, err := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now); err == nil {
				t.Fatal("写失败没有返回错误")
			}
			if !reflect.DeepEqual(before, levelTicketSnapshot(t, g)) {
				t.Fatal("失败后出现部分提交")
			}
		})
	}
}

func TestLevelUpTicketWeightedOutcomesAndConcurrentCooldown(t *testing.T) {
	for _, roll := range []int64{0, 4, 5, 7, 8} {
		t.Run(fmt.Sprint(roll), func(t *testing.T) {
			g, tables, q, req, now := levelTicketFixture(t)
			req.Definition.MaximumGain = 3
			req.Definition.Outcomes = []LevelUpTicketOutcome{{1, 5}, {2, 3}, {3, 1}}
			req.Roll = func(total int64) (int64, error) {
				if total != 9 {
					t.Fatal(total)
				}
				return roll, nil
			}
			r, err := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now)
			want := 51
			if roll >= 5 {
				want = 52
			}
			if roll >= 8 {
				want = 53
			}
			if err != nil || r.Character.Level != want {
				t.Fatal(r, err)
			}
		})
	}
	g, tables, q, req, now := levelTicketFixture(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ApplyLevelUpTicket(context.Background(), g, tables, q, req, now); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("并发点击绕过持久冷却", successes.Load())
	}
}
