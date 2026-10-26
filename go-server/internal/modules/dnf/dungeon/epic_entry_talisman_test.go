package dungeon

import (
	"context"
	"errors"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

func TestEpicTalismanActualPolicyPriorityRollbackAndExpiry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual PVF required")
	}
	archive, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := onlineevent.LoadEpicCatalog(nil, archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                         string
		id, tokens, tickets, wantTokens, wantTickets int64
		expired, rollback, refuse                    bool
	}{
		{"30_tokens_first", 6063, 3, 90, 2, 90, false, false, false},
		{"30_no_tickets", 6063, 1, 0, 0, 0, false, false, false},
		{"45_mixed", 6064, 2, 90, 1, 75, false, false, false},
		{"60_two_tokens", 6065, 2, 90, 0, 90, false, false, false},
		{"60_one_token", 6065, 1, 90, 0, 60, false, false, false},
		{"60_no_token", 6065, 0, 90, 0, 30, false, false, false},
		{"expired_fallback", 6063, 1, 90, 1, 60, true, false, false},
		{"mixed_insufficient", 6064, 1, 14, 1, 14, false, false, true},
		{"late_rollback", 6064, 1, 90, 1, 90, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, g, cmd := epicOwnerFixture(t)
			cmd.DungeonID = tc.id
			for _, r := range catalog.EntryAlternatives[tc.id] {
				var items []EntryItemRequirement
				for _, item := range r {
					items = append(items, EntryItemRequirement{ItemID: item.ItemID, Count: item.Count, ConsumeOnEntry: true})
				}
				cmd.Alternatives = append(cmd.Alternatives, items)
			}
			inv, _, _ := g.Inventory.Load(nil, "31")
			inv.Slots["0:1"] = repo.ItemStack{ItemID: 3330, Count: tc.tickets}
			token := repo.ItemStack{ItemID: 490010214, Count: tc.tokens}
			if tc.expired {
				token.ExpireAt = cmd.UpdatedAt
			}
			inv.Slots["0:3"] = token
			inv.Warehouse = map[string]repo.ItemStack{}
			if err := g.Inventory.Save(nil, inv); err != nil {
				t.Fatal(err)
			}
			day := cmd.UpdatedAt.In(onlineevent.ChinaCalendar).Add(-6 * time.Hour)
			start := time.Date(day.Year(), day.Month(), day.Day(), 6, 0, 0, 0, onlineevent.ChinaCalendar)
			campaign := onlineevent.EpicCampaign{Version: 1, CreatedAt: cmd.UpdatedAt, Starts: start, Ends: start.AddDate(0, 0, 28)}
			cmd.Campaign = &campaign
			fault := errors.New("after physical callback")
			if tc.rollback {
				g.CharacterAssets = epicRollbackAssets{g.CharacterAssets, fault}
			}
			owner, _ := NewOwner(g)
			result, err := owner.ConsumeEpicEntry(context.Background(), cmd)
			if tc.rollback {
				if !errors.Is(err, fault) {
					t.Fatal(err)
				}
			} else if tc.refuse {
				if !errors.Is(err, ErrEntryItemConsumeInsufficient) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Inventory.Load(nil, "31")
			ch, _, _ := g.Character.Load(nil, "31")
			if after.Slots["0:3"].Count != tc.wantTokens || after.Slots["0:1"].Count != tc.wantTickets {
				t.Fatalf("unexpected debit: %+v", after.Slots)
			}
			state, err := onlineevent.EpicCharacterSnapshot(ch, campaign, cmd.UpdatedAt)
			if err != nil || int64(state.Invitations) != tc.tickets-tc.wantTickets {
				t.Fatal(state, err)
			}
			if tc.rollback || tc.refuse {
				if ch.Stats[EpicEntrySequenceKey] != 0 || ch.Stats["fatigue"] != 100 {
					t.Fatal("partial entry", ch.Stats)
				}
				return
			}
			if ch.Stats[EpicEntrySequenceKey] != 1 || ch.Stats["fatigue"] != 99 || result.Replay {
				t.Fatal("entry receipt", ch.Stats)
			}
			if replay, err := owner.ConsumeEpicEntry(nil, cmd); err != nil || !replay.Replay {
				t.Fatal(replay, err)
			}
			again, _, _ := g.Inventory.Load(nil, "31")
			if again.Slots["0:3"].Count != tc.wantTokens || again.Slots["0:1"].Count != tc.wantTickets {
				t.Fatal("duplicate debit")
			}
		})
	}
}
