package wedding

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func guestDepartureFixture(t *testing.T) (Owner, PairRequest, *GuestRules, GuestLedger, r.CharacterRecord) {
	t.Helper()
	o, req, rules, ledger := guestOwnerFixture(t)
	ctx := context.Background()
	c, _, _ := o.Repositories.Character.Load(ctx, "3")
	c.Stats["area_state"] = 3
	if err := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	if _, err := o.InviteWeddingGuests(ctx, req, rules, []string{"3"}); err != nil {
		t.Fatal(err)
	}
	entry, err := o.EnterGuestChurch(ctx, Actor{"a3", "3"}, ledger.Key, req.Now, CeremonyPosition{X: 670, Y: 330, Direction: 5}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	return o, req, rules, ledger, entry.Guest
}

func guestDepartureDestination(r.CharacterRecord) (CeremonyReturnLocation, error) {
	return CeremonyReturnLocation{Town: 38, Area: 1, X: 450, Y: 234, Direction: 5}, nil
}

func TestWeddingGuestDepartureCancelledSettledAndStillActive(t *testing.T) {
	for _, mode := range []string{"cancelled", "settled", "active", "expired"} {
		t.Run(mode, func(t *testing.T) {
			o, req, rules, ledger, guest := guestDepartureFixture(t)
			ctx := context.Background()
			switch mode {
			case "cancelled":
				guestSetHostPhase(t, o, Engaged, 0)
				if _, err := o.RefundGuestWedding(ctx, req, ledger.At); err != nil {
					t.Fatal(err)
				}
			case "settled":
				guestSetHostPhase(t, o, Married, ledger.At)
				req.Now = req.Now.Add(360 * 1000000000)
				if _, err := o.FinalizeGuestWedding(ctx, req, ledger.At, rules); err != nil {
					t.Fatal(err)
				}
			case "expired":
				req.Now = req.Now.Add(361 * 1000000000)
			}
			beforeA, _, _ := o.Repositories.Character.Load(ctx, "1")
			beforeB, _, _ := o.Repositories.Character.Load(ctx, "2")
			scope, _ := r.WeddingPairScope([]string{"1", "2"})
			beforeLedger, _, _ := o.Repositories.Settings.Load(ctx, scope)
			got, moved, err := o.RecoverGuestChurchDeparture(ctx, Actor{"a3", "3"}, guest, req.Now, guestDepartureDestination, func() bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			if mode == "active" {
				if moved || !reflect.DeepEqual(got, guest) {
					t.Fatal("active visit retired")
				}
			} else {
				if !moved || got.Stats["town_id"] != 38 || got.Stats["area_id"] != 1 || got.Stats["pos_x"] != 450 || got.Stats[statPrefix+"guest_key"] != 0 || got.Stats[statPrefix+"guest_at"] != 0 || got.Stats["gold"] != guest.Stats["gold"] {
					t.Fatalf("departure %+v moved=%v", got.Stats, moved)
				}
			}
			afterA, _, _ := o.Repositories.Character.Load(ctx, "1")
			afterB, _, _ := o.Repositories.Character.Load(ctx, "2")
			afterLedger, _, _ := o.Repositories.Settings.Load(ctx, scope)
			if !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeB, afterB) || !reflect.DeepEqual(beforeLedger, afterLedger) {
				t.Fatal("departure changed host assets or gift ledger")
			}
		})
	}
}

type guestDepartureSettingsFailure struct {
	r.SettingsRepository
	err error
}

func (f guestDepartureSettingsFailure) Load(context.Context, string) (r.SettingsRecord, bool, error) {
	return r.SettingsRecord{}, false, f.err
}

type guestDepartureFailedTransaction struct {
	r.CharacterTradeUnitOfWork
	inner   r.WeddingGuestLifecycleUnitOfWork
	failure error
}

func (u guestDepartureFailedTransaction) WithinWeddingGuestLifecycle(ctx context.Context, hosts, guests []string, scope string, apply func(r.Group) error) error {
	return u.inner.WithinWeddingGuestLifecycle(ctx, hosts, guests, scope, func(g r.Group) error {
		g.Settings = guestDepartureSettingsFailure{g.Settings, u.failure}
		return apply(g)
	})
}

func TestWeddingGuestDepartureReadFailuresAndRetiredBindingDoNotMove(t *testing.T) {
	for _, mode := range []string{"index-read", "locked-ledger-read", "retired-binding", "new-visit"} {
		t.Run(mode, func(t *testing.T) {
			o, req, _, ledger, guest := guestDepartureFixture(t)
			ctx := context.Background()
			guestSetHostPhase(t, o, Engaged, 0)
			if _, err := o.RefundGuestWedding(ctx, req, ledger.At); err != nil {
				t.Fatal(err)
			}
			base := o.Repositories
			failure := errors.New("transient guest repository failure")
			switch mode {
			case "index-read":
				o.Repositories.Settings = guestDepartureSettingsFailure{o.Repositories.Settings, failure}
			case "locked-ledger-read":
				o.Repositories.CharacterTrade = guestDepartureFailedTransaction{o.Repositories.CharacterTrade, o.Repositories.CharacterTrade.(r.WeddingGuestLifecycleUnitOfWork), failure}
			case "new-visit":
				c := r.CloneCharacter(guest)
				c.Stats[statPrefix+"guest_key"]++
				if err := r.SaveCharacterFields(ctx, base.Character, c, r.CharacterFieldWedding); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := base.Character.Load(ctx, "3")
			calls := 0
			_, moved, err := o.RecoverGuestChurchDeparture(ctx, Actor{"a3", "3"}, guest, req.Now, guestDepartureDestination, func() bool { calls++; return mode != "retired-binding" || calls == 1 })
			if mode == "index-read" || mode == "locked-ledger-read" {
				if !errors.Is(err, failure) {
					t.Fatalf("transient failure reclassified: %v", err)
				}
			} else if mode == "retired-binding" {
				if !errors.Is(err, ErrOwnership) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, _, _ := base.Character.Load(ctx, "3")
			if moved || !reflect.DeepEqual(before, after) {
				t.Fatal("failure or newer entry changed guest position/marker")
			}
		})
	}
}
