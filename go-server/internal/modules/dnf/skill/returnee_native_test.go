package skill

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestReturneeNativeColorlessCountsCastsWithAtomicDebit(t *testing.T) {
	g, cmd := seriaSkillFixture(t)
	ctx := context.Background()
	ch, _, _ := g.Character.Load(ctx, "1")
	ch.Stats = map[string]int64{"is_return_user": 1, "seria_returnee_active_until": cmd.At.Add(time.Hour).Unix()}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := ConsumeColorlessSkill(ctx, g, cmd); err != nil {
			t.Fatal(err)
		}
	}
	ch, _, _ = g.Character.Load(ctx, "1")
	if ch.Stats[repo.ReturneeCubeSkillCountKey] != 1 {
		t.Fatal("重放重复计数，或将两个晶块当作两次技能", ch.Stats)
	}
	base := g.AccountAssets
	g.AccountAssets = seriaSkillAssetsWrapper{base, seriaSkillFailTransaction{base.(repo.AccountCharacterSkillAssetUnitOfWork)}}
	cmd.EventID = "skill:returnee-failure"
	if _, err := ConsumeColorlessSkill(ctx, g, cmd); err == nil {
		t.Fatal("故障未返回")
	}
	ch, _, _ = g.Character.Load(ctx, "1")
	if ch.Stats[repo.ReturneeCubeSkillCountKey] != 1 {
		t.Fatal("失败事务增加回归计数")
	}
}
