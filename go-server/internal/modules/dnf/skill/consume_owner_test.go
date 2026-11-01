package skill

import (
	"context"
	"errors"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func rankSkillFixture(t *testing.T) (repo.Group, ColorlessUseCommand) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	for _, id := range []string{"1", "2"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: id, Job: "0", Level: 90}); err != nil {
			t.Fatal(err)
		}
		if err := g.Skill.Save(ctx, repo.SkillRecord{CharacterID: id, Skills: map[int64]repo.SkillState{81: {Level: 1, Enabled: true}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"0:358": {ItemID: 3037, Count: 20, RawEntry: make([]byte, 77)}}}); err != nil {
		t.Fatal(err)
	}
	return g, ColorlessUseCommand{AccountID: "a", CharacterID: "1", EventID: "skill:first", Definition: Skill{Job: 0, ID: 81, Active: true, UsesColorlessCube: true, ColorlessCubeCount: 2, RequiredLevel: 45}, DungeonID: 310, At: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
}

type rankSkillFailTransaction struct {
	repo.AccountCharacterSkillAssetUnitOfWork
}

func (u rankSkillFailTransaction) WithinAccountCharacterSkillAssets(ctx context.Context, a, c string, apply func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.SkillRepository) error) error {
	return u.AccountCharacterSkillAssetUnitOfWork.WithinAccountCharacterSkillAssets(ctx, a, c, func(ar repo.AccountInventoryRepository, cr repo.CharacterRepository, sr repo.SkillRepository) error {
		if err := apply(ar, cr, sr); err != nil {
			return err
		}
		return errors.New("模拟事务后段失败")
	})
}

type rankSkillAssetsWrapper struct {
	repo.AccountCharacterAssetUnitOfWork
	rankSkillFailTransaction
}

func TestRankColorlessSkillAtomicQuantityReplayAndSharedAccount(t *testing.T) {
	g, cmd := rankSkillFixture(t)
	ctx := context.Background()
	r, err := ConsumeColorlessSkill(ctx, g, cmd)
	if err != nil || !r.Accepted || r.Spent != 2 || r.Remaining != 18 {
		t.Fatal(r, err)
	}
	cmd.At = cmd.At.AddDate(0, 0, 1)
	r, err = ConsumeColorlessSkill(ctx, g, cmd)
	if err != nil || !r.Replayed || r.Spent != 0 || r.Remaining != 18 {
		t.Fatal(r, err)
	}
	cmd.CharacterID, cmd.EventID = "2", "skill:second"
	r, err = ConsumeColorlessSkill(ctx, g, cmd)
	if err != nil || r.Remaining != 16 || r.Spent != 2 {
		t.Fatal(r, err)
	}
	s, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if err != nil || s.Metrics[repo.ActivityColorless] != 4 || len(s.Characters) != 2 {
		t.Fatal(s, err)
	}
	cmd.EventID = "skill:fail"
	base := g.AccountAssets
	g.AccountAssets = rankSkillAssetsWrapper{base, rankSkillFailTransaction{base.(repo.AccountCharacterSkillAssetUnitOfWork)}}
	if _, err = ConsumeColorlessSkill(ctx, g, cmd); err == nil {
		t.Fatal("故障未返回")
	}
	cube, _, _ := g.AccountInventory.Load(ctx, "a")
	s, _ = g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if cube.Slots["0:358"].Count != 16 || s.Metrics[repo.ActivityColorless] != 4 {
		t.Fatal(cube, s)
	}
}

func TestRankColorlessSkillRejectsUnlearnedInsufficientAndWrongOwner(t *testing.T) {
	for _, mode := range []string{"short", "unlearned", "foreign", "revoked", "cost", "job"} {
		t.Run(mode, func(t *testing.T) {
			g, cmd := rankSkillFixture(t)
			ctx := context.Background()
			switch mode {
			case "short":
				cmd.Definition.ColorlessCubeCount = 21
			case "unlearned":
				g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1"})
			case "foreign":
				ch, _, _ := g.Character.Load(ctx, "1")
				ch.AccountID = "other"
				g.Character.Save(ctx, ch)
			case "revoked":
				cmd.Current = func() bool { return false }
			case "cost":
				cmd.Definition.ColorlessCubeCount = 0
			case "job":
				cmd.Definition.Job = 1
			}
			if r, err := ConsumeColorlessSkill(ctx, g, cmd); err == nil || r.Accepted {
				t.Fatal(r, err)
			}
			cube, _, _ := g.AccountInventory.Load(ctx, "a")
			events, err := g.Activity.Events(ctx, repo.ActivityQuery{AccountID: "a"})
			if err != nil || len(events) != 0 || cube.Slots["0:358"].Count != 20 {
				t.Fatal(cube, events, err)
			}
		})
	}
}

func TestRankColorlessSkillUsesFixedDungeonPVFQuantity(t *testing.T) {
	for _, row := range []struct {
		text string
		want int64
	}{
		{"[consume item]\n3037 2 2\n[/consume item]\n[pvp]\n[consume item]\n[/consume item]\n[/pvp]", 2},
		{"[consume item]\n3037 10 10\n[/consume item]", 10},
		{"[consume item]\n3037 1 2\n[/consume item]", 0},
		{"[consume item]\n3037 2\n[/consume item]", 0},
		{"[consume item]\n3037 2 2\n[/consume item]\n[dungeon]\n[consume item]\n[/consume item]\n[/dungeon]", 0},
	} {
		doc, err := dnfpvf.Parse("skill/test.skl", row.text)
		if err != nil {
			t.Fatal(err)
		}
		if got := fixedColorlessCubeCount(doc); got != row.want {
			t.Fatal(fmt.Sprintf("得到%d，预期%d", got, row.want))
		}
	}
}
