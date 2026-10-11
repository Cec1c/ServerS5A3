package buffswap

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"sync"
	"testing"
)

type failSettings struct{ repo.SettingsRepository }

func (failSettings) Save(context.Context, repo.SettingsRecord) error {
	return errors.New("injected registration settings failure")
}

type failingRegistrationUOW struct{ repo.CeraShopAssetUnitOfWork }

func (u failingRegistrationUOW) WithinCeraShopAssets(ctx context.Context, a, c, scope string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error) error {
	return u.CeraShopAssetUnitOfWork.WithinCeraShopAssets(ctx, a, c, scope, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository, sr repo.SettingsRepository) error {
		return apply(ar, cr, ir, er, failSettings{sr})
	})
}

func TestInstanceRegistrationRollsBackItemMetadataOnSettingsFailure(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	before, _, _ := r.Inventory.Load(ctx, "6")
	r.CeraShopAssets = failingRegistrationUOW{r.CeraShopAssets}
	if err := Apply(ctx, r, "6", "account-6", Command{38, 12, 0, 54}); err == nil {
		t.Fatal("settings failure accepted")
	}
	after, _, _ := r.Inventory.Load(ctx, "6")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("item identity committed without registration")
	}
	if _, found, _ := r.Settings.Load(ctx, Scope("6")); found {
		t.Fatal("failed registration persisted")
	}
}

func TestInstanceCannotBindReplacementOrCopiedIdentity(t *testing.T) {
	for _, mode := range []string{"lost_metadata", "copied_identity"} {
		t.Run(mode, func(t *testing.T) {
			r, ctx := fixture(t), context.Background()
			if err := Apply(ctx, r, "6", "account-6", Command{38, 12, 0, 54}); err != nil {
				t.Fatal(err)
			}
			state, _, _ := Load(ctx, r, "6", "account-6")
			if state.Slots[12].Instance == "" {
				t.Fatal("new registration lacks instance")
			}
			inv, _, _ := r.Inventory.Load(ctx, "6")
			if mode == "lost_metadata" {
				a := inv.Slots["0:54"]
				a.Extra = nil
				inv.Slots["0:54"] = a
			} else {
				inv.Slots["0:55"] = inv.Slots["0:54"]
			}
			if err := r.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			out, omitted, err := Resolve(ctx, r, "6", state)
			if err != nil || omitted != 1 || len(out.Slots) != 0 {
				t.Fatal("replacement/copy misbound", out, omitted, err)
			}
			if mode == "copied_identity" {
				if err := Apply(ctx, r, "6", "account-6", Command{38, 12, 0, 54}); err == nil {
					t.Fatal("duplicate identity registration accepted")
				}
			}
		})
	}
}

func TestRegistrationUOWRejectsForeignOwnerAndMissingAtomicCapability(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	if err := Apply(ctx, r, "6", "foreign", Command{38, 12, 0, 54}); err == nil {
		t.Fatal("foreign owner accepted")
	}
	r.CeraShopAssets = nil
	if err := Apply(ctx, r, "6", "account-6", Command{38, 12, 0, 54}); err == nil {
		t.Fatal("non atomic registration fallback")
	}
}

func TestConcurrentRegistrationKeepsEverySlotAndExactScope(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	var wg sync.WaitGroup
	errorsCh := make(chan error, SlotCount)
	for slot := byte(0); slot < SlotCount; slot++ {
		wg.Add(1)
		go func(slot byte) {
			defer wg.Done()
			errorsCh <- Apply(ctx, r, "6", "account-6", Command{38, slot, 0, 54})
		}(slot)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, _, err := Load(ctx, r, "6", "account-6")
	if err != nil || len(state.Slots) != SlotCount {
		t.Fatal("concurrent registration lost slots", state, err)
	}
	expected := state.Slots[0].Instance
	for _, b := range state.Slots {
		if b.Instance != expected || expected == "" {
			t.Fatal("same real item got competing instances")
		}
	}
	if err := repo.WithinBuffRegistration(ctx, r, "6", "account-6", func(tx repo.Group) error { return tx.Settings.Save(ctx, repo.SettingsRecord{Scope: Scope("7")}) }); err == nil {
		t.Fatal("foreign settings scope accepted")
	}
}

func TestRegistrationNeverCreatesInstanceForSplitOrMergeableStack(t *testing.T) {
	for _, stack := range []repo.ItemStack{{ItemID: 101040493, Count: 2}, {ItemID: 101040493, Count: 1, Extra: map[string]string{"item_kind": "stackable"}}} {
		r, ctx := fixture(t), context.Background()
		inv, _, _ := r.Inventory.Load(ctx, "6")
		inv.Slots["0:54"] = stack
		if err := r.Inventory.Save(ctx, inv); err != nil {
			t.Fatal(err)
		}
		if err := Apply(ctx, r, "6", "account-6", Command{38, 12, 0, 54}); err == nil {
			t.Fatal("splittable/stackable item registered")
		}
		after, _, _ := r.Inventory.Load(ctx, "6")
		if !reflect.DeepEqual(after, inv) {
			t.Fatal("rejection added item identity")
		}
	}
}
