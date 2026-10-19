package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func trainingRewardCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{DefaultList: "2674 `pvp/pvp_tookuk_05.qst`\n", "n_quest/pvp/pvp_tookuk_05.qst": "[type]\n`[custom multi condition]`\n[int data]\n1 1 1 1\n[sub reward]\n10158452\n[reward type]\n`[tp]`\n"}
	index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func trainingRewardSeed(t *testing.T, progress int64) (repo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "owner", Stats: map[string]int64{"preserved": 73}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Quest.Save(ctx, repo.QuestRecord{CharacterID: "1", States: map[int64]repo.QuestState{2674: {Status: "active", ProgressValue: progress, Extra: map[string]string{"pvf_path": "n_quest/pvp/pvp_tookuk_05.qst"}}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, owner
}

func TestPVPTrainingSubRewardUsesPVFAndSurvivesReplay(t *testing.T) {
	ctx := context.Background()
	r, owner := trainingRewardSeed(t, 1<<18)
	catalog := trainingRewardCatalog(t)
	d, _ := catalog.Find(2674)
	d.SubReward[0] = 9
	d, _ = catalog.Find(2674)
	if d.SubReward[0] != 10158452 {
		t.Fatal("目录奖励可被外部改写")
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		result, err := owner.ApplyPVPTrainingReward(ctx, catalog, "owner", "1", now, finishOwnerAllocator(nil))
		if err != nil || result.Granted != (i == 0) || result.ItemID != 10158452 || result.Inventory.Slots["0:65"].Count != 1 {
			t.Fatalf("秘籍发放或重放错误: %+v %v", result, err)
		}
	}
	quests, _, _ := r.Quest.Load(ctx, "1")
	if quests.States[2674].ProgressValue != 1<<18 || quests.States[2674].Status != "active" {
		t.Fatal("发物品代完成任务")
	}
	character, _, _ := r.Character.Load(ctx, "1")
	if character.Stats["preserved"] != 73 || character.Stats[pvpTrainingRewardLedger] != 10158452 {
		t.Fatal("发放账本或角色隔离错误")
	}
	// 客户端收到物品后上报第三条件；本接口不重复增加物品。
	got, err := owner.ApplySetTrigger(ctx, Command{Operation: "set_quest_trigger", AccountID: "owner", SelectedCharacterID: 1, QuestID: 2674, TriggerType: 64})
	if err != nil || got.ProgressValue != 0 {
		t.Fatalf("物品后第三条件无法归零: %+v %v", got, err)
	}
	result, err := owner.ApplyPVPTrainingReward(ctx, catalog, "owner", "1", now, finishOwnerAllocator(nil))
	if err != nil || result.Granted || result.Inventory.Slots["0:65"].Count != 1 {
		t.Fatal("第三条件完成后重复发物品", err)
	}
	// 丢弃后即便重新接受任务，角色账本仍防止刷取。
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Quest.Save(ctx, repo.QuestRecord{CharacterID: "1", States: map[int64]repo.QuestState{2674: {Status: "active", ProgressValue: 1 << 18, Extra: map[string]string{"pvf_path": d.Path}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyPVPTrainingReward(ctx, catalog, "owner", "1", now, finishOwnerAllocator(nil)); !errors.Is(err, ErrPVPTrainingRewardNotReady) {
		t.Fatal("丢弃/重接后可刷取", err)
	}
}

func TestPVPTrainingSubRewardRejectsIncompleteForeignAndBadPVF(t *testing.T) {
	for _, progress := range []int64{1, 1 << 9, 0x40201, 0x40200, 2 << 18, -1, 1 << 32, 0} {
		t.Run("条件"+time.Duration(progress).String(), func(t *testing.T) {
			r, owner := trainingRewardSeed(t, progress)
			if _, err := owner.ApplyPVPTrainingReward(context.Background(), trainingRewardCatalog(t), "owner", "1", time.Now(), finishOwnerAllocator(nil)); !errors.Is(err, ErrPVPTrainingRewardNotReady) {
				t.Fatal("条件不全发放", progress, err)
			}
			inv, _, _ := r.Inventory.Load(context.Background(), "1")
			if len(inv.Slots) != 0 {
				t.Fatal("拒绝请求改变物品")
			}
		})
	}
	_, owner := trainingRewardSeed(t, 1<<18)
	catalog := trainingRewardCatalog(t)
	if _, err := owner.ApplyPVPTrainingReward(context.Background(), catalog, "foreign", "1", time.Now(), finishOwnerAllocator(nil)); !errors.Is(err, ErrCharacterNotFound) {
		t.Fatal("跨账号发放", err)
	}
	d := catalog.byID[2674]
	d.SubReward = []int64{10158452, 2}
	catalog.byID[2674] = d
	if _, err := owner.ApplyPVPTrainingReward(context.Background(), catalog, "owner", "1", time.Now(), finishOwnerAllocator(nil)); !errors.Is(err, ErrPVPTrainingRewardNotReady) {
		t.Fatal("不明奖励形状发放", err)
	}
}

func TestPVPTrainingSubRewardRollbackBeforeAndAfterWrites(t *testing.T) {
	for _, late := range []bool{false, true} {
		ctx := context.Background()
		r, _ := trainingRewardSeed(t, 1<<18)
		beforeChar, _, _ := r.Character.Load(ctx, "1")
		beforeQuest, _, _ := r.Quest.Load(ctx, "1")
		g := r
		allocate := finishOwnerAllocator(errors.New("背包已满"))
		if late {
			g.CharacterSettlement = antonPrerequisiteFailCommit{r.CharacterSettlement}
			allocate = finishOwnerAllocator(nil)
		}
		owner, _ := NewOwner(g)
		result, err := owner.ApplyPVPTrainingReward(ctx, trainingRewardCatalog(t), "owner", "1", time.Now(), allocate)
		if err == nil || result.Granted {
			t.Fatal("失败事务返回发放成功")
		}
		character, _, _ := r.Character.Load(ctx, "1")
		quests, _, _ := r.Quest.Load(ctx, "1")
		inv, _, _ := r.Inventory.Load(ctx, "1")
		if !reflect.DeepEqual(character, beforeChar) || !reflect.DeepEqual(quests, beforeQuest) || len(inv.Slots) != 0 {
			t.Fatal("失败事务残留物品、账本或任务变化")
		}
	}
}

func TestPVPTrainingTPFinishRequiresBookCommitsBothPagesAndReplays(t *testing.T) {
	ctx := context.Background()
	r := finishOwnerSeed(t, ctx)
	catalog := trainingRewardCatalog(t)
	d := catalog.byID[2674]
	d.NoExperience = true
	d.LevelMin, d.LevelMax = 1, 99
	catalog.byID[2674] = d
	if err := r.Quest.Save(ctx, repo.QuestRecord{CharacterID: "19", States: map[int64]repo.QuestState{2674: {Status: "active", ProgressValue: 0, Extra: map[string]string{"pvf_path": d.Path}}}}); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := r.Skill.Load(ctx, "19")
	skill.Points.TotalTP, skill.Points.RemainingTP = 7, 3
	skill.Trees.Expanded = true
	skill.Trees.Other.Points = repo.SkillPointState{TotalTP: 7, RemainingTP: 1, SyncedLevel: 1}
	if err := r.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	// 与MySQL一致，仅保存请求的字段；内存仓储整条回退会掩盖第二页漏存。
	r.CharacterSettlement = trainingFieldScopedSettlement{r.CharacterSettlement}
	owner, _ := NewOwner(r)
	input := FinishCommitInput{AccountID: "acc", CharacterID: "19", QuestID: 2674, Multiplier: 1, CommittedAt: time.Now().UTC(), Progression: finishOwnerProgression(t)}
	if _, err := owner.ApplyFinishSettlement(ctx, catalog, input); !errors.Is(err, ErrFinishRequiredItemsMissing) {
		t.Fatal("缺秘籍仍领奖", err)
	}
	inv, _, _ := r.Inventory.Load(ctx, "19")
	if _, err := finishOwnerAllocator(nil)(&inv, FinishItemGrantRequest{ItemID: 10158452, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := owner.ApplyFinishSettlement(ctx, catalog, input)
		if err != nil {
			t.Fatal(err)
		}
		if !got.AtomicCommitted || got.Replayed != (i == 1) || got.TPDelta != 2 || got.PostCommitCharacter.Stats["bonus_tp"] != 2 || got.PostCommitSkill.Points.TotalTP != 9 || got.PostCommitSkill.Points.RemainingTP != 5 || got.PostCommitSkill.Trees.Other.Points.TotalTP != 9 || got.PostCommitSkill.Trees.Other.Points.RemainingTP != 3 || got.PostCommitQuest.States[2674].Status != "completed" {
			t.Fatalf("TP结算/两页已消费点数/重放错误: %+v", got)
		}
		persisted, _, _ := r.Skill.Load(ctx, "19")
		if persisted.Trees.Other.Points.TotalTP != 9 || persisted.Trees.Other.Points.RemainingTP != 3 {
			t.Fatal("MySQL分字段保存遗漏第二页TP")
		}
	}
}

type trainingFieldScopedSettlement struct {
	base repo.CharacterSettlementUnitOfWork
}

func (r trainingFieldScopedSettlement) WithinCharacterSettlement(ctx context.Context, id string, fn func(repo.Group) error) error {
	return r.base.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		tx.Skill = trainingFieldScopedSkill{tx.Skill}
		return fn(tx)
	})
}

type trainingFieldScopedSkill struct{ repo.SkillRepository }

func (r trainingFieldScopedSkill) SaveFields(ctx context.Context, record repo.SkillRecord, fields ...repo.SkillField) error {
	old, _, err := r.SkillRepository.Load(ctx, record.CharacterID)
	if err != nil {
		return err
	}
	old = repo.CloneSkill(old)
	for _, field := range fields {
		switch field {
		case repo.SkillFieldPoints:
			old.Points = record.Points
		case repo.SkillFieldTrees:
			old.Trees = record.Trees
		default:
			return errors.New("此夹具仅接受末步TP的点数与两页字段")
		}
	}
	old.UpdatedAt = record.UpdatedAt
	return r.SkillRepository.Save(ctx, old)
}

func TestPVPTrainingTPBonusRejectsUnknownShapeAndOverflow(t *testing.T) {
	catalog := trainingRewardCatalog(t)
	d, _ := catalog.Find(2674)
	for _, mutate := range []func(*Definition){func(d *Definition) { d.ID = 2675 }, func(d *Definition) { d.RewardIntData = []int64{8} }, func(d *Definition) { d.SubReward = nil }} {
		other := cloneDefinition(d)
		mutate(&other)
		if _, ok := pvpTrainingTPReward(other); ok {
			t.Fatal("给未核对任务套用2TP")
		}
	}
	inv := repo.InventoryRecord{Slots: map[string]repo.ItemStack{"0:121": {ItemID: 10158452, Count: 1}}}
	character := repo.CharacterRecord{Stats: map[string]int64{"bonus_tp": 65535}}
	if err := applyPVPTrainingTPReward(&character, &repo.SkillRecord{}, inv, d, 2); !errors.Is(err, ErrQuestRewardMalformed) {
		t.Fatal("TP奖励溢出未拒绝", err)
	}
}
