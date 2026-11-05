package wedding

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func ownerFixture(t *testing.T) (Owner, time.Time) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	for i, job := range []string{"0", "11", "6"} {
		id := strconv.Itoa(i + 1)
		c := r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Job: job, Level: 30, Stats: map[string]int64{"gold": 10_000_000}}
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	rules := &Rules{MinimumLevel: 30, MaxLovePoint: 999, QuestIDs: []int64{20737, 20738}, Durations: []int64{300, 600, 900}, Ceremonies: map[byte]Ceremony{0: {Kind: 0, Gold: 500000}}, Rings: []Ring{{Stage: 0}, {Stage: 1, TicketID: 10002611, TicketCount: 1}, {Stage: 2, TicketID: 10002612, TicketCount: 1}}}
	rules.CeremonyGifts = map[byte]Gift{0: {ItemID: 10002629, Count: 1}}
	rules.CeremonySeconds = 60
	return Owner{g, rules}, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
}

func pairRequest(now time.Time) PairRequest {
	return PairRequest{Actor: Actor{"a1", "1"}, Partner: Actor{"a2", "2"}, Now: now, CeremonyRoute: &CeremonyRoute{Channel: 10, Area: 8}}
}
func assertPair(t *testing.T, o Owner, phase byte, ring int) {
	t.Helper()
	a, _, _ := o.Repositories.Character.Load(context.Background(), "1")
	b, _, _ := o.Repositories.Character.Load(context.Background(), "2")
	x, y, e := ValidatePair(a, b)
	if e != nil || x.Phase != phase || y.Phase != phase || x.Ring != ring || y.Ring != ring {
		t.Fatalf("pair=%+v/%+v err=%v", x, y, e)
	}
}

func TestConsentAndReservationRequireBothOwnedCharactersAndQuests(t *testing.T) {
	o, now := ownerFixture(t)
	ctx := context.Background()
	req := pairRequest(now)
	bad := req
	bad.Actor.AccountID = "other"
	if _, e := o.Engage(ctx, bad); !errors.Is(e, ErrOwnership) {
		t.Fatalf("foreign owner: %v", e)
	}
	if _, e := o.Engage(ctx, req); e != nil {
		t.Fatal(e)
	}
	assertPair(t, o, Engaged, 0)
	if _, e := o.Engage(ctx, req); !errors.Is(e, ErrState) {
		t.Fatalf("duplicate consent: %v", e)
	}
	for _, id := range []string{"1", "2"} {
		if _, e := o.Reserve(ctx, req, 0, 0, 20261101); !errors.Is(e, ErrQuest) {
			t.Fatalf("missing other preparation: %v", e)
		}
		q := r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}, 20738: {Status: "completed"}}}
		if e := o.Repositories.Quest.Save(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	result, e := o.Reserve(ctx, req, 0, 0, 20261101)
	if e != nil {
		t.Fatal(e)
	}
	if result.Actor.Stats["gold"] != 9_500_000 || result.Partner.Stats["gold"] != 10_000_000 {
		t.Fatal("reservation charged wrong wallet")
	}
	assertPair(t, o, Reserved, 0)
	if _, e = o.Reserve(ctx, req, 0, 0, 20261101); !errors.Is(e, ErrState) {
		t.Fatal("duplicate reservation accepted")
	}
	if _, e = o.Begin(ctx, req, now.Unix()+300); !errors.Is(e, ErrState) {
		t.Fatal("early ceremony accepted")
	}
	req.Now = now.Add(300 * time.Second)
	if _, e = o.Begin(ctx, req, now.Unix()+301); !errors.Is(e, ErrState) {
		t.Fatal("stale reservation generation accepted")
	}
	if _, e = o.Begin(ctx, req, now.Unix()+300); e != nil {
		t.Fatal(e)
	}
	if _, e = o.EnterChurch(ctx, req, now.Unix()+300, CeremonyPosition{X: 670, Y: 330}); e != nil {
		t.Fatal(e)
	}
	other := req
	other.Actor, other.Partner = req.Partner, req.Actor
	if _, e = o.EnterChurch(ctx, other, now.Unix()+300, CeremonyPosition{X: 685, Y: 360}); e != nil {
		t.Fatal(e)
	}
	failure := errors.New("gift has no real destination capacity")
	if _, e = o.Complete(ctx, req, now.Unix()+300, func(*r.InventoryRecord, int64, int64, time.Time) error { return failure }); !errors.Is(e, failure) {
		t.Fatalf("gift failure: %v", e)
	}
	assertPair(t, o, InCeremony, 0)
	if _, e = o.Complete(ctx, req, now.Unix()+300, nil); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("missing grant accepted: %v", e)
	}
	if _, e = o.Complete(ctx, req, now.Unix()+300, func(inv *r.InventoryRecord, id, count int64, _ time.Time) error {
		if inv.Slots == nil {
			inv.Slots = map[string]r.ItemStack{}
		}
		inv.Slots["0:65"] = r.ItemStack{ItemID: id, Count: count}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	assertPair(t, o, Married, 0)
}

func TestConcurrentConsentCannotGiveOneCharacterTwoPartners(t *testing.T) {
	o, now := ownerFixture(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := pairRequest(now)
			if i%2 == 1 {
				req.Partner = Actor{"a3", "3"}
			}
			if _, e := o.Engage(context.Background(), req); e == nil {
				successes.Add(1)
			} else if !errors.Is(e, ErrState) {
				t.Errorf("competing consent: %v", e)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful partners: %d", successes.Load())
	}
}

func TestRingUpgradeDebitsOneActualTicketAndUpdatesOfflinePartner(t *testing.T) {
	o, now := ownerFixture(t)
	ctx := context.Background()
	req := pairRequest(now)
	if _, e := o.Engage(ctx, req); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		s, _ := Read(c)
		s.Phase = Married
		s.MarriedAt = now.Unix()
		write(&c, s)
		if e := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
			t.Fatal(e)
		}
	}
	i, _, _ := o.Repositories.Inventory.Load(ctx, "1")
	key := strconv.Itoa(int(r.MainInventoryListType)) + ":9"
	i.Slots = make(map[string]r.ItemStack)
	i.Slots[key] = r.ItemStack{ItemID: 10002611, Count: 1}
	if e := o.Repositories.Inventory.Save(ctx, i); e != nil {
		t.Fatal(e)
	}
	if _, e := o.UpgradeRing(ctx, req, 2, key); !errors.Is(e, ErrState) {
		t.Fatalf("skipped stage: %v", e)
	}
	if _, e := o.UpgradeRing(ctx, req, 1, key); e != nil {
		t.Fatal(e)
	}
	assertPair(t, o, Married, 1)
	i, _, _ = o.Repositories.Inventory.Load(ctx, "1")
	if _, ok := i.Slots[key]; ok {
		t.Fatal("ticket was not consumed")
	}
	if _, e := o.UpgradeRing(ctx, req, 1, key); !errors.Is(e, ErrState) {
		t.Fatalf("replayed upgrade: %v", e)
	}
	assertPair(t, o, Married, 1)
}

func TestLiveIdentityChangeRollsBackBothSides(t *testing.T) {
	o, now := ownerFixture(t)
	var calls int
	req := pairRequest(now)
	req.Revalidate = func() bool { calls++; return calls == 1 }
	if _, e := o.Engage(context.Background(), req); !errors.Is(e, ErrState) {
		t.Fatalf("stale identity: %v", e)
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(context.Background(), id)
		s, e := Read(c)
		if e != nil || s.Phase != Single || s.PartnerID != 0 {
			t.Fatalf("identity failure persisted relation: %+v %v", s, e)
		}
	}
}
