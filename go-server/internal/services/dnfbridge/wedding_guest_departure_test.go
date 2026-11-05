package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingGuestDepartureLoginFixture(t *testing.T) (*weddingChurchLoadingFixture, *gameSession, wedding.Owner, wedding.PairRequest, *wedding.GuestRules, wedding.GuestLedger, r.CharacterRecord) {
	t.Helper()
	f := newWeddingChurchLoadingFixture(t)
	ctx := context.Background()
	peer := &gameSession{conn: &bufferConn{}, connID: "offline-guest-31", accountID: "wedding:31", selectedCharacterID: 31, characterGeneration: 31, channel: f.first.channel, residentChannel: f.first.channel}
	f.s.gameSessions[31] = peer
	c := r.CharacterRecord{CharacterID: "31", AccountID: peer.accountID, Name: "offline-guest", Level: 90, Stats: map[string]int64{"town_id": 39, "area_id": 0, "area_state": 3, "gold": 20000}}
	if err := f.repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "31", Slots: map[string]r.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	owner, err := f.s.currentWeddingOwner()
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := f.repos.Character.Load(ctx, "29")
	b, _, _ := f.repos.Character.Load(ctx, "30")
	req := wedding.PairRequest{Actor: wedding.Actor{AccountID: a.AccountID, CharacterID: a.CharacterID}, Partner: wedding.Actor{AccountID: b.AccountID, CharacterID: b.CharacterID}, Now: f.now}
	rules := &wedding.GuestRules{MinimumGold: 10000, MaximumGold: 400000000, Charge: 50, Capacity: map[byte]int{0: 1}}
	ledger, err := owner.EnsureGuestWedding(ctx, req, rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.InviteWeddingGuests(ctx, req, rules, []string{"31"}); err != nil {
		t.Fatal(err)
	}
	entry, err := owner.EnterGuestChurch(ctx, wedding.Actor{AccountID: peer.accountID, CharacterID: "31"}, ledger.Key, f.now, wedding.CeremonyPosition{X: 670, Y: 330, Direction: 5}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	return f, peer, owner, req, rules, ledger, entry.Guest
}

func TestWeddingGuestDepartureInitialLoginUsesPVFLandingAfterRefundOrSettlement(t *testing.T) {
	for _, settled := range []bool{false, true} {
		t.Run(map[bool]string{false: "refunded", true: "settled"}[settled], func(t *testing.T) {
			f, peer, owner, req, rules, ledger, guest := weddingGuestDepartureLoginFixture(t)
			ctx := context.Background()
			for _, id := range []string{"29", "30"} {
				c, _, _ := f.repos.Character.Load(ctx, id)
				if settled {
					c.Stats["wedding_v1_phase"], c.Stats["wedding_v1_married_at"] = int64(wedding.Married), ledger.At+60
				} else {
					c.Stats["wedding_v1_phase"], c.Stats["wedding_v1_ceremony_at"] = int64(wedding.Engaged), 0
				}
				if err := r.SaveCharacterFields(ctx, f.repos.Character, c, r.CharacterFieldWedding); err != nil {
					t.Fatal(err)
				}
			}
			if settled {
				f.now = f.now.Add(360000000000)
				req.Now = f.now
				if _, err := owner.FinalizeGuestWedding(ctx, req, ledger.At, rules); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := owner.RefundGuestWedding(ctx, req, ledger.At); err != nil {
					t.Fatal(err)
				}
			}
			peer.townMu.Lock()
			c, town, area, row, path, err := f.s.currentCharacterListLoginTransition(ctx, peer, 31, guest)
			peer.townMu.Unlock()
			if err != nil || town != 38 || area != 1 || row.Value1 != 450 || row.Value2 != 234 || !townMapPathLooksLikeSeria(path) || c.Stats["wedding_v1_guest_key"] != 0 {
				t.Fatalf("login departure %d/%d row=%+v path=%s err=%v stats=%+v", town, area, row, path, err, c.Stats)
			}
			stored, _, _ := f.repos.Character.Load(ctx, "31")
			if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 || stored.Stats["wedding_v1_guest_at"] != 0 || stored.Stats["gold"] != 20000 {
				t.Fatal("marker/location not durable together")
			}
		})
	}
}

type weddingGuestDepartureReadFailure struct {
	r.SettingsRepository
	failure error
}

func (f weddingGuestDepartureReadFailure) Load(context.Context, string) (r.SettingsRecord, bool, error) {
	return r.SettingsRecord{}, false, f.failure
}

func TestWeddingGuestDepartureActiveAndTransientReadErrorPreserveVisit(t *testing.T) {
	f, peer, _, _, _, _, guest := weddingGuestDepartureLoginFixture(t)
	ctx := context.Background()
	current, moved, err := f.s.recoverCurrentWeddingGuestLogin(ctx, peer, 31, guest)
	if err != nil || moved || !reflect.DeepEqual(current, guest) {
		t.Fatalf("active visit changed: %v %v", moved, err)
	}
	failure := errors.New("transient settings connection failure")
	g := f.repos
	g.Settings = weddingGuestDepartureReadFailure{g.Settings, failure}
	f.s.repositoryProvider = func() (r.Group, bool) { return g, true }
	peer.townMu.Lock()
	_, _, _, _, _, err = f.s.currentCharacterListLoginTransition(ctx, peer, 31, guest)
	peer.townMu.Unlock()
	if !errors.Is(err, failure) {
		t.Fatalf("temporary error was treated as terminal: %v", err)
	}
	after, _, _ := f.repos.Character.Load(ctx, "31")
	if !reflect.DeepEqual(after, guest) {
		t.Fatal("temporary read error moved guest")
	}
}
