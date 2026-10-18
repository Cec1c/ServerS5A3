package itemtrade

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func crystalTradeFixture(t *testing.T) repo.Group {
	t.Helper()
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	for id, account := range map[string]string{"1": "a", "2": "b"} {
		if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: account, Stats: map[string]int64{"gold": 1000}}); err != nil {
			t.Fatal(err)
		}
		if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	for account, count := range map[string]int64{"a": 5000, "b": 20} {
		if err := r.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: account, Slots: map[string]repo.ItemStack{"0:358": {ItemID: 3037, Count: count, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/material/cubepiece_clear.stk", "stackable_type": "material"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func crystalTradeSnapshot(t *testing.T, r repo.Group) []any {
	t.Helper()
	ctx := context.Background()
	var result []any
	for _, account := range []string{"a", "b"} {
		record, found, err := r.AccountInventory.Load(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, record, found)
	}
	for _, id := range []string{"1", "2"} {
		c, found, err := r.Character.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, c, found)
		i, found, err := r.Inventory.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, i, found)
	}
	return result
}

func TestAccountCrystalTradeRejectsWithoutChangingAnyOwner(t *testing.T) {
	for _, scenario := range []string{"short", "bound", "expired", "wrong_slot_item", "soul", "destination_full", "destination_identity", "wrong_account", "duplicate_source", "count_overflow", "missing_atomic_extension", "stale_character_copy"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			r := crystalTradeFixture(t)
			first := Participant{CharacterID: "1", AccountID: "a", Gold: 100, Offers: []Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 358, Count: 1000, ExpectedItem: 3037}}}
			second := Participant{CharacterID: "2", AccountID: "b", Gold: 50}
			account, _, _ := r.AccountInventory.Load(ctx, "a")
			stack := account.Slots["0:358"]
			switch scenario {
			case "short":
				stack.Count = 999
			case "bound":
				stack.Bind = true
			case "expired":
				stack.ExpireAt = time.Now().Add(-time.Hour)
			case "wrong_slot_item":
				stack.ItemID = 3036
				first.Offers[0].ExpectedItem = 3036
			case "soul":
				delete(account.Slots, "0:358")
				first.Offers[0].SourceSlot = 365
				first.Offers[0].ExpectedItem = 10158124
				account.Slots["0:365"] = repo.ItemStack{ItemID: 10158124, Count: 5000}
			case "destination_full":
				destination, _, _ := r.AccountInventory.Load(ctx, "b")
				out := destination.Slots["0:358"]
				out.Count = math.MaxInt32 - 999
				destination.Slots["0:358"] = out
				if err := r.AccountInventory.Save(ctx, destination); err != nil {
					t.Fatal(err)
				}
			case "destination_identity":
				destination, _, _ := r.AccountInventory.Load(ctx, "b")
				out := destination.Slots["0:358"]
				out.Bind = true
				destination.Slots["0:358"] = out
				if err := r.AccountInventory.Save(ctx, destination); err != nil {
					t.Fatal(err)
				}
			case "wrong_account":
				first.AccountID = "b"
			case "duplicate_source":
				first.Offers[0].Count = 3000
				first.Offers = append(first.Offers, Offer{TradeSlot: 4, SourceList: 0, SourceSlot: 358, Count: 3000, ExpectedItem: 3037})
			case "count_overflow":
				first.Offers[0].Count = math.MaxInt64
			case "missing_atomic_extension":
				r.CharacterTrade = crystalLegacyTrade{r.CharacterTrade}
			case "stale_character_copy":
				delete(account.Slots, "0:358")
				if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:358": stack}}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "soul" && scenario != "stale_character_copy" {
				account.Slots["0:358"] = stack
			}
			if err := r.AccountInventory.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			before := crystalTradeSnapshot(t, r)
			owner, err := NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exchange(ctx, first, second); err == nil {
				t.Fatal("invalid exchange accepted")
			}
			if after := crystalTradeSnapshot(t, r); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected exchange changed assets\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

type crystalLegacyTrade struct{ repo.CharacterTradeUnitOfWork }

func TestAccountCrystalTradeMixedGoldAndCharacterItem(t *testing.T) {
	ctx := context.Background()
	r := crystalTradeFixture(t)
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:70": {ItemID: 10008073, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(r)
	result, err := owner.Exchange(ctx, Participant{CharacterID: "1", AccountID: "a", Gold: 100, Offers: []Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 358, Count: 1000, ExpectedItem: 3037}, {TradeSlot: 4, SourceList: 0, SourceSlot: 70, Count: 1, ExpectedItem: 10008073}}}, Participant{CharacterID: "2", AccountID: "b", Gold: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReceivedGold["1"] != 50 || result.ReceivedGold["2"] != 100 || result.GoldFees["1"] != 1 || result.GoldFees["2"] != 3 {
		t.Fatalf("mixed trade gross/fee receipts: %+v", result)
	}
	for id, want := range map[string]int64{"1": 949, "2": 1047} {
		c, _, _ := r.Character.Load(ctx, id)
		if c.Stats["gold"] != want {
			t.Fatalf("gold %s=%d want%d", id, c.Stats["gold"], want)
		}
	}
	first, _, _ := r.Inventory.Load(ctx, "1")
	second, _, _ := r.Inventory.Load(ctx, "2")
	if first.Slots["0:70"].Count != 1 || second.Slots["0:65"].ItemID != 10008073 || second.Slots["0:65"].Count != 1 {
		t.Fatal("mixed ordinary transfer", first, second)
	}
	if len(result.Received["2"]) != 2 {
		t.Fatal("missing transfer receipts", result)
	}
	if _, bad := second.Slots["0:358"]; bad {
		t.Fatal("warehouse copied into character")
	}
}

func TestAccountCrystalTradeSameAccountConservesWarehouse(t *testing.T) {
	ctx := context.Background()
	r := crystalTradeFixture(t)
	c, _, _ := r.Character.Load(ctx, "2")
	c.AccountID = "a"
	if err := r.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(r)
	_, err := owner.Exchange(ctx, Participant{CharacterID: "1", AccountID: "a", Offers: []Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 358, Count: 1000, ExpectedItem: 3037}}}, Participant{CharacterID: "2", AccountID: "a", Offers: []Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 358, Count: 500, ExpectedItem: 3037}}})
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := r.AccountInventory.Load(ctx, "a")
	if a.Slots["0:358"].Count != 5000 {
		t.Fatal("same-account exchange changed total", a)
	}
}

func TestAccountCrystalConcurrentTradesCannotOverspend(t *testing.T) {
	ctx := context.Background()
	r := crystalTradeFixture(t)
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "3", AccountID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := r.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "c", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(r)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, recipient := range []string{"2", "3"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			<-start
			_, err := owner.Exchange(ctx, Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 358, Count: 4000, ExpectedItem: 3037}}}, Participant{CharacterID: id})
			results <- err
		}(recipient)
	}
	close(start)
	workers.Wait()
	close(results)
	success, rejected := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrItemUnavailable) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success%d rejected%d", success, rejected)
	}
	var total int64
	for _, id := range []string{"a", "b", "c"} {
		account, _, _ := r.AccountInventory.Load(ctx, id)
		total += account.Slots["0:358"].Count
	}
	if total != 5020 {
		t.Fatalf("crystal total=%d", total)
	}
}

func TestAccountCrystalTradeMovesBothAccountOwners(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	for id, account := range map[string]string{"1": "a", "2": "b"} {
		if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: account}); err != nil {
			t.Fatal(err)
		}
	}
	for account, count := range map[string]int64{"a": 5000, "b": 20} {
		if err := r.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: account, Slots: map[string]repo.ItemStack{"0:358": {ItemID: 3037, Count: count, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/material/cubepiece_clear.stk", "stackable_type": "material"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Exchange(ctx, Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 3, SourceList: 0, SourceSlot: 358, Count: 1000, ExpectedItem: 3037}}}, Participant{CharacterID: "2"})
	if err != nil {
		t.Fatal(err)
	}
	for account, want := range map[string]int64{"a": 4000, "b": 1020} {
		record, found, err := r.AccountInventory.Load(ctx, account)
		if err != nil || !found || record.Slots["0:358"].Count != want {
			t.Fatalf("account %s got=%+v found=%v err=%v want=%d", account, record, found, err, want)
		}
	}
	for _, id := range []string{"1", "2"} {
		record, _, err := r.Inventory.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := record.Slots["0:358"]; exists {
			t.Fatal("account crystal persisted in character inventory", id)
		}
	}
	rows := result.Received["2"]
	if len(rows) != 1 || rows[0].DestinationList != 0 || rows[0].DestinationSlot != 358 || rows[0].Stack.ItemID != 3037 || rows[0].Stack.Count != 1020 {
		t.Fatalf("receipt %+v", rows)
	}
}
