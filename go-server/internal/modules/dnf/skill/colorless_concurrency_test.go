package skill

import (
	"context"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSeriaColorlessConcurrentReplayDebitsOnce(t *testing.T) {
	g, cmd := seriaSkillFixture(t)
	ctx := context.Background()
	ch, _, _ := g.Character.Load(ctx, "1")
	ch.Stats = map[string]int64{"is_return_user": 1, "seria_returnee_active_until": cmd.At.Add(time.Hour).Unix()}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	errors := make(chan error, 32)
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := ConsumeColorlessSkill(ctx, g, cmd)
			errors <- err
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	account, _, _ := g.AccountInventory.Load(ctx, "a")
	ch, _, _ = g.Character.Load(ctx, "1")
	if account.Slots["0:358"].Count != 18 || ch.Stats[repo.ReturneeCubeSkillCountKey] != 1 {
		t.Fatal("并发重试重复扣除或计数", account, ch.Stats)
	}
	base := g.AccountAssets
	g.AccountAssets = seriaSkillAssetsWrapper{base, seriaSkillFailTransaction{base.(repo.AccountCharacterSkillAssetUnitOfWork)}}
	cmd.EventID = "skill:rollback"
	if _, err := ConsumeColorlessSkill(ctx, g, cmd); err == nil {
		t.Fatal("事务后段故障未返回")
	}
	account, _, _ = g.AccountInventory.Load(ctx, "a")
	ch, _, _ = g.Character.Load(ctx, "1")
	if account.Slots["0:358"].Count != 18 || ch.Stats[repo.ReturneeCubeSkillCountKey] != 1 {
		t.Fatal("失败事务遗留扣除或计数", account, ch.Stats)
	}
}

func TestSeriaColorlessRejectsInvalidOwnerSkillOrFunds(t *testing.T) {
	for _, scenario := range []string{"insufficient", "unlearned", "foreign", "stale", "variable-cost", "job"} {
		t.Run(scenario, func(t *testing.T) {
			g, cmd := seriaSkillFixture(t)
			ctx := context.Background()
			switch scenario {
			case "insufficient":
				cmd.Definition.ColorlessCubeCount = 21
			case "unlearned":
				if err := g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1"}); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				ch, _, _ := g.Character.Load(ctx, "1")
				ch.AccountID = "other"
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
			case "stale":
				cmd.Current = func() bool { return false }
			case "variable-cost":
				cmd.Definition.ColorlessCubeCount = 0
			case "job":
				cmd.Definition.Job = 1
			}
			if result, err := ConsumeColorlessSkill(ctx, g, cmd); err == nil || result.Accepted {
				t.Fatal("无效施放被接受", result, err)
			}
			account, _, _ := g.AccountInventory.Load(ctx, "a")
			if account.Slots["0:358"].Count != 20 {
				t.Fatal("拒绝后仍扣除无色", account)
			}
		})
	}
}
