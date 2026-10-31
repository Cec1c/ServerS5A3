package questmaterial

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

var testFailure = errors.New("injected failure")

func seed(t *testing.T, g r.Group, n int) []string {
	t.Helper()
	ids := []string{}
	ctx := context.Background()
	for i := 0; i < n; i++ {
		id := fmt.Sprint(101 + i)
		ids = append(ids, id)
		for _, e := range []error{g.Account.Save(ctx, r.AccountRecord{AccountID: "owner-" + id}), g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "owner-" + id, Name: "synthetic-" + id, Slot: i, Level: 90}), g.Quest.Save(ctx, r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{84001: {Status: "active"}}}), g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}})} {
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	return ids
}
func planOne(_ context.Context, _ string, _ r.QuestRecord) ([]Attempt, error) {
	return []Attempt{{QuestID: 84001, ItemID: 600001, Count: 1, MaxStack: 5}}, nil
}
func grantOne(inv *r.InventoryRecord, a Attempt) ([]int16, error) {
	if inv.Slots == nil {
		inv.Slots = map[string]r.ItemStack{}
	}
	s := inv.Slots["0:177"]
	s.ItemID = int64(a.ItemID)
	s.Count += a.Count
	inv.Slots["0:177"] = s
	return []int16{177}, nil
}
func held(t *testing.T, g r.Group, id string) int64 {
	t.Helper()
	inv, _, e := g.Inventory.Load(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	return inv.Slots["0:177"].Count
}
func recoverOne(t *testing.T, g r.Group, id string) Result {
	t.Helper()
	v, e := Recover(context.Background(), g, "owner-"+id, id, grantOne)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestPersonalMaterialJournalMatrix(t *testing.T) {
	for n := 1; n <= 4; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			g := memory.NewMemoryGroup()
			ids := seed(t, g, n)
			calls := map[string]int{}
			plan := func(ctx context.Context, id string, q r.QuestRecord) ([]Attempt, error) {
				calls[id]++
				return planOne(ctx, id, q)
			}
			for i := 0; i < 3; i++ {
				if e := Prepare(context.Background(), g, "room-a", "monster-1", ids, plan); e != nil {
					t.Fatal(e)
				}
			}
			for _, id := range ids {
				v := recoverOne(t, g, id)
				if held(t, g, id) != 1 || calls[id] != 1 {
					t.Fatalf("%s inventory/plans %d/%d", id, held(t, g, id), calls[id])
				}
				if e := Acknowledge(context.Background(), g, id, v.Notices); e != nil {
					t.Fatal(e)
				}
				recoverOne(t, g, id)
				if held(t, g, id) != 1 {
					t.Fatal("duplicate delivery")
				}
			}
		})
	}
}

func TestPersonalMaterialFullMissCapAndReaccept(t *testing.T) {
	for _, mode := range []string{"full", "miss", "cap", "completed", "reaccepted", "notify-failed"} {
		t.Run(mode, func(t *testing.T) {
			g := memory.NewMemoryGroup()
			id := seed(t, g, 1)[0]
			ctx := context.Background()
			calls := 0
			plan := func(c context.Context, i string, q r.QuestRecord) ([]Attempt, error) {
				calls++
				a, _ := planOne(c, i, q)
				if mode == "miss" {
					a[0].Count = 0
				}
				return a, nil
			}
			if e := Prepare(ctx, g, "room-a", "monster-1", []string{id}, plan); e != nil {
				t.Fatal(e)
			}
			if mode == "completed" || mode == "reaccepted" {
				q, _, _ := g.Quest.Load(ctx, id)
				if mode == "completed" {
					x := q.States[84001]
					x.Status = "completed"
					q.States[84001] = x
				} else {
					q.States[84001] = r.QuestState{Status: "active"}
				}
				if e := g.Quest.Save(ctx, q); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "cap" {
				inv, _, _ := g.Inventory.Load(ctx, id)
				inv.Slots = map[string]r.ItemStack{}
				inv.Slots["0:177"] = r.ItemStack{ItemID: 600001, Count: 5}
				if e := g.Inventory.Save(ctx, inv); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "full" {
				v, e := Recover(ctx, g, "owner-"+id, id, func(inv *r.InventoryRecord, a Attempt) ([]int16, error) {
					_, _ = grantOne(inv, a)
					return nil, ErrBagFull
				})
				if e != nil || !v.Pending || held(t, g, id) != 0 {
					t.Fatalf("full rolled back: %+v %v", v, e)
				}
			}
			v := recoverOne(t, g, id)
			want := int64(1)
			switch mode {
			case "miss", "completed", "reaccepted":
				want = 0
			case "cap":
				want = 5
			}
			if held(t, g, id) != want {
				t.Fatalf("held=%d want=%d", held(t, g, id), want)
			}
			// Missing send ACK re-emits only a snapshot, including after reconnect.
			recoverOne(t, g, id)
			if held(t, g, id) != want {
				t.Fatal("retry changed balance")
			}
			if e := Acknowledge(ctx, g, id, v.Notices); e != nil {
				t.Fatal(e)
			}
			if e := Prepare(ctx, g, "room-a", "monster-1", []string{id}, plan); e != nil {
				t.Fatal(e)
			}
			recoverOne(t, g, id)
			if calls != 1 || held(t, g, id) != want {
				t.Fatal("duplicate re-rolled")
			}
		})
	}
}

type failSettlement struct {
	base  r.CharacterSettlementUnitOfWork
	stage string
}

func (u failSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(r.Group) error) error {
	err := u.base.WithinCharacterSettlement(ctx, id, func(tx r.Group) error {
		if u.stage == "save" {
			tx.Settings = &failSettings{tx.Settings}
		}
		if e := apply(tx); e != nil {
			return e
		}
		if u.stage == "rollback" {
			return testFailure
		}
		return nil
	})
	if err == nil && u.stage == "unknown" {
		return testFailure
	}
	return err
}

type failSettings struct{ r.SettingsRepository }

func (s *failSettings) Save(ctx context.Context, record r.SettingsRecord) error {
	if err := s.SettingsRepository.Save(ctx, record); err != nil {
		return err
	}
	return testFailure
}

type failPrepare struct {
	base    r.QuestMaterialPreparationUnitOfWork
	unknown bool
}

func (u failPrepare) WithinQuestMaterialPreparation(ctx context.Context, ids []string, apply func(r.Group) error) error {
	err := u.base.WithinQuestMaterialPreparation(ctx, ids, func(tx r.Group) error {
		if e := apply(tx); e != nil {
			return e
		}
		if !u.unknown {
			return testFailure
		}
		return nil
	})
	if err == nil && u.unknown {
		return testFailure
	}
	return err
}

func transactionCases(t *testing.T, g r.Group) {
	t.Helper()
	ctx := context.Background()
	ids := seed(t, g, 4)
	for _, mode := range []string{"save", "rollback", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			event := "failure-" + mode
			bad := g
			bad.QuestMaterialPlan = failPrepare{g.QuestMaterialPlan, false}
			if e := Prepare(ctx, bad, "room-a", event, ids, planOne); e == nil {
				t.Fatal("prepare failure ignored")
			}
			var j journal
			if e := loadJSON(ctx, g.Settings, scope(ids[0], "room-a"), &j); e != nil {
				t.Fatal(e)
			}
			if _, ok := j.Events[event]; ok {
				t.Fatal("partial preparation committed")
			}
			bad.QuestMaterialPlan = failPrepare{g.QuestMaterialPlan, true}
			if e := Prepare(ctx, bad, "room-a", event, ids, planOne); e == nil {
				t.Fatal("prepare unknown not surfaced")
			}
			if e := Prepare(ctx, g, "room-a", event, ids, func(context.Context, string, r.QuestRecord) ([]Attempt, error) {
				t.Fatal("unknown preparation re-planned")
				return nil, nil
			}); e != nil {
				t.Fatal(e)
			}
			id := ids[0]
			before := held(t, g, id)
			bad = g
			bad.CharacterSettlement = failSettlement{g.CharacterSettlement, mode}
			if _, e := Recover(ctx, bad, "owner-"+id, id, grantOne); e == nil {
				t.Fatal("settlement failure ignored")
			}
			want := before
			if mode == "unknown" {
				want++
			}
			if held(t, g, id) != want {
				t.Fatalf("failure balance=%d want=%d", held(t, g, id), want)
			}
			v := recoverOne(t, g, id)
			if held(t, g, id) != before+1 {
				t.Fatal("recovery omitted or duplicated grant")
			}
			if e := Acknowledge(ctx, g, id, v.Notices); e != nil {
				t.Fatal(e)
			}
			// A different member remains independently recoverable.
			for _, peer := range ids[1:] {
				v := recoverOne(t, g, peer)
				if e := Acknowledge(ctx, g, peer, v.Notices); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
	// Simultaneous reports/consumers serialize through character transactions.
	if e := Prepare(ctx, g, "room-b", "one-boss", ids, planOne); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			if _, e := Recover(ctx, g, "owner-"+id, id, grantOne); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if held(t, g, id) != 4 {
			t.Fatalf("concurrent %s held=%d", id, held(t, g, id))
		}
	}
	if _, e := Recover(ctx, g, "foreign", ids[0], grantOne); e == nil {
		t.Fatal("foreign owner accepted")
	}
}
func TestPersonalMaterialMemoryTransactions(t *testing.T) {
	transactionCases(t, memory.NewMemoryGroup())
}

func TestPersonalMaterialCorruptInboxFailsClosed(t *testing.T) {
	g := memory.NewMemoryGroup()
	id := seed(t, g, 1)[0]
	ctx := context.Background()
	if err := saveJSON(ctx, g.Settings, scope(id, "pending"), pending{Rooms: []string{"missing-room"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(ctx, g, "owner-"+id, id, grantOne); err == nil {
		t.Fatal("missing durable journal silently discarded")
	}
	var p pending
	if err := loadJSON(ctx, g.Settings, scope(id, "pending"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Rooms) != 1 {
		t.Fatal("lost unresolved inbox")
	}
}

func TestPersonalMaterialNoCandidateIsFinalAndHistoryPreserved(t *testing.T) {
	g := memory.NewMemoryGroup()
	id := seed(t, g, 1)[0]
	ctx := context.Background()
	q, _, _ := g.Quest.Load(ctx, id)
	q.States[90000] = r.QuestState{Status: "completed", Extra: map[string]string{"keep": "historical"}}
	if err := g.Quest.Save(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(ctx, g, "room-a", "no-candidate", []string{id}, func(context.Context, string, r.QuestRecord) ([]Attempt, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(ctx, g, "room-a", "no-candidate", []string{id}, func(context.Context, string, r.QuestRecord) ([]Attempt, error) {
		t.Fatal("late acceptance rerolled past death")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(ctx, g, "room-a", "next-monster", []string{id}, planOne); err != nil {
		t.Fatal(err)
	}
	recoverOne(t, g, id)
	q, _, _ = g.Quest.Load(ctx, id)
	if q.States[90000].Extra["keep"] != "historical" {
		t.Fatal("history changed")
	}
}

func TestPersonalMaterialFullRoomsDoNotStarveOtherDeliveries(t *testing.T) {
	g := memory.NewMemoryGroup()
	id := seed(t, g, 1)[0]
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		count := int64(1)
		if i == 4 {
			count = 2
		}
		if err := Prepare(ctx, g, fmt.Sprint("room-", i), "death", []string{id}, func(context.Context, string, r.QuestRecord) ([]Attempt, error) {
			return []Attempt{{QuestID: 84001, ItemID: 600001, Count: count, MaxStack: 100}}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(inv *r.InventoryRecord, a Attempt) ([]int16, error) {
		if a.Count == 1 {
			return nil, ErrBagFull
		}
		return grantOne(inv, a)
	}
	for i := 0; i < 2; i++ {
		if _, err := Recover(ctx, g, "owner-"+id, id, grant); err != nil {
			t.Fatal(err)
		}
	}
	if held(t, g, id) != 2 {
		t.Fatal("full rooms starved later deliverable event")
	}
}
