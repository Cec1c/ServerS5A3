package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type skillTreeFailOnceStore struct {
	repo.SkillRepository
	failed bool
}

func (s *skillTreeFailOnceStore) Save(ctx context.Context, r repo.SkillRecord) error {
	if err := s.SkillRepository.Save(ctx, r); err != nil {
		return err
	}
	if !s.failed {
		s.failed = true
		return errors.New("模拟技能存储在写入后失败")
	}
	return nil
}

func TestCeraSkillCheckoutRollsBackWalletCharacterAndProfiles(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGroup()
	a := repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cera": "1000"}}
	c := repo.CharacterRecord{CharacterID: "31", AccountID: "a", Stats: map[string]int64{"skill_tree_index": -1}}
	before := repo.SkillRecord{CharacterID: "31", Skills: map[int64]repo.SkillState{46: {Level: 3, Enabled: true}}, Points: repo.SkillPointState{TotalSP: 100, RemainingSP: 60}}
	for _, err := range []error{g.Account.Save(ctx, a), g.Character.Save(ctx, c), g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31"}), g.Skill.Save(ctx, before)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	u := g.CeraShopAssets.(*memoryCeraShopAssetUnitOfWork)
	u.skills = &skillTreeFailOnceStore{SkillRepository: g.Skill}
	err := u.WithinCeraShopSkillAssets(ctx, "a", "31", repo.CharacterContainerStateScope("31"), func(a repo.AccountRepository, c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.SettingsRepository, s repo.SkillRepository) error {
		if err := a.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cera": "900"}}); err != nil {
			return err
		}
		if err := c.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "a", Stats: map[string]int64{"skill_tree_index": 0}}); err != nil {
			return err
		}
		next := repo.CloneSkill(before)
		next.Trees.Expanded = true
		return s.Save(ctx, next)
	})
	if err == nil {
		t.Fatal("模拟保存失败却提交了购买")
	}
	afterA, _, _ := g.Account.Load(ctx, "a")
	afterC, _, _ := g.Character.Load(ctx, "31")
	afterS, _, _ := g.Skill.Load(ctx, "31")
	if !reflect.DeepEqual(a, afterA) || !reflect.DeepEqual(c, afterC) || !reflect.DeepEqual(before, afterS) {
		t.Fatalf("回滚不完整: %+v %+v %+v", afterA, afterC, afterS)
	}
}
