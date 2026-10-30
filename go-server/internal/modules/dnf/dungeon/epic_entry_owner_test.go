package dungeon

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"sync"
	"testing"
	"time"
)

func epicOwnerFixture(t *testing.T) (*Owner, repo.Group, EpicEntryCommand) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	c := repo.CharacterRecord{CharacterID: "31", AccountID: "own10029", Level: 90, Stats: map[string]int64{"fatigue": 100, "fatigue_limit": 156, "keep": 7}}
	if err := g.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: 90}, "0:2": {ItemID: 3329, Count: 7}}}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	return o, g, EpicEntryCommand{CharacterID: "31", AccountID: "own10029", DungeonID: 6063, Nonce: 99, UpdatedAt: time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)}
}
func epicBalance(t *testing.T, g repo.Group) (int64, int64, int64) {
	t.Helper()
	c, _, e := g.Character.Load(context.Background(), "31")
	if e != nil {
		t.Fatal(e)
	}
	v, _, e := g.Inventory.Load(context.Background(), "31")
	if e != nil {
		t.Fatal(e)
	}
	if c.Stats["keep"] != 7 || v.Slots["0:2"].Count != 7 {
		t.Fatal("unrelated changed")
	}
	return c.Stats[EpicEntrySequenceKey], c.Stats["fatigue"], v.Slots["0:1"].Count
}
func TestEpicEntryOwnerAtomicReceiptReplayAndNextRun(t *testing.T) {
	o, g, cmd := epicOwnerFixture(t)
	if r, e := o.ConsumeEpicEntry(context.Background(), cmd); e != nil || r.Replay || r.FatigueSpent != 1 {
		t.Fatal(r, e)
	}
	if r, e := o.ConsumeEpicEntry(context.Background(), cmd); e != nil || !r.Replay || r.FatigueSpent != 1 {
		t.Fatal(r, e)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 1 || fp != 99 || tickets != 60 {
		t.Fatal(seq, fp, tickets)
	}
	foreign := cmd
	foreign.Nonce = 100
	if _, e := o.ConsumeEpicEntry(context.Background(), foreign); e == nil {
		t.Fatal("foreign same-sequence receipt accepted")
	}
	cmd.ExpectedSequence = 1
	cmd.Nonce = 101
	cmd.DungeonID = 6064
	if _, e := o.ConsumeEpicEntry(context.Background(), cmd); e != nil {
		t.Fatal(e)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 2 || fp != 98 || tickets != 15 {
		t.Fatal(seq, fp, tickets)
	}
}

type epicRollbackAssets struct {
	repo.CharacterAssetUnitOfWork
	failure error
}

func (v epicRollbackAssets) WithinCharacterAssets(ctx context.Context, id string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return v.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, b repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := fn(c, b, e); err != nil {
			return err
		}
		return v.failure
	})
}
func TestEpicEntryOwnerRollbackAfterBothWritesAndRetry(t *testing.T) {
	_, g, cmd := epicOwnerFixture(t)
	under := g.CharacterAssets
	fault := errors.New("before physical commit")
	g.CharacterAssets = epicRollbackAssets{under, fault}
	o, e := NewOwner(g)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.ConsumeEpicEntry(context.Background(), cmd); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 0 || fp != 100 || tickets != 90 {
		t.Fatal(seq, fp, tickets)
	}
	g.CharacterAssets = under
	o, e = NewOwner(g)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.ConsumeEpicEntry(context.Background(), cmd); e != nil {
		t.Fatal(e)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 1 || fp != 99 || tickets != 60 {
		t.Fatal(seq, fp, tickets)
	}
}
func TestEpicEntryOwnerRejectsOwnerCostAndStaleIdentity(t *testing.T) {
	for _, name := range []string{"account", "character", "dungeon", "sequence", "projection", "generation", "insufficient", "fatigue"} {
		t.Run(name, func(t *testing.T) {
			o, g, cmd := epicOwnerFixture(t)
			switch name {
			case "account":
				cmd.AccountID = "foreign"
			case "character":
				cmd.CharacterID = "32"
			case "dungeon":
				cmd.DungeonID = 6062
			case "sequence":
				cmd.ExpectedSequence = 1
			case "projection":
				cmd.Project = func(int16, repo.ItemStack) (repo.ItemStack, error) { return repo.ItemStack{}, errors.New("projection") }
			case "generation":
				n := 0
				cmd.Validate = func() error {
					n++
					if n == 2 {
						return errors.New("stale identity")
					}
					return nil
				}
			case "insufficient":
				v, _, _ := g.Inventory.Load(context.Background(), "31")
				v.Slots["0:1"] = repo.ItemStack{ItemID: 3330, Count: 29}
				g.Inventory.Save(context.Background(), v)
			case "fatigue":
				c, _, _ := g.Character.Load(context.Background(), "31")
				c.Stats["fatigue"] = 0
				g.Character.Save(context.Background(), c)
			}
			if _, e := o.ConsumeEpicEntry(context.Background(), cmd); e == nil {
				t.Fatal("illegal admission accepted")
			}
			seq, fp, tickets := epicBalance(t, g)
			wantFP, wantTickets := int64(100), int64(90)
			if name == "fatigue" {
				wantFP = 0
			}
			if name == "insufficient" {
				wantTickets = 29
			}
			if seq != 0 || fp != wantFP || tickets != wantTickets {
				t.Fatal(seq, fp, tickets)
			}
		})
	}
}
func TestEpicEntryOwnerConcurrentPreparedReceiptsChargeOnce(t *testing.T) {
	o, g, cmd := epicOwnerFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for n := int64(1); n <= 16; n++ {
		wg.Add(1)
		go func(n int64) {
			defer wg.Done()
			c := cmd
			c.Nonce = n
			if _, e := o.ConsumeEpicEntry(context.Background(), c); e == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	if success != 1 {
		t.Fatal("successful distinct prepared entries", success)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 1 || fp != 99 || tickets != 60 {
		t.Fatal(seq, fp, tickets)
	}
}
