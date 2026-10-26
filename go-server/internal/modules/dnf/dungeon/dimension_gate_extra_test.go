package dungeon

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func dimensionExtraFixture(t *testing.T) (dnfrepo.Group, *Owner, DimensionGateExtraEntryCommand) {
	t.Helper()
	g, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:1":  {ItemID: 10158125, Count: 40},
		"0:20": {ItemID: 10158125, Count: 20},
		"0:21": {ItemID: 10158692, Count: 1},
	})
	if err := g.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "own", Level: 90, Stats: map[string]int64{"fatigue": 100, "gold": 123}}); err != nil {
		t.Fatal(err)
	}
	return g, owner, DimensionGateExtraEntryCommand{CharacterID: "19", AccountID: "own", Slot: 20, ItemID: 10158125, Count: 1, BaseLimit: 5, ExtraLimit: 5, MinimumLevel: 70, Cooldown: 10 * time.Second, UpdatedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
}

func dimensionExtraSnapshot(t *testing.T, g dnfrepo.Group) (dnfrepo.CharacterRecord, dnfrepo.InventoryRecord) {
	t.Helper()
	c, found, err := g.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	b, found, err := g.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	return dnfrepo.CloneCharacter(c), dnfrepo.CloneInventory(b)
}

func TestDimensionExtraTicketsPersistWithoutReplenishingDailyGrantCap(t *testing.T) {
	g, owner, ticket := dimensionExtraFixture(t)
	entry := DimensionGateEntryCommand{CharacterID: "19", Limit: 5, ExtraLimit: 5, UpdatedAt: ticket.UpdatedAt}
	for i := 0; i < 5; i++ {
		if _, err := owner.ConsumeDimensionGateEntry(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	for i := int64(1); i <= 5; i++ {
		ticket.UpdatedAt = ticket.UpdatedAt.Add(10 * time.Second)
		r, err := owner.GrantDimensionGateExtraEntry(context.Background(), ticket)
		if err != nil || r.Granted != i || r.Total != 5+i || r.Slot.Slot != 20 {
			t.Fatal(r, err)
		}
		owner, err = NewOwner(g)
		if err != nil {
			t.Fatal(err)
		}
		entry.UpdatedAt = ticket.UpdatedAt
		if r, err := owner.ConsumeDimensionGateEntry(context.Background(), entry); err != nil || r.Used != 5+i {
			t.Fatal(r, err)
		}
	}
	c, b := dimensionExtraSnapshot(t, g)
	if b.Slots["0:20"].Count != 15 || b.Slots["0:20"].Extra["count"] != "15" || b.Slots["0:20"].Extra["amount_or_count"] != "15" || b.Slots["0:1"].Count != 40 || c.Stats["gold"] != 123 || c.Stats["fatigue"] != 100 {
		t.Fatal(c, b)
	}
	ticket.UpdatedAt = ticket.UpdatedAt.Add(10 * time.Second)
	if _, err := owner.GrantDimensionGateExtraEntry(context.Background(), ticket); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatal(err)
	}
	if _, err := owner.ConsumeDimensionGateEntry(context.Background(), entry); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatal(err)
	}
	afterC, afterB := dimensionExtraSnapshot(t, g)
	if !reflect.DeepEqual(c, afterC) || !reflect.DeepEqual(b, afterB) {
		t.Fatal("rejected eleventh entry/grant mutated assets")
	}
}

func TestDimensionExtraFiveTicketAndSixAMReset(t *testing.T) {
	for _, entryFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "first_entry", false: "first_ticket"}[entryFirst], func(t *testing.T) {
			g, o, cmd := dimensionExtraFixture(t)
			cmd.Slot = 21
			cmd.ItemID = 10158692
			cmd.Count = 5
			r, err := o.GrantDimensionGateExtraEntry(context.Background(), cmd)
			if err != nil || r.Total != 10 || !r.Slot.Removed {
				t.Fatal(r, err)
			}
			entry := DimensionGateEntryCommand{CharacterID: "19", Limit: 5, ExtraLimit: 5, UpdatedAt: cmd.UpdatedAt}
			for i := 0; i < 10; i++ {
				if _, err := o.ConsumeDimensionGateEntry(context.Background(), entry); err != nil {
					t.Fatal(err)
				}
			}
			entry.UpdatedAt = time.Date(2026, 10, 14, 21, 59, 59, 0, time.UTC)
			if _, err := o.ConsumeDimensionGateEntry(context.Background(), entry); !errors.Is(err, ErrDailyEntryLimit) {
				t.Fatal(err)
			}
			entry.UpdatedAt = entry.UpdatedAt.Add(time.Second)
			if entryFirst {
				if _, err := o.ConsumeDimensionGateEntry(context.Background(), entry); err != nil {
					t.Fatal(err)
				}
				c, _ := dimensionExtraSnapshot(t, g)
				if c.Stats[dimensionGateExtraKey] != 0 {
					t.Fatal("old bonus resurrected by new service day entry", c)
				}
			}
			cmd.Slot = 20
			cmd.ItemID = 10158125
			cmd.Count = 1
			cmd.UpdatedAt = entry.UpdatedAt
			if r, err := o.GrantDimensionGateExtraEntry(context.Background(), cmd); err != nil || r.Granted != 1 || r.Total != 6 {
				t.Fatal(r, err)
			}
			c, _ := dimensionExtraSnapshot(t, g)
			used, extra, total, err := DimensionGateDailyAllowance(c, cmd.UpdatedAt, 5, 5)
			wantUsed := int64(0)
			if entryFirst {
				wantUsed = 1
			}
			if err != nil || used != wantUsed || extra != 1 || total != 6 {
				t.Fatal(used, extra, total, err)
			}
		})
	}
}

func TestDimensionExtraConcurrentGrantsRespectDailyCap(t *testing.T) {
	g, o, cmd := dimensionExtraFixture(t)
	cmd.Cooldown = 0
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := o.GrantDimensionGateExtraEntry(context.Background(), cmd)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrDailyEntryLimit) {
			t.Fatal(err)
		}
	}
	c, b := dimensionExtraSnapshot(t, g)
	if accepted != 5 || c.Stats[dimensionGateExtraKey] != 5 || b.Slots["0:20"].Count != 15 {
		t.Fatal(accepted, c, b)
	}
}

func TestDimensionExtraRejectionsPreserveAllAssets(t *testing.T) {
	for _, name := range []string{"account", "level", "dungeon", "wrong_slot", "wrong_item", "expired_stack", "expired_definition", "projection", "negative_grant", "invalid_policy", "overflow_policy", "base_not_exhausted", "five_after_one", "cooldown", "negative_state", "excess_state"} {
		t.Run(name, func(t *testing.T) {
			g, o, cmd := dimensionExtraFixture(t)
			c, b := dimensionExtraSnapshot(t, g)
			switch name {
			case "account":
				cmd.AccountID = "foreign"
			case "level":
				c.Level = 69
			case "dungeon":
				c.Location.DungeonID = 7165
			case "wrong_slot":
				cmd.Slot = 22
			case "wrong_item":
				cmd.ItemID = 10158692
			case "expired_stack":
				s := b.Slots["0:20"]
				s.ExpireAt = cmd.UpdatedAt
				b.Slots["0:20"] = s
			case "expired_definition":
				cmd.ExpiresAt = cmd.UpdatedAt
			case "projection":
				cmd.Project = func(int16, dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
					return dnfrepo.ItemStack{}, errors.New("projection failure")
				}
			case "negative_grant":
				cmd.Count = -1
			case "invalid_policy":
				cmd.ExtraLimit = -1
			case "overflow_policy":
				cmd.BaseLimit = math.MaxInt64
			case "base_not_exhausted":
				cmd.RequireBaseExhausted = true
			case "five_after_one", "cooldown":
				if _, err := o.GrantDimensionGateExtraEntry(context.Background(), cmd); err != nil {
					t.Fatal(err)
				}
				c, b = dimensionExtraSnapshot(t, g)
				if name == "five_after_one" {
					cmd.Slot = 21
					cmd.ItemID = 10158692
					cmd.Count = 5
					cmd.UpdatedAt = cmd.UpdatedAt.Add(10 * time.Second)
				}
			case "negative_state", "excess_state":
				c.Stats[dimensionGateDayKey] = premium.ServiceDayID(cmd.UpdatedAt)
				c.Stats[dimensionGateExtraKey] = -1
				if name == "excess_state" {
					c.Stats[dimensionGateExtraKey] = 6
				}
			}
			if err := g.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			beforeC, beforeB := dimensionExtraSnapshot(t, g)
			if r, err := o.GrantDimensionGateExtraEntry(context.Background(), cmd); err == nil || !reflect.DeepEqual(r, DimensionGateExtraEntryResult{}) {
				t.Fatal(r, err)
			}
			afterC, afterB := dimensionExtraSnapshot(t, g)
			if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeB, afterB) {
				t.Fatal("rejected grant changed assets")
			}
		})
	}
}

func TestDimensionExtraRollbackAfterBothWritesAndRetry(t *testing.T) {
	g, o, cmd := dimensionExtraFixture(t)
	beforeC, beforeB := dimensionExtraSnapshot(t, g)
	fault := errors.New("before commit")
	underlying := g.CharacterAssets
	g.CharacterAssets = epicRollbackAssets{underlying, fault}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := o.GrantDimensionGateExtraEntry(context.Background(), cmd); !errors.Is(err, fault) || !reflect.DeepEqual(r, DimensionGateExtraEntryResult{}) {
		t.Fatal(r, err)
	}
	afterC, afterB := dimensionExtraSnapshot(t, g)
	if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeB, afterB) {
		t.Fatal("failed commit changed assets")
	}
	g.CharacterAssets = underlying
	o, err = NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := o.GrantDimensionGateExtraEntry(context.Background(), cmd); err != nil || r.Granted != 1 || r.Slot.CountAfter != 19 {
		t.Fatal(r, err)
	}
}
