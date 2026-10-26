package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAppendedEpicActualTalismanPrepareCommit(t *testing.T) {
	for _, tc := range []struct {
		id                                       uint32
		tokens, tickets, wantTokens, wantTickets int64
	}{{6063, 1, 0, 0, 0}, {6064, 1, 15, 0, 0}, {6065, 2, 60, 0, 60}, {6065, 1, 60, 0, 30}, {6065, 0, 60, 0, 0}} {
		t.Run(fmt.Sprint(tc.id, "/", tc.tokens), func(t *testing.T) {
			s, a, _, g, _ := prepareAuditActual6063Owner(t)
			ctx := context.Background()
			if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: tc.tickets}, "0:2": {ItemID: 490010214, Count: tc.tokens}}}); err != nil {
				t.Fatal(err)
			}
			r, _, err := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: tc.id})
			if err != nil {
				t.Fatal("prepare", err)
			}
			if err = freezeCurrentDungeonTownReturnOrigin(a, r); err != nil {
				t.Fatal(err)
			}
			if err = s.commitDungeonRuntime(a, r); err != nil {
				t.Fatal("commit", err)
			}
			bag, _, err := g.Inventory.Load(ctx, "99")
			if err != nil || bag.Slots["0:1"].Count != tc.wantTickets || bag.Slots["0:2"].Count != tc.wantTokens {
				t.Fatal(bag, err)
			}
		})
	}
}

func TestAppendedSilentForestActualAdmissionLimit(t *testing.T) {
	s, a, _ := prepareSemesterActualReady(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	// An exhausted, current-day fatigue ledger must remain exhausted: 6062
	// is free even though ordinary admission refreshes stale daily ledgers.
	character, found, err := g.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	character.Stats["fatigue_service_day_id"] = premium.ServiceDayID(s.gameplayNow())
	character.Stats["fatigue_limit"] = 156
	character.Stats["fatigue_used"] = 156
	character.Stats["fatigue"] = 0
	if err = g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if _, found, err := g.Account.Load(ctx, a.accountID); err != nil {
		t.Fatal(err)
	} else if !found {
		if err = g.Account.Save(ctx, repo.AccountRecord{AccountID: a.accountID}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 11; i++ {
		bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
		r, _, err := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: 6062})
		if i == 10 {
			if !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
				t.Fatal("eleventh entry", err)
			}
			break
		}
		if err != nil {
			t.Fatal(i, "prepare", err)
		}
		if err = freezeCurrentDungeonTownReturnOrigin(a, r); err != nil {
			t.Fatal(err)
		}
		if err = s.commitDungeonRuntime(a, r); err != nil {
			t.Fatal(i, "commit", err)
		}
		if err = r.Session.Abandon(); err != nil {
			t.Fatal(err)
		}
	}
	account, _, _ := g.Account.Load(ctx, a.accountID)
	used, err := dnfdungeon.SilentForestEntryUsed(account, s.gameplayNow())
	ch, _, _ := g.Character.Load(ctx, "99")
	if used != 10 || err != nil || ch.Stats["fatigue"] != 0 {
		t.Fatal(used, err, ch.Stats)
	}
}
