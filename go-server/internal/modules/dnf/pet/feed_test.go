package pet

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func feedFixture(t *testing.T) (repo.Group, *FeedOwner, FeedCommand) {
	t.Helper()
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	seedGrowthTestCreature(t, ctx, r, 77, 37, 10, 2, 17, 5)
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "a77"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"7:189": {ItemID: 24, Count: 2, Extra: map[string]string{"preserve": "yes"}}}}); err != nil {
		t.Fatal(err)
	}
	source := petGrowthTestSource(false)
	source["stackable/stackable.lst"] = "24 `cash/creature/food.stk`\n25 `cash/creature/rename.stk`\n"
	source["stackable/cash/creature/food.stk"] = "[stackable type] `[feed]`\n[int data] 300 -20 30\n[/int data]\n"
	source["stackable/cash/creature/rename.stk"] = "[stackable type] `[creature]`\n[int data] 300 -20 30\n[/int data]\n"
	c, err := NewPVFCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewFeedOwner(r, c)
	if err != nil {
		t.Fatal(err)
	}
	cmd := FeedCommand{AccountID: "a77", CharacterID: "77", ListType: 7, Slot: 189, ItemID: 24, Now: time.Unix(200000, 0), Expired: func(s repo.ItemStack, n time.Time) bool { return !s.ExpireAt.IsZero() && !s.ExpireAt.After(n) }, SetCount: func(s repo.ItemStack, n int64) (repo.ItemStack, error) { s.Count = n; return s, nil }}
	return r, o, cmd
}

func TestPetFeedAtomicDebitFractionAndCap(t *testing.T) {
	r, o, cmd := feedFixture(t)
	ctx := context.Background()
	p, _, _ := r.Pet.Load(ctx, "77")
	e := p.Entries["37"]
	e.SatietyMicros = 5_250_000
	p.Entries["37"] = e
	if err := r.Pet.Save(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := o.Feed(ctx, cmd)
	if err != nil || got.PetKey != 37 || got.Before.Satiety != 5 || got.After.Satiety != 35 || got.After.SatietyMicros != 35_250_000 || got.Remaining != 1 || got.Definition.DurationSeconds() != 300 || got.Definition.ChargePercent() != -20 {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	p, _, _ = r.Pet.Load(ctx, "77")
	e = p.Entries["37"]
	if e.Level != 2 || e.Exp != 17 || string(e.NameRaw) != "Mina" || e.Extra["preserve"] != "pet" {
		t.Fatal("unrelated creature state changed")
	}
	e.Satiety = 99
	e.SatietyMicros = 99_750_000
	p.Entries["37"] = e
	if err := r.Pet.Save(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err = o.Feed(ctx, cmd)
	if err != nil || got.After.Satiety != 100 || got.After.SatietyMicros != 100_000_000 || got.Remaining != 0 || got.RemainingStack.ItemID != 0 {
		t.Fatalf("last feed=%+v err=%v", got, err)
	}
	inv, _, _ := r.Inventory.Load(ctx, "77")
	if _, ok := inv.Slots["7:189"]; ok {
		t.Fatal("last feed not removed")
	}
}

func TestPetFeedRejectsWithoutAssetMutation(t *testing.T) {
	for _, name := range []string{"wrong_owner", "wrong_list", "bad_slot", "wrong_item", "not_feed", "locked", "expired", "expired_pet", "alias", "no_pet", "mismatched_worn", "malformed_feed"} {
		t.Run(name, func(t *testing.T) {
			r, o, cmd := feedFixture(t)
			ctx := context.Background()
			inv, _, _ := r.Inventory.Load(ctx, "77")
			p, _, _ := r.Pet.Load(ctx, "77")
			w, _, _ := r.Equipment.Load(ctx, "77")
			s := inv.Slots["7:189"]
			switch name {
			case "wrong_owner":
				cmd.AccountID = "other"
			case "wrong_list":
				cmd.ListType = 0
			case "bad_slot":
				cmd.Slot = 239
			case "wrong_item":
				cmd.ItemID = 99
			case "not_feed":
				s.ItemID = 25
				cmd.ItemID = 25
			case "locked":
				s.Extra["equipment_lock_state"] = "locked"
			case "expired":
				s.ExpireAt = cmd.Now
			case "expired_pet":
				pet := w.Entries["26"]
				pet.ExpireAt = cmd.Now
				w.Entries["26"] = pet
			case "alias":
				inv.Slots["07:0189"] = repo.ItemStack{ItemID: 999, Count: 1}
			case "no_pet":
				p.EquippedKey = ""
			case "mismatched_worn":
				delete(w.Entries, "26")
			case "malformed_feed":
				o.catalog.source.(petCatalogTestSource)["stackable/cash/creature/food.stk"] = "[stackable type] `[feed]`\n[int data] 30\n"
			}
			inv.Slots["7:189"] = s
			for _, err := range []error{r.Inventory.Save(ctx, inv), r.Pet.Save(ctx, p), r.Equipment.Save(ctx, w)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			inv, _, _ = r.Inventory.Load(ctx, "77")
			p, _, _ = r.Pet.Load(ctx, "77")
			w, _, _ = r.Equipment.Load(ctx, "77")
			if _, err := o.Feed(ctx, cmd); err == nil {
				t.Fatal("invalid feed accepted")
			}
			afterI, _, _ := r.Inventory.Load(ctx, "77")
			afterP, _, _ := r.Pet.Load(ctx, "77")
			afterW, _, _ := r.Equipment.Load(ctx, "77")
			if !reflect.DeepEqual(inv, afterI) || !reflect.DeepEqual(p, afterP) || !reflect.DeepEqual(w, afterW) {
				t.Fatal("rejection mutated assets")
			}
		})
	}
}

var errFeedSave = errors.New("injected pet feed transaction failure")

type feedFailureAssets struct {
	repo.AccountCharacterAssetUnitOfWork
	inner repo.AccountCharacterPetAssetUnitOfWork
	where string
}

func (f feedFailureAssets) WithinAccountCharacterPetAssets(ctx context.Context, a, c string, fn func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.PetRepository) error) error {
	return f.inner.WithinAccountCharacterPetAssets(ctx, a, c, func(ar repo.AccountInventoryRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository, pr repo.PetRepository) error {
		if f.where == "inventory" {
			ir = feedFailureInventory{ir}
		}
		if f.where == "pet" {
			pr = feedFailurePet{pr}
		}
		if err := fn(ar, cr, ir, er, pr); err != nil {
			return err
		}
		if f.where == "commit" {
			return errFeedSave
		}
		return nil
	})
}

type feedFailureInventory struct{ repo.InventoryRepository }

func (feedFailureInventory) Save(context.Context, repo.InventoryRecord) error { return errFeedSave }

type feedFailurePet struct{ repo.PetRepository }

func (feedFailurePet) Save(context.Context, repo.PetRecord) error { return errFeedSave }

func TestPetFeedRollbackAndConcurrentLastItem(t *testing.T) {
	for _, where := range []string{"inventory", "pet", "commit"} {
		t.Run(where, func(t *testing.T) {
			r, o, cmd := feedFixture(t)
			ctx := context.Background()
			beforeI, _, _ := r.Inventory.Load(ctx, "77")
			beforeP, _, _ := r.Pet.Load(ctx, "77")
			o.assets = feedFailureAssets{r.AccountAssets, r.AccountAssets.(repo.AccountCharacterPetAssetUnitOfWork), where}
			got, err := o.Feed(ctx, cmd)
			if !errors.Is(err, errFeedSave) || got.PetKey != 0 {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			afterI, _, _ := r.Inventory.Load(ctx, "77")
			afterP, _, _ := r.Pet.Load(ctx, "77")
			if !reflect.DeepEqual(beforeI, afterI) || !reflect.DeepEqual(beforeP, afterP) {
				t.Fatal("partial debit/state survived failure")
			}
		})
	}
	r, o, cmd := feedFixture(t)
	ctx := context.Background()
	inv, _, _ := r.Inventory.Load(ctx, "77")
	s := inv.Slots["7:189"]
	s.Count = 1
	inv.Slots["7:189"] = s
	if err := r.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := o.Feed(ctx, cmd); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	p, _, _ := r.Pet.Load(ctx, "77")
	if success != 1 || p.Entries["37"].Satiety != 35 {
		t.Fatalf("success=%d pet=%+v", success, p)
	}
}

func TestRealPVFPetFeedDefinition(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("runtime PVF required")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewPVFCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.ResolveFeed(24)
	if err != nil || d.ItemID() != 24 || d.Satiety() != 30 || d.DurationSeconds() != 300 || d.ChargePercent() != -20 {
		t.Fatalf("feed=%+v err=%v", d, err)
	}
	if _, err := c.ResolveFeed(25); !errors.Is(err, ErrPetFeedInvalid) {
		t.Fatalf("rename card accepted as feed: %v", err)
	}
}
