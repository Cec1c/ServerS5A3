package expertjob

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type expertExperienceFailureUOW struct {
	repository.CharacterSettlementUnitOfWork
	mode string
}
type expertExperienceFailureCharacter struct{ repository.CharacterRepository }

func (r expertExperienceFailureCharacter) SaveExpertJobExperience(context.Context, repository.CharacterRecord) error {
	return errors.New("注入经验写失败")
}

type expertExperienceFailureInventory struct{ repository.InventoryRepository }

func (r expertExperienceFailureInventory) Save(context.Context, repository.InventoryRecord) error {
	return errors.New("注入来源写失败")
}
func (u expertExperienceFailureUOW) WithinCharacterSettlement(ctx context.Context, id string, apply func(repository.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx repository.Group) error {
		if u.mode == "经验失败" {
			tx.Character = expertExperienceFailureCharacter{tx.Character}
		}
		if u.mode == "来源失败" {
			tx.Inventory = expertExperienceFailureInventory{tx.Inventory}
		}
		return apply(tx)
	})
}

func TestOwnerExtractionExperienceKeepsAtomicAssetBoundary(t *testing.T) {
	for _, mode := range []string{"成功", "经验失败", "来源失败"} {
		fail := mode != "成功"
		repos := memory.NewMemoryGroup()
		ctx := context.Background()
		if err := repos.Character.Save(ctx, repository.CharacterRecord{CharacterID: "19", AccountID: "a", Stats: map[string]int64{"expert_job_exp": 10, "gold": 700, "grow_type": 2}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: "19", Slots: map[string]repository.ItemStack{"0:10": {ItemID: 100, Count: 1}}}); err != nil {
			t.Fatal(err)
		}
		if fail {
			repos.CharacterSettlement = expertExperienceFailureUOW{repos.CharacterSettlement, mode}
		}
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		err = owner.Extract(ctx, Command{AccountID: "a", CharacterID: "19", Project: func(a *Assets) (Changes, error) {
			a.Character.Stats["expert_job_exp"] = 13
			a.Character.Stats["gold"] = 0 // 经验通道不允许顺带写其它属性。
			delete(a.Inventory.Slots, "0:10")
			return Changes{ExpertExperience: true, Inventory: true}, nil
		}})
		if (err != nil) != fail {
			t.Fatal("失败状态不符", fail, err)
		}
		c, _, _ := repos.Character.Load(ctx, "19")
		bag, _, _ := repos.Inventory.Load(ctx, "19")
		want := int64(13)
		if fail {
			want = 10
		}
		if c.Stats["expert_job_exp"] != want || c.Stats["gold"] != 700 || c.Stats["grow_type"] != 2 || (len(bag.Slots) == 1) != fail {
			t.Fatal("经验或资产未保持同事务", fail, c.Stats, bag.Slots)
		}
	}
}
