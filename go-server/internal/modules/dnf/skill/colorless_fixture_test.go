package skill

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func seriaSkillFixture(t *testing.T) (repo.Group, ColorlessUseCommand) {
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

type seriaSkillFailTransaction struct {
	repo.AccountCharacterSkillAssetUnitOfWork
}

func (u seriaSkillFailTransaction) WithinAccountCharacterSkillAssets(ctx context.Context, a, c string, apply func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.SkillRepository) error) error {
	return u.AccountCharacterSkillAssetUnitOfWork.WithinAccountCharacterSkillAssets(ctx, a, c, func(ar repo.AccountInventoryRepository, cr repo.CharacterRepository, sr repo.SkillRepository) error {
		if err := apply(ar, cr, sr); err != nil {
			return err
		}
		return errors.New("模拟事务后段失败")
	})
}

type seriaSkillAssetsWrapper struct {
	repo.AccountCharacterAssetUnitOfWork
	seriaSkillFailTransaction
}
