package inventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	"longheng.io/server/internal/modules/dnf/rental"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func luckyItemFixture(t *testing.T) (*Owner, r.Group, LuckyStarItemRequest, time.Time) {
	t.Helper()
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	now := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	for _, err := range []error{
		repos.Account.Save(ctx, r.AccountRecord{AccountID: "lucky", Metadata: map[string]string{rental.PointMetadataKey: "10", "unrelated": "kept"}}),
		repos.Character.Save(ctx, r.CharacterRecord{AccountID: "lucky", CharacterID: "1", Level: 90, Stats: map[string]int64{"gold": 12345}}),
		repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:87": {ItemID: 490001384, Count: 3, ExpireAt: now.Add(time.Hour), Extra: map[string]string{"item_kind": "stackable", "count": "3", "stack_limit": "999", "daily_delete_at": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	o, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	q := LuckyStarItemRequest{AccountID: "lucky", CharacterID: "1", RequestID: fmt.Sprintf("%064x", 1), Slot: 87, Revalidate: func() bool { return true }, Resolve: func(id int64) (LuckyStarItemDefinition, error) {
		return LuckyStarItemDefinition{ItemID: 490001384, PVFPath: "stackable/box.stk", Amount: 50, Limit: 400, MinimumLevel: 1, DailyDelete: true}, nil
	}}
	return o, repos, q, now
}

func TestLuckyStarItemAtomicUseAndReplayAfterSlotReuse(t *testing.T) {
	o, repos, q, now := luckyItemFixture(t)
	got, err := o.UseLuckyStarItem(context.Background(), q, now)
	if err != nil || got.Points != 60 || got.Remaining != 2 || got.Replayed {
		t.Fatalf("%+v %v", got, err)
	}
	bag, _, _ := repos.Inventory.Load(context.Background(), "1")
	if bag.Slots["0:87"].Extra["count"] != "2" {
		t.Fatal("count mirror")
	}
	bag.Slots["0:87"] = r.ItemStack{ItemID: 999, Count: 1}
	if err := repos.Inventory.Save(context.Background(), bag); err != nil {
		t.Fatal(err)
	}
	rebuilt, _ := NewOwner(repos)
	q.Resolve = func(int64) (LuckyStarItemDefinition, error) {
		t.Fatal("replay resolved reused slot")
		return LuckyStarItemDefinition{}, nil
	}
	got, err = rebuilt.UseLuckyStarItem(context.Background(), q, now.Add(24*time.Hour))
	if err != nil || !got.Replayed {
		t.Fatalf("%+v %v", got, err)
	}
	a, _, _ := repos.Account.Load(context.Background(), "lucky")
	c, _, _ := repos.Character.Load(context.Background(), "1")
	b, _, _ := repos.Inventory.Load(context.Background(), "1")
	if a.Metadata[rental.PointMetadataKey] != "60" || a.Metadata["unrelated"] != "kept" || c.Stats["gold"] != 12345 || !reflect.DeepEqual(bag, b) {
		t.Fatal("replay/foreign state modified")
	}
}

func TestLuckyStarItemRejectPreservesBoxAndPoints(t *testing.T) {
	for _, mode := range []string{"cap", "almost_cap", "expired", "daily_expired", "bad_daily_deadline", "wrong_account", "wrong_character", "wrong_item", "locked", "bad_pvf", "changed_session", "late_session", "missing_nonce"} {
		t.Run(mode, func(t *testing.T) {
			o, repos, q, now := luckyItemFixture(t)
			a, _, _ := repos.Account.Load(context.Background(), "lucky")
			b, _, _ := repos.Inventory.Load(context.Background(), "1")
			v := b.Slots["0:87"]
			switch mode {
			case "cap":
				a.Metadata[rental.PointMetadataKey] = "400"
			case "almost_cap":
				a.Metadata[rental.PointMetadataKey] = "351"
			case "expired":
				v.ExpireAt = now
			case "daily_expired":
				v.Extra[itemexpiration.DailyDeleteDeadlineKey] = strconv.FormatInt(now.Unix(), 10)
			case "bad_daily_deadline":
				v.Extra[itemexpiration.DailyDeleteDeadlineKey] = "broken"
			case "wrong_account":
				q.AccountID = "stranger"
			case "wrong_character":
				q.CharacterID = "2"
			case "wrong_item":
				v.ItemID = 999
			case "locked":
				v.Extra["equipment_lock_state"] = "1"
			case "bad_pvf":
				v.Extra["pvf_path"] = "stackable/other.stk"
			case "changed_session":
				q.Revalidate = func() bool { return false }
			case "late_session":
				n := 0
				q.Revalidate = func() bool { n++; return n == 1 }
			case "missing_nonce":
				q.RequestID = ""
			}
			b.Slots["0:87"] = v
			if err := repos.Account.Save(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			if got, err := o.UseLuckyStarItem(context.Background(), q, now); err == nil || got != (LuckyStarItemResult{}) {
				t.Fatalf("%+v %v", got, err)
			}
			afterA, _, _ := repos.Account.Load(context.Background(), "lucky")
			afterB, _, _ := repos.Inventory.Load(context.Background(), "1")
			if !reflect.DeepEqual(a, afterA) || !reflect.DeepEqual(b, afterB) {
				t.Fatal("rejection changed assets")
			}
		})
	}
}

func TestLuckyStarItemConcurrentCapAndDuplicate(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(strconv.FormatBool(duplicate), func(t *testing.T) {
			o, repos, q, now := luckyItemFixture(t)
			a, _, _ := repos.Account.Load(context.Background(), "lucky")
			a.Metadata[rental.PointMetadataKey] = "350"
			repos.Account.Save(context.Background(), a)
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					rq := q
					if !duplicate {
						rq.RequestID = fmt.Sprintf("%064x", i+1)
					}
					_, err := o.UseLuckyStarItem(context.Background(), rq, now)
					if err != nil && !errors.Is(err, rental.ErrPointLimit) {
						t.Error(err)
					}
				}(i)
			}
			wg.Wait()
			a, _, _ = repos.Account.Load(context.Background(), "lucky")
			b, _, _ := repos.Inventory.Load(context.Background(), "1")
			if a.Metadata[rental.PointMetadataKey] != "400" || b.Slots["0:87"].Count != 2 || len(a.Metadata) != 3 {
				t.Fatalf("account=%+v bag=%+v", a, b)
			}
		})
	}
}

type luckyItemFailUOW struct{ r.RentalAssetUnitOfWork }
type luckyItemFailAccount struct{ r.AccountRepository }

func (luckyItemFailAccount) Save(context.Context, r.AccountRecord) error {
	return errors.New("account save failed")
}
func (u luckyItemFailUOW) WithinRentalAssets(ctx context.Context, a, c string, apply func(r.AccountRepository, r.CharacterRepository, r.InventoryRepository, r.EquipmentRepository) error) error {
	return u.RentalAssetUnitOfWork.WithinRentalAssets(ctx, a, c, func(ar r.AccountRepository, cr r.CharacterRepository, ir r.InventoryRepository, er r.EquipmentRepository) error {
		return apply(luckyItemFailAccount{ar}, cr, ir, er)
	})
}
func TestLuckyStarItemLateSaveRollback(t *testing.T) {
	_, repos, q, now := luckyItemFixture(t)
	a, _, _ := repos.Account.Load(context.Background(), "lucky")
	b, _, _ := repos.Inventory.Load(context.Background(), "1")
	broken := repos
	broken.RentalAssets = luckyItemFailUOW{repos.RentalAssets}
	o, _ := NewOwner(broken)
	if _, err := o.UseLuckyStarItem(context.Background(), q, now); err == nil {
		t.Fatal("expected save failure")
	}
	aa, _, _ := repos.Account.Load(context.Background(), "lucky")
	bb, _, _ := repos.Inventory.Load(context.Background(), "1")
	if !reflect.DeepEqual(a, aa) || !reflect.DeepEqual(b, bb) {
		t.Fatal("rollback lost assets")
	}
}
