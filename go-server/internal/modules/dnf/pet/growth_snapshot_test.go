package pet

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestGrowthOwnerTransactionEntryMatchesCommittedStateAndIsIndependent(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedGrowthTestCreature(t, ctx, repos, 77, 37, 10, 1, 1, 100)
	owner := newGrowthTestOwner(t, repos)
	result, err := owner.ApplyDungeonClear(ctx, DungeonClearGrowthCommand{SelectedCharacterID: 77, ClearToken: "独立通知快照", ConsumedFatigue: 1})
	if err != nil {
		t.Fatal(err)
	}
	record, _, _ := repos.Pet.Load(ctx, "77")
	if !reflect.DeepEqual(result.Entry, record.Entries["37"]) {
		t.Fatal("通知快照与提交后的宠物不一致")
	}
	result.Entry.NameRaw[0] = 'x'
	result.Entry.Extra["preserve"] = "changed"
	result.Entry.AppliedClearTokens["独立通知快照"] = false
	record, _, _ = repos.Pet.Load(ctx, "77")
	if string(record.Entries["37"].NameRaw) != "Mina" || record.Entries["37"].Extra["preserve"] != "pet" || !record.Entries["37"].AppliedClearTokens["独立通知快照"] {
		t.Fatal("通知结果反向修改了持久化状态")
	}
}

func TestGrowthOwnerCatalogModifiersUseTransactionArtifactsAndKeepFormula(t *testing.T) {
	ctx := context.Background()
	catalog := newPetGrowthTestCatalog(t, false)
	for _, itemID := range []int64{0, 20, 21, 10} {
		repos := dnfrepomemory.NewMemoryGroup()
		seedGrowthTestCreature(t, ctx, repos, 78, 38, 10, 1, 0, 10)
		record, _, _ := repos.Pet.Load(ctx, "78")
		if itemID != 0 {
			record.Artifacts = map[string]dnfrepo.ItemStack{"red": {ItemID: itemID, Count: 1}}
		}
		if err := repos.Pet.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		owner := newGrowthTestOwner(t, repos)
		before := PetGrowthState{ItemID: 10, Level: 1, Satiety: 10}
		var ids []int64
		if itemID != 0 {
			ids = append(ids, itemID)
		}
		mods, modErr := catalog.ResolveSatietyModifiers(ids)
		result, err := owner.ApplyDungeonElapsedWithCatalog(ctx, PetElapsedCommand{SelectedCharacterID: 78, Elapsed: 2 * time.Minute}, catalog)
		if modErr != nil {
			if !errors.Is(err, ErrPetPVFArtifactInvalid) || result.Entry.PetKey != "" {
				t.Fatalf("invalid artifact result=%+v err=%v", result, err)
			}
			after, _, _ := repos.Pet.Load(ctx, "78")
			if !reflect.DeepEqual(record, after) {
				t.Fatal("神器解析失败仍保存了宠物")
			}
			continue
		}
		want, wantErr := owner.engine.ApplyDungeonElapsed(before, 2*time.Minute, mods)
		if err != nil || wantErr != nil || result.After != want.After || result.SatietyDelta != want.SatietyDelta {
			t.Fatalf("artifact=%d got=%+v want=%+v err=%v", itemID, result, want, err)
		}
		after, _, _ := repos.Pet.Load(ctx, "78")
		if !reflect.DeepEqual(result.Entry, after.Entries["38"]) {
			t.Fatal("饱食度通知快照不一致")
		}
	}
}

type growthFailAfterApply struct {
	base dnfrepo.CharacterPetUnitOfWork
}

func (u growthFailAfterApply) WithinCharacterPets(ctx context.Context, id string, apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.PetRepository) error) error {
	return u.base.WithinCharacterPets(ctx, id, func(i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository, p dnfrepo.PetRepository) error {
		if err := apply(i, e, p); err != nil {
			return err
		}
		return errors.New("提交前失败")
	})
}

func TestGrowthOwnerFailedTransactionPublishesNoEntryAndKeepsRetry(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedGrowthTestCreature(t, ctx, repos, 79, 39, 10, 1, 0, 10)
	broken := repos
	broken.CharacterPets = growthFailAfterApply{repos.CharacterPets}
	cmd := DungeonClearGrowthCommand{SelectedCharacterID: 79, ClearToken: "失败后重试", ConsumedFatigue: 1}
	result, err := newGrowthTestOwner(t, broken).ApplyDungeonClear(ctx, cmd)
	if err == nil || result.Entry.PetKey != "" {
		t.Fatalf("发布了未提交快照：%+v err=%v", result, err)
	}
	record, _, _ := repos.Pet.Load(ctx, "79")
	if record.Entries["39"].Exp != 0 || record.Entries["39"].AppliedClearTokens[cmd.ClearToken] {
		t.Fatal("失败事务未回滚")
	}
	result, err = newGrowthTestOwner(t, repos).ApplyDungeonClear(ctx, cmd)
	if err != nil || result.Replayed || result.ExperienceGained != 1 || result.Entry.Exp != 1 {
		t.Fatalf("重试未保留：%+v err=%v", result, err)
	}
}
