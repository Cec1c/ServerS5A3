package inventory

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/itemusage"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func itemUsageOwnerFixture(t *testing.T) (repo.Group, Command, itemusage.Definition) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	saveTestCharacter(t, ctx, g, repo.CharacterRecord{CharacterID: "77", AccountID: "a", Stats: map[string]int64{"keep": 9}})
	saveTestInventory(t, ctx, g, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{
		"0:130": {ItemID: 7440, Count: 10, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/potion.stk", "stackable_type": "[waste]"}},
	}})
	d := itemusage.Definition{ItemID: 7440, GroupID: -1, Daily: true, DailyLimit: 3}
	cmd := NewUseStackableCommand(alignedcmd.Request{AccountID: "a", SelectedCharacterID: 77, ItemUsageNow: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
		ItemUsageResolver: func(id int64) (*itemusage.Definition, error) {
			if id != 7440 {
				return nil, itemusage.ErrDefinition
			}
			copy := d
			return &copy, nil
		}},
		UseStackableRequest{ListType: 0, SlotIndex: 130, InstanceValue: 4242, ItemCode: 7440})
	return g, cmd, d
}

func TestItemUsageInventoryTransaction(t *testing.T) {
	for _, mode := range []string{"success", "inventory_save_failure", "counter_save_failure", "missing_uow", "wrong_owner", "wrong_item", "definition_failure"} {
		t.Run(mode, func(t *testing.T) {
			g, cmd, d := itemUsageOwnerFixture(t)
			ctx := context.Background()
			use := g
			beforeC, _, _ := g.Character.Load(ctx, "77")
			beforeI, _, _ := g.Inventory.Load(ctx, "77")
			switch mode {
			case "inventory_save_failure":
				use.CharacterAssets = failPotionInventoryUOW{g.CharacterAssets}
			case "counter_save_failure":
				use.CharacterAssets = failUsageCharacterUOW{g.CharacterAssets}
			case "missing_uow":
				use.CharacterAssets = nil
			case "wrong_owner":
				cmd.AccountID = "foreign"
			case "wrong_item":
				cmd.ItemTemplateID = 7441
			case "definition_failure":
				cmd.ItemUsageResolver = func(int64) (*itemusage.Definition, error) { return nil, itemusage.ErrDefinition }
			}
			o, err := NewOwner(use)
			if err != nil {
				t.Fatal(err)
			}
			result, err := o.UseStackable(ctx, cmd, nil)
			afterC, _, _ := g.Character.Load(ctx, "77")
			afterI, _, _ := g.Inventory.Load(ctx, "77")
			if mode == "success" {
				used, _ := itemusage.Counts(afterC, d, cmd.ItemUsageNow)
				if err != nil || !result.ItemUsageChanged || used != 1 || afterI.Slots["0:130"].Count != 9 || afterC.Stats["keep"] != 9 {
					t.Fatalf("result=%+v used=%d err=%v", result, used, err)
				}
			} else if err == nil || result.ItemUsageChanged || !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatalf("failed use escaped transaction: %v", err)
			}
		})
	}
}

func TestItemUsageConcurrentUsesStopAtLimit(t *testing.T) {
	g, cmd, d := itemUsageOwnerFixture(t)
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	results := make(chan error, 20)
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := o.UseStackable(ctx, cmd, nil); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, itemusage.ErrLimit) {
			t.Fatal(err)
		}
	}
	c, _, _ := g.Character.Load(ctx, "77")
	inv, _, _ := g.Inventory.Load(ctx, "77")
	used, _ := itemusage.Counts(c, d, cmd.ItemUsageNow)
	if success != 3 || used != 3 || inv.Slots["0:130"].Count != 7 {
		t.Fatalf("success=%d used=%d count=%d", success, used, inv.Slots["0:130"].Count)
	}
}

type failUsageCharacterUOW struct{ repo.CharacterAssetUnitOfWork }

func (u failUsageCharacterUOW) WithinCharacterAssets(ctx context.Context, id string, f func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return f(failUsageCharacter{c}, i, e)
	})
}

type failUsageCharacter struct{ repo.CharacterRepository }

func (failUsageCharacter) Save(context.Context, repo.CharacterRecord) error {
	return errors.New("injected usage counter save failure")
}
