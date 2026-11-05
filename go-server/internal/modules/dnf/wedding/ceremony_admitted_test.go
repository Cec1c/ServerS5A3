package wedding

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestAdmittedCeremonySeparatesMovieAndDurableCompletion(t *testing.T) {
	o, req, at := reservedCeremonyFixture(t)
	ctx := context.Background()
	if _, e := o.BeginAdmittedCeremony(ctx, req, at); !errors.Is(e, ErrState) {
		t.Fatal("unadmitted begin", e)
	}
	if _, e := o.EnterChurch(ctx, req, at, CeremonyPosition{X: 670, Y: 330}); e != nil {
		t.Fatal(e)
	}
	other := req
	other.Actor, other.Partner = req.Partner, req.Actor
	if _, e := o.EnterChurch(ctx, other, at, CeremonyPosition{X: 685, Y: 360}); e != nil {
		t.Fatal(e)
	}
	req.Now = time.Unix(at+10, 0)
	req.Revalidate = func() bool { return true }
	if _, e := o.MarkCeremonyReady(ctx, req, at); e != nil {
		t.Fatal(e)
	}
	if _, e := o.BeginAdmittedCeremony(ctx, req, at); e != nil {
		t.Fatal(e)
	}
	assertPair(t, o, InCeremony, 0)
	for _, id := range []string{"1", "2"} {
		bag, _, _ := o.Repositories.Inventory.Load(ctx, id)
		if len(bag.Slots) != 0 {
			t.Fatal("movie begin minted gift")
		}
	}
	req.Now = time.Unix(at+69, 0)
	if _, e := o.FinishAdmittedCeremony(ctx, req, at, ceremonyTestGrant); !errors.Is(e, ErrState) {
		t.Fatal("early completion", e)
	}
	// Native cleanup has already moved both spouses out of the Church.
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		c.Stats["town_id"], c.Stats["area_id"] = 39, 0
		if e := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); e != nil {
			t.Fatal(e)
		}
	}
	req.Now = time.Unix(at+70, 0)
	fault := errors.New("second gift fault")
	if _, e := o.FinishAdmittedCeremony(ctx, req, at, func(inv *r.InventoryRecord, id, count int64, now time.Time) error {
		if inv.CharacterID == "2" {
			return fault
		}
		return ceremonyTestGrant(inv, id, count, now)
	}); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	assertPair(t, o, InCeremony, 0)
	for _, id := range []string{"1", "2"} {
		bag, _, _ := o.Repositories.Inventory.Load(ctx, id)
		if len(bag.Slots) != 0 {
			t.Fatal("partial gift on failed end")
		}
	}
	if _, e := o.FinishAdmittedCeremony(ctx, req, at, ceremonyTestGrant); e != nil {
		t.Fatal(e)
	}
	assertPair(t, o, Married, 0)
	if _, e := o.FinishAdmittedCeremony(ctx, req, at, ceremonyTestGrant); !errors.Is(e, ErrState) {
		t.Fatal("duplicate end", e)
	}
	for _, id := range []string{"1", "2"} {
		bag, _, _ := o.Repositories.Inventory.Load(ctx, id)
		if bag.Slots["0:65"].Count != 1 {
			t.Fatal("end gift was lost or duplicated")
		}
	}
}
