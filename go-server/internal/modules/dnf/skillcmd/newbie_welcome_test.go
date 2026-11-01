package skillcmd

import (
	"context"
	"testing"
	"time"
)

func TestReturneeNewbieLearningUsesOriginalPointsTransaction(t *testing.T) {
	ctx := context.Background()
	r := seededSkillOwnerRepositories(t, ctx)
	ch, _, _ := r.Character.Load(ctx, "31")
	ch.Stats["is_return_user"] = 0
	ch.Stats["seria_newbie_active_until"] = time.Now().Add(time.Hour).Unix()
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(r, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{RawSkillTree: 0, Count: 1, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 100}}})
	if _, err := owner.ApplyBuy(ctx, invalid); err == nil {
		t.Fatal("非法新手学习通过")
	}
	ch, _, _ = r.Character.Load(ctx, "31")
	if ch.Stats["seria_returnee_skill_count"] != 0 {
		t.Fatal("失败学习增加任务计数")
	}
	valid := NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{RawSkillTree: 0, Count: 2, FinalMode: 3, Entries: []BuySkillEntry{{SkillID: 47, LevelDelta: 1}, {SkillID: 46, LevelDelta: 1}}})
	if _, err := owner.ApplyBuy(ctx, valid); err != nil {
		t.Fatal(err)
	}
	ch, _, _ = r.Character.Load(ctx, "31")
	sk, _, _ := r.Skill.Load(ctx, "31")
	if ch.Stats["seria_returnee_skill_count"] != 2 || ch.Stats["is_return_user"] != 0 || sk.Points.RemainingSP != 50 {
		t.Fatal("新手计数/身份/SP不一致", ch.Stats, sk.Points)
	}
}
