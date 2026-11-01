package pvp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/activeness"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func privateActivityPVPFatigue030Catalog(t *testing.T) *activeness.Catalog {
	t.Helper()
	doc, err := pvf.Parse(activeness.PVFPath, `[private activity tasks]
1 1 10 2 1 10 3 1 50 4 1 100 5 1 10 6 100 20 7 1 10 8 1 10
[/private activity tasks]
[private activity rewards]
1 30 500 990027401 1 0 0 2 50 500 990027402 1 3330 500 3 100 5500 990027403 1 3330 500
[/private activity rewards]`)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := activeness.ParseCatalog(doc)
	if err != nil || catalog == nil {
		t.Fatal("activity catalog", err)
	}
	return catalog
}

func privateActivityPVPFatigue030Load(t *testing.T, g repo.Group, id string) repo.CharacterRecord {
	t.Helper()
	c, found, err := g.Character.Load(context.Background(), id)
	if err != nil || !found {
		t.Fatal("load character", id, found, err)
	}
	return c
}

func TestPrivateActivityPVPFatigue030TransactionalCallback(t *testing.T) {
	for _, tc := range []struct {
		name        string
		amount      int64
		balance     int64
		failHook    bool
		failSave    bool
		staleDay    bool
		withoutHook bool
	}{
		{name: "one_point_threshold", amount: 1, balance: 156},
		{name: "positive_saturates", amount: 30, balance: 156},
		{name: "zero_skips_hook", amount: 0, balance: 0, failHook: true},
		{name: "insufficient_skips_hook", amount: 30, balance: 29},
		{name: "callback_failure_rolls_back", amount: 30, balance: 156, failHook: true},
		{name: "save_failure_rolls_back", amount: 30, balance: 156, failSave: true},
		{name: "refresh_counts_only_new_debit", amount: 1, balance: 3, staleDay: true},
		{name: "optional_callback", amount: 1, balance: 156, withoutHook: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
			g := memory.NewMemoryGroup()
			catalog := privateActivityPVPFatigue030Catalog(t)
			if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"1", "2"} {
				c := repo.CharacterRecord{AccountID: "a", CharacterID: id, UpdatedAt: now, Stats: map[string]int64{
					"fatigue": tc.balance, "fatigue_limit": 156, "fatigue_used": 156 - tc.balance,
					"fatigue_display_update": 156 - tc.balance, fatigue.DayKey: premium.ServiceDayID(now),
				}}
				if tc.staleDay {
					c.Stats[fatigue.DayKey]--
				}
				if _, err := activeness.Apply(&c, catalog, 6, 99, now, now); err != nil {
					t.Fatal(err)
				}
				if err := g.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			before := privateActivityPVPFatigue030Load(t, g, "1")
			other := privateActivityPVPFatigue030Load(t, g, "2")
			injected := errors.New("injected activity debit failure")
			calls := 0
			q := ConsumeArcadeCommand{AccountID: "a", CharacterID: "1", Amount: tc.amount, RequireFull: true, UpdatedAt: now}
			if !tc.withoutHook {
				q.OnFatigueDebit = func(c *repo.CharacterRecord, consumed int64, at time.Time) error {
					calls++
					if c.CharacterID != "1" || !at.Equal(now) || consumed != tc.amount || c.Stats["fatigue"] != 156-consumed {
						t.Fatalf("wrong transaction callback: char=%s amount=%d at=%s fatigue=%d", c.CharacterID, consumed, at, c.Stats["fatigue"])
					}
					if _, err := activeness.Apply(c, catalog, 6, uint32(consumed), at, at); err != nil {
						return err
					}
					if tc.failHook {
						return injected
					}
					return nil
				}
			}
			if tc.failSave {
				g.RentalAssets = privateActivityPVPFatigue030FailSave{g.RentalAssets, injected}
			}
			out, err := ConsumeArcadeFatigue(ctx, g, q)
			got := privateActivityPVPFatigue030Load(t, g, "1")
			wantCalls := 1
			if tc.amount == 0 || tc.balance < tc.amount && !tc.staleDay || tc.withoutHook {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("callback calls=%d want=%d", calls, wantCalls)
			}
			if tc.balance < tc.amount && !tc.staleDay {
				if !errors.Is(err, ErrFatigueInsufficient) {
					t.Fatal("expected insufficient fatigue", err)
				}
			} else if tc.failSave || tc.failHook && tc.amount > 0 {
				if !errors.Is(err, injected) {
					t.Fatal("expected transaction failure", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err != nil || tc.amount == 0 {
				if !reflect.DeepEqual(before, got) {
					t.Fatal("failed/zero debit changed character or activity")
				}
			} else {
				progress, points := int64(100), int64(20)
				if tc.withoutHook {
					progress, points = 99, 0
				}
				if out.Before != 156 || out.After != 156-tc.amount || out.Consumed != tc.amount || got.Stats["fatigue"] != out.After || got.Stats["private_activity_v1_progress_6"] != progress || got.Stats["private_activity_v1_points"] != points {
					t.Fatalf("wrong committed debit: before=%d after=%d consumed=%d progress=%d points=%d", out.Before, out.After, out.Consumed, got.Stats["private_activity_v1_progress_6"], got.Stats["private_activity_v1_points"])
				}
			}
			if !reflect.DeepEqual(other, privateActivityPVPFatigue030Load(t, g, "2")) {
				t.Fatal("same-account second character changed")
			}
		})
	}
}

type privateActivityPVPFatigue030FailSave struct {
	repo.RentalAssetUnitOfWork
	err error
}

func (u privateActivityPVPFatigue030FailSave) WithinRentalAssets(ctx context.Context, account, character string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.RentalAssetUnitOfWork.WithinRentalAssets(ctx, account, character, func(a repo.AccountRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return apply(a, privateActivityPVPFatigue030FailCharacters{c, u.err}, i, e)
	})
}

type privateActivityPVPFatigue030FailCharacters struct {
	repo.CharacterRepository
	err error
}

func (c privateActivityPVPFatigue030FailCharacters) Save(context.Context, repo.CharacterRecord) error {
	return c.err
}
