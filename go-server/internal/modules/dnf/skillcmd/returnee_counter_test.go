package skillcmd

import (
	"context"
	"testing"
	"time"
)

func TestPortedSkillLearnCounterWithPointRollback(t *testing.T) {
	ctx := context.Background()
	r := seededSkillOwnerRepositories(t, ctx)
	ch, _, _ := r.Character.Load(ctx, "31")
	ch.Stats["is_return_user"] = 1
	ch.Stats["seria_returnee_active_until"] = time.Now().Add(time.Hour).Unix()
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(r, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{RawSkillTree: 0, Count: 1, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 100}}})
	if _, err := owner.ApplyBuy(ctx, invalid); err == nil {
		t.Fatal("非法学习未被拒绝")
	}
	ch, _, _ = r.Character.Load(ctx, "31")
	sk, _, _ := r.Skill.Load(ctx, "31")
	if ch.Stats["seria_returnee_skill_count"] != 0 || sk.Points.RemainingSP != 100 {
		t.Fatal("失败学习改变计数或技能点")
	}
	valid := NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{RawSkillTree: 0, Count: 2, FinalMode: 3, Entries: []BuySkillEntry{{SkillID: 47, LevelDelta: 1}, {SkillID: 46, LevelDelta: 1}}})
	if _, err := owner.ApplyBuy(ctx, valid); err != nil {
		t.Fatal(err)
	}
	ch, _, _ = r.Character.Load(ctx, "31")
	sk, _, _ = r.Skill.Load(ctx, "31")
	if ch.Stats["seria_returnee_skill_count"] != 2 || sk.Skills[46].Level != 2 || sk.Skills[47].Level != 1 || sk.Points.RemainingSP != 50 {
		t.Fatalf("学习计数与资产不一致：%+v %+v", ch.Stats, sk)
	}
}
