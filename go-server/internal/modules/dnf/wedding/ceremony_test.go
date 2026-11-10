package wedding

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func reservedCeremonyFixture(t *testing.T) (Owner, PairRequest, int64) {
	t.Helper()
	o, now := ownerFixture(t)
	req := pairRequest(now)
	ctx := context.Background()
	if _, err := o.Engage(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := o.Repositories.Quest.Save(ctx, r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}, 20738: {Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.Reserve(ctx, req, 0, 0, 20261101); err != nil {
		t.Fatal(err)
	}
	return o, req, now.Unix() + 300
}

func ceremonyTestGrant(inv *r.InventoryRecord, id, count int64, _ time.Time) error {
	if inv.Slots == nil {
		inv.Slots = map[string]r.ItemStack{}
	}
	if inv.Slots["0:65"].Count != 0 {
		return ErrItem
	}
	inv.Slots["0:65"] = r.ItemStack{ItemID: id, Count: count}
	return nil
}

func TestCeremonyStartsOnlyDueWithBothCommittedInChurchAndGrantsOnce(t *testing.T) {
	o, req, at := reservedCeremonyFixture(t)
	ctx := context.Background()
	if _, err := o.StartCeremony(ctx, req, at, ceremonyTestGrant); !errors.Is(err, ErrState) {
		t.Fatalf("early start %v", err)
	}
	if _, err := o.EnterChurch(ctx, req, at, CeremonyPosition{X: 670, Y: 330}); err != nil {
		t.Fatal(err)
	}
	req.Now = time.Unix(at, 0)
	if _, err := o.StartCeremony(ctx, req, at, ceremonyTestGrant); !errors.Is(err, ErrState) {
		t.Fatalf("missing second location %v", err)
	}
	other := req
	other.Actor, other.Partner = req.Partner, req.Actor
	if _, err := o.EnterChurch(ctx, other, at, CeremonyPosition{X: 685, Y: 360}); err != nil {
		t.Fatal(err)
	}
	result, err := o.StartCeremony(ctx, req, at, ceremonyTestGrant)
	if err != nil {
		t.Fatal(err)
	}
	x, y, err := ValidatePair(result.Actor, result.Partner)
	if err != nil || x.Phase != Married || y.Phase != Married || x.MarriedAt != at || x.Ring != 0 {
		t.Fatalf("marriage %+v/%+v %v", x, y, err)
	}
	for _, id := range []string{"1", "2"} {
		if result.Inventories[id].Slots["0:65"].ItemID != 10002629 || result.Inventories[id].Slots["0:65"].Count != 1 {
			t.Fatalf("gift %s %+v", id, result.Inventories[id])
		}
	}
	if _, err = o.StartCeremony(ctx, req, at, ceremonyTestGrant); !errors.Is(err, ErrState) {
		t.Fatalf("replayed start %v", err)
	}
}

func TestCeremonySecondGiftFailureRollsBackBothAndKeepsReservationRecoverable(t *testing.T) {
	o, req, at := reservedCeremonyFixture(t)
	ctx := context.Background()
	_, _ = o.EnterChurch(ctx, req, at, CeremonyPosition{X: 670, Y: 330})
	other := req
	other.Actor, other.Partner = req.Partner, req.Actor
	_, _ = o.EnterChurch(ctx, other, at, CeremonyPosition{X: 685, Y: 360})
	req.Now = time.Unix(at, 0)
	beforeA, _, _ := o.Repositories.Character.Load(ctx, "1")
	beforeB, _, _ := o.Repositories.Character.Load(ctx, "2")
	failure := errors.New("second owner inventory full")
	grant := func(inv *r.InventoryRecord, id, count int64, now time.Time) error {
		if inv.CharacterID == "2" {
			return failure
		}
		return ceremonyTestGrant(inv, id, count, now)
	}
	if _, err := o.StartCeremony(ctx, req, at, grant); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	a, _, _ := o.Repositories.Character.Load(ctx, "1")
	b, _, _ := o.Repositories.Character.Load(ctx, "2")
	if !reflect.DeepEqual(a, beforeA) || !reflect.DeepEqual(b, beforeB) {
		t.Fatal("partial marriage persisted")
	}
	inv, _, _ := o.Repositories.Inventory.Load(ctx, "1")
	if len(inv.Slots) != 0 {
		t.Fatal("partial gift persisted")
	}
	if _, err := o.StartCeremony(ctx, req, at, ceremonyTestGrant); err != nil {
		t.Fatal(err)
	}
}

func TestCeremonyRouteCannotBeOmittedOrRedirected(t *testing.T) {
	o, now := ownerFixture(t)
	req := pairRequest(now)
	ctx := context.Background()
	if _, err := o.Engage(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := o.Repositories.Quest.Save(ctx, r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed"}, 20738: {Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, route := range []*CeremonyRoute{nil, {}, {Channel: 1, Area: 7}, {Channel: 1, Area: 39}} {
		req.CeremonyRoute = route
		if _, err := o.Reserve(ctx, req, 0, 0, 20261101); !errors.Is(err, ErrState) {
			t.Fatalf("invalid route %+v: %v", route, err)
		}
	}
	a, _, err := o.Repositories.Character.Load(ctx, "1")
	if err != nil || a.Stats["gold"] != 10_000_000 {
		t.Fatalf("rejected route charged wallet: %+v %v", a.Stats, err)
	}
	assertPair(t, o, Engaged, 0)
}

func TestCeremonyWrongTimerOrOwnerCannotEnter(t *testing.T) {
	o, req, at := reservedCeremonyFixture(t)
	ctx := context.Background()
	if _, err := o.EnterChurch(ctx, req, at+1, CeremonyPosition{X: 1, Y: 1}); !errors.Is(err, ErrState) {
		t.Fatalf("stale entry %v", err)
	}
	req.Actor.AccountID = "other"
	if _, err := o.EnterChurch(ctx, req, at, CeremonyPosition{X: 1, Y: 1}); !errors.Is(err, ErrOwnership) {
		t.Fatalf("foreign entry %v", err)
	}
}

func TestCeremonyExpirationPreservesPaidCostsAndDailyLimitAndReturnsChurchOccupants(t *testing.T) {
	o, req, at := reservedCeremonyFixture(t)
	ctx := context.Background()
	if _, err := o.EnterChurch(ctx, req, at, CeremonyPosition{X: 670, Y: 330}); err != nil {
		t.Fatal(err)
	}
	req.Now = time.Unix(at+CeremonyAdmissionGraceSeconds, 0)
	if _, err := o.ExpireReservation(ctx, req, at, nil); !errors.Is(err, ErrState) {
		t.Fatalf("expired inside admission window: %v", err)
	}
	req.Now = req.Now.Add(time.Second)
	if _, err := o.ExpireReservation(ctx, req, at, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Church occupant was stranded: %v", err)
	}
	if _, err := o.EnterChurch(ctx, req, at, CeremonyPosition{X: 670, Y: 330}); !errors.Is(err, ErrState) {
		t.Fatalf("late old entry accepted: %v", err)
	}
	result, err := o.ExpireReservation(ctx, req, at, map[string]CeremonyReturnLocation{"1": {Town: 38, Area: 1, X: 474, Y: 234, Direction: 2}})
	if err != nil {
		t.Fatal(err)
	}
	x, y, err := ValidatePair(result.Actor, result.Partner)
	if err != nil || x.Phase != Engaged || y.Phase != Engaged || x.CeremonyAt != 0 || x.CeremonyArea != 0 || x.LastWeddingDay != 20261101 || y.LastWeddingDay != 20261101 || result.Actor.Stats["gold"] != 9_500_000 || result.Actor.Stats["town_id"] != 38 || result.Actor.Stats["pos_x"] != 474 {
		t.Fatalf("expiration changed costs/limits or retained Church: %+v %+v %v", result, x, err)
	}
	if _, err = o.StartCeremony(ctx, req, at, ceremonyTestGrant); !errors.Is(err, ErrState) {
		t.Fatalf("stale award after expiration: %v", err)
	}
	if _, err = o.Reserve(ctx, req, 0, 0, 20261101); !errors.Is(err, ErrState) {
		t.Fatalf("expiration refunded daily application: %v", err)
	}
	if _, err = o.Reserve(ctx, req, 0, 0, 20261102); err != nil {
		t.Fatalf("next day could not create a real new reservation: %v", err)
	}
}

func TestCeremonyReadyAdmissionSurvivesGiftFailureAndDoesNotExpire(t *testing.T) {
	o, req, at := reservedCeremonyFixture(t)
	ctx := context.Background()
	other := req
	other.Actor, other.Partner = req.Partner, req.Actor
	for _, entrant := range []PairRequest{req, other} {
		if _, err := o.EnterChurch(ctx, entrant, at, CeremonyPosition{X: 670, Y: 330}); err != nil {
			t.Fatal(err)
		}
	}
	req.Now = time.Unix(at, 0)
	req.Revalidate = func() bool { return true }
	if _, err := o.MarkCeremonyReady(ctx, req, at); err != nil {
		t.Fatal(err)
	}
	full := errors.New("gift inventory full")
	if _, err := o.StartCeremony(ctx, req, at, func(*r.InventoryRecord, int64, int64, time.Time) error { return full }); !errors.Is(err, full) {
		t.Fatal(err)
	}
	req.Now = time.Unix(at+600, 0)
	if _, err := o.ExpireReservation(ctx, req, at, nil); !errors.Is(err, ErrState) {
		t.Fatalf("fully admitted pair expired while freeing inventory: %v", err)
	}
	result, err := o.StartCeremony(ctx, req, at, ceremonyTestGrant)
	if err != nil {
		t.Fatalf("retry without leaving Church failed: %v", err)
	}
	x, _, err := ValidatePair(result.Actor, result.Partner)
	if err != nil || x.CeremonyReadyAt != at || x.Phase != Married || result.Actor.Stats["gold"] != 9_500_000 {
		t.Fatalf("admission/cost lost on retry: %+v %v", x, err)
	}
}
