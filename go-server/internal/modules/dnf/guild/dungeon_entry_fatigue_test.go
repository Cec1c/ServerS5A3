package guild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func dungeonEntryFatigueFixture(t *testing.T, n int) (r.Group, DungeonEntryFatigueQuery, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	repos, reg := memory.NewMemoryGroup(), NewRegistry()
	q := DungeonEntryFatigueQuery{Cost: 8, Catalog: dungeonLifecycleCatalog(t), Accounts: map[string]r.AccountRecord{}, Revalidate: func() bool { return true }}
	owners := []r.GuildOwner{}
	var g *Guild
	for i := 1; i <= n; i++ {
		id := fmt.Sprint(i)
		a := r.AccountRecord{AccountID: "fp-account-" + id}
		c := r.CharacterRecord{CharacterID: id, AccountID: a.AccountID, Name: "FP" + id, Level: 90, Stats: map[string]int64{"server_group_id": 1, "fatigue": 8, "fatigue_limit": 156, "fatigue_used": 148, "fatigue_display_update": 148, fatigue.DayKey: premium.ServiceDayID(now)}}
		if err := repos.Account.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		var err error
		if g == nil {
			g, err = reg.Create(c, "FP Guild", now)
		} else {
			_, err = reg.JoinDirect(g.ID, c, now)
		}
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, r.GuildOwner{AccountID: a.AccountID, CharacterID: id})
		q.Accounts[a.AccountID] = a
	}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	var err error
	q.Entries, err = PrepareDungeonEntries(reg, q.Catalog, 8500, owners, "fp-run-1", now)
	if err != nil {
		t.Fatal(err)
	}
	return repos, q, now
}

func assertDungeonEntryFatigue(t *testing.T, repos r.Group, q DungeonEntryFatigueQuery, want int64, receipt bool) {
	t.Helper()
	for _, entry := range q.Entries {
		c, found, err := repos.Character.Load(context.Background(), entry.Owner.CharacterID)
		if err != nil || !found || c.Stats["fatigue"] != want {
			t.Fatalf("owner=%s fatigue=%d found=%v err=%v", entry.Owner.CharacterID, c.Stats["fatigue"], found, err)
		}
	}
	e := q.Entries[0]
	_, found, err := repos.Settings.Load(context.Background(), r.GuildDungeonEntryReceiptScope(e.GuildID, e.DungeonID, e.RunID))
	if err != nil || found != receipt {
		t.Fatalf("receipt=%v want=%v err=%v", found, receipt, err)
	}
}

func TestDungeonEntryFatigueZeroReplayAndConcurrentCommit(t *testing.T) {
	for n := 1; n <= 4; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			repos, q, now := dungeonEntryFatigueFixture(t, n)
			first, err := CommitDungeonEntryFatigue(context.Background(), repos.Guild, q, now)
			if err != nil || first.Replayed || len(first.Characters) != n {
				t.Fatal(first, err)
			}
			assertDungeonEntryFatigue(t, repos, q, 0, true)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := CommitDungeonEntryFatigue(context.Background(), repos.Guild, q, now)
					if err != nil || !result.Replayed {
						t.Errorf("replay=%v err=%v", result.Replayed, err)
					}
				}()
			}
			wg.Wait()
			assertDungeonEntryFatigue(t, repos, q, 0, true)
		})
	}
}

type failedDungeonEntryUnit struct {
	r.GuildUnitOfWork
	failID      string
	failReceipt bool
}

func (u failedDungeonEntryUnit) WithinGuildDungeonEntry(ctx context.Context, owners []r.GuildOwner, scope string, apply func(r.Group) error) error {
	return u.GuildUnitOfWork.(r.GuildDungeonEntryUnitOfWork).WithinGuildDungeonEntry(ctx, owners, scope, func(tx r.Group) error {
		tx.Character = failedDungeonEntryCharacter{CharacterRepository: tx.Character, failID: u.failID}
		if u.failReceipt {
			tx.Settings = failedDungeonEntrySettings{tx.Settings, scope}
		}
		return apply(tx)
	})
}

type failedDungeonEntryCharacter struct {
	r.CharacterRepository
	failID string
}

func (c failedDungeonEntryCharacter) Save(ctx context.Context, record r.CharacterRecord) error {
	if record.CharacterID == c.failID {
		return errors.New("injected entry character save failure")
	}
	return c.CharacterRepository.Save(ctx, record)
}

type failedDungeonEntrySettings struct {
	r.SettingsRepository
	scope string
}

func (s failedDungeonEntrySettings) Save(ctx context.Context, record r.SettingsRecord) error {
	if record.Scope == s.scope {
		return errors.New("injected entry receipt save failure")
	}
	return s.SettingsRepository.Save(ctx, record)
}

func TestDungeonEntryFatigueRollbackAndRevalidation(t *testing.T) {
	for _, failure := range []string{"2", "4", "receipt", "last_validate"} {
		t.Run(failure, func(t *testing.T) {
			repos, q, now := dungeonEntryFatigueFixture(t, 4)
			unit := failedDungeonEntryUnit{GuildUnitOfWork: repos.Guild, failID: failure, failReceipt: failure == "receipt"}
			calls := 0
			if failure == "last_validate" {
				q.Revalidate = func() bool { calls++; return calls < 3 }
			}
			if _, err := CommitDungeonEntryFatigue(context.Background(), unit, q, now); err == nil {
				t.Fatal("failure committed")
			}
			assertDungeonEntryFatigue(t, repos, q, 8, false)
		})
	}
}

func TestDungeonEntryFatigueRechecksCycleCatalogMembershipAndReceipt(t *testing.T) {
	for _, failure := range []string{"cycle", "catalog", "joined", "account", "receipt", "balance"} {
		t.Run(failure, func(t *testing.T) {
			repos, q, now := dungeonEntryFatigueFixture(t, 4)
			switch failure {
			case "cycle":
				now = now.AddDate(0, 1, 0)
			case "catalog":
				q.Entries[2].CatalogDigest = "changed"
			case "joined":
				q.Entries[2].JoinedAt = q.Entries[2].JoinedAt.Add(time.Second)
			case "account":
				delete(q.Accounts, q.Entries[2].Owner.AccountID)
			case "receipt":
				e := q.Entries[0]
				if err := repos.Settings.Save(context.Background(), r.SettingsRecord{Scope: r.GuildDungeonEntryReceiptScope(e.GuildID, e.DungeonID, e.RunID), Values: map[string]string{"receipt": "tampered"}}); err != nil {
					t.Fatal(err)
				}
			case "balance":
				c, _, _ := repos.Character.Load(context.Background(), "4")
				c.Stats["fatigue"] = 7
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string]r.CharacterRecord{}
			for _, e := range q.Entries {
				c, _, _ := repos.Character.Load(context.Background(), e.Owner.CharacterID)
				before[e.Owner.CharacterID] = c
			}
			if _, err := CommitDungeonEntryFatigue(context.Background(), repos.Guild, q, now); err == nil {
				t.Fatal("invalid entry committed")
			}
			for id, want := range before {
				got, _, _ := repos.Character.Load(context.Background(), id)
				if !reflect.DeepEqual(got, want) {
					t.Fatal("rejected entry changed", id)
				}
			}
		})
	}
}

func TestDungeonEntryFatigueRefreshesServiceDayBeforeDebit(t *testing.T) {
	repos, q, now := dungeonEntryFatigueFixture(t, 1)
	c, _, _ := repos.Character.Load(context.Background(), "1")
	c.Stats["fatigue"] = 0
	c.Stats[fatigue.DayKey]--
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitDungeonEntryFatigue(context.Background(), repos.Guild, q, now); err != nil {
		t.Fatal(err)
	}
	assertDungeonEntryFatigue(t, repos, q, 148, true)
}

func TestDungeonEntryFatigueScopedRepository(t *testing.T) {
	repos, q, _ := dungeonEntryFatigueFixture(t, 4)
	owners := []r.GuildOwner{}
	for _, e := range q.Entries {
		owners = append(owners, e.Owner)
	}
	uow := repos.Guild.(r.GuildDungeonEntryUnitOfWork)
	for _, scope := range []string{"", "global:unrelated", "guild-dungeon-entry-fatigue:v1:", "guild-dungeon-entry-fatigue:v1:not-a-digest"} {
		called := false
		if err := uow.WithinGuildDungeonEntry(context.Background(), owners, scope, func(r.Group) error { called = true; return nil }); err == nil || called {
			t.Fatal("invalid receipt scope", scope, err)
		}
	}
	if err := repos.Guild.WithinGuild(context.Background(), owners, func(r.Group) error { return nil }); err == nil {
		t.Fatal("ordinary guild widened beyond two owners")
	}
	e := q.Entries[0]
	scope := r.GuildDungeonEntryReceiptScope(e.GuildID, e.DungeonID, e.RunID)
	if err := uow.WithinGuildDungeonEntry(context.Background(), owners, scope, func(tx r.Group) error {
		if _, _, err := tx.Settings.Load(context.Background(), "global:unrelated"); err == nil {
			t.Fatal("unrelated settings readable")
		}
		if err := tx.Settings.Save(context.Background(), r.SettingsRecord{Scope: r.GuildDungeonEntryReceiptScope(e.GuildID, e.DungeonID, "another-run")}); err == nil {
			t.Fatal("other run writable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
