package guild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/activeness"
	"longheng.io/server/internal/modules/dnf/fatigue"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func privateActivityFatigue030Catalog(t *testing.T) *activeness.Catalog {
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
	c, err := activeness.ParseCatalog(doc)
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	return c
}

func privateActivityFatigue030Snapshot(t *testing.T, repos r.Group, q DungeonEntryFatigueQuery) map[string]r.CharacterRecord {
	t.Helper()
	rows := map[string]r.CharacterRecord{}
	for _, entry := range q.Entries {
		id := entry.Owner.CharacterID
		c, found, err := repos.Character.Load(context.Background(), id)
		if err != nil || !found {
			t.Fatal(id, found, err)
		}
		rows[id] = c
	}
	return rows
}

func TestPrivateActivityFatigue030PartyActualDebitAndReplay(t *testing.T) {
	ctx := context.Background()
	repos, q, now := dungeonEntryFatigueFixture(t, 4)
	catalog := privateActivityFatigue030Catalog(t)
	for i, progress := range []uint32{0, 92, 99, 100} {
		id := fmt.Sprint(i + 1)
		c, _, _ := repos.Character.Load(ctx, id)
		if progress > 0 {
			if _, err := activeness.Apply(&c, catalog, 6, progress, now, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	calls := map[string]int{}
	q.OnFatigueDebit = func(c *r.CharacterRecord, consumed int64, at time.Time) error {
		if consumed != 8 || !at.Equal(now) || c.Stats["fatigue"] != 0 || c.Stats["fatigue_used"] != 156 || q.Accounts[c.AccountID].AccountID != c.AccountID {
			return fmt.Errorf("wrong debit callback: id=%s amount=%d at=%s stats=%v", c.CharacterID, consumed, at, c.Stats)
		}
		calls[c.CharacterID]++
		_, err := activeness.Apply(c, catalog, 6, uint32(consumed), at, at)
		return err
	}
	result, err := CommitDungeonEntryFatigue(ctx, repos.Guild, q, now)
	if err != nil || result.Replayed {
		t.Fatal(result, err)
	}
	assertDungeonEntryFatigue(t, repos, q, 0, true)
	for i, progress := range []int64{8, 100, 100, 100} {
		id := fmt.Sprint(i + 1)
		c, _, _ := repos.Character.Load(ctx, id)
		points := int64(0)
		if progress == 100 {
			points = 20
		}
		if calls[id] != 1 || c.Stats["private_activity_v1_progress_6"] != progress || c.Stats["private_activity_v1_points"] != points || !reflect.DeepEqual(c, result.Characters[id]) {
			t.Fatal("personal activity or returned snapshot mismatch", id, calls, c.Stats)
		}
	}
	before := privateActivityFatigue030Snapshot(t, repos, q)
	q.OnFatigueDebit = func(*r.CharacterRecord, int64, time.Time) error {
		t.Error("replay invoked debit callback")
		return errors.New("replay must not invoke callback")
	}
	replayed, err := CommitDungeonEntryFatigue(ctx, repos.Guild, q, now)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(before, privateActivityFatigue030Snapshot(t, repos, q)) {
		t.Fatal("zero-balance receipt replay changed activity", replayed, err)
	}
}

func TestPrivateActivityFatigue030ZeroAndRefreshActualAmount(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("refresh_%t", refresh), func(t *testing.T) {
			repos, q, now := dungeonEntryFatigueFixture(t, 1)
			c, _, _ := repos.Character.Load(context.Background(), "1")
			if refresh {
				c.Stats["fatigue"] = 0
				c.Stats[fatigue.DayKey]--
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			} else {
				q.Cost = 0
			}
			calls := 0
			q.OnFatigueDebit = func(c *r.CharacterRecord, amount int64, at time.Time) error {
				calls++
				if !refresh || amount != 8 || c.Stats["fatigue"] != 148 || !at.Equal(now) {
					return errors.New("callback counted a refill or zero cost")
				}
				return nil
			}
			if _, err := CommitDungeonEntryFatigue(context.Background(), repos.Guild, q, now); err != nil {
				t.Fatal(err)
			}
			if refresh && calls != 1 || !refresh && calls != 0 {
				t.Fatal("wrong callback count", calls)
			}
		})
	}
}

func TestPrivateActivityFatigue030FailureRollsBackRosterAndReceipt(t *testing.T) {
	for _, failure := range []string{"callback_2", "callback_4", "save_2", "save_4", "receipt", "last_validate", "insufficient"} {
		t.Run(failure, func(t *testing.T) {
			repos, q, now := dungeonEntryFatigueFixture(t, 4)
			catalog := privateActivityFatigue030Catalog(t)
			for _, entry := range q.Entries {
				c, _, _ := repos.Character.Load(context.Background(), entry.Owner.CharacterID)
				if _, err := activeness.Apply(&c, catalog, 6, 99, now, now); err != nil {
					t.Fatal(err)
				}
				if failure == "insufficient" && c.CharacterID == "4" {
					c.Stats["fatigue"] = 7
				}
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			}
			before := privateActivityFatigue030Snapshot(t, repos, q)
			callbackError := errors.New("injected activity callback failure")
			q.OnFatigueDebit = func(c *r.CharacterRecord, amount int64, at time.Time) error {
				if _, err := activeness.Apply(c, catalog, 6, uint32(amount), at, at); err != nil {
					return err
				}
				if failure == "callback_"+c.CharacterID {
					return callbackError
				}
				return nil
			}
			unit := r.GuildUnitOfWork(repos.Guild)
			if failure == "save_2" || failure == "save_4" || failure == "receipt" {
				id := ""
				if failure != "receipt" {
					id = failure[len("save_"):]
				}
				unit = failedDungeonEntryUnit{GuildUnitOfWork: repos.Guild, failID: id, failReceipt: failure == "receipt"}
			}
			if failure == "last_validate" {
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
			}
			if _, err := CommitDungeonEntryFatigue(context.Background(), unit, q, now); err == nil {
				t.Fatal("failed entry committed")
			} else if (failure == "callback_2" || failure == "callback_4") && !errors.Is(err, callbackError) {
				t.Fatal("callback error was lost", err)
			}
			if !reflect.DeepEqual(before, privateActivityFatigue030Snapshot(t, repos, q)) {
				t.Fatal("failed entry committed activity or fatigue")
			}
			e := q.Entries[0]
			if _, found, err := repos.Settings.Load(context.Background(), r.GuildDungeonEntryReceiptScope(e.GuildID, e.DungeonID, e.RunID)); err != nil || found {
				t.Fatal("failed entry wrote receipt", found, err)
			}
		})
	}
}

func TestPrivateActivityFatigue030ConcurrentReceiptCallsOncePerMember(t *testing.T) {
	repos, q, now := dungeonEntryFatigueFixture(t, 4)
	var calls atomic.Int32
	q.OnFatigueDebit = func(c *r.CharacterRecord, amount int64, at time.Time) error {
		calls.Add(1)
		c.Stats["private_activity_v1_progress_6"] += amount
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := CommitDungeonEntryFatigue(context.Background(), repos.Guild, q, now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 4 {
		t.Fatal("concurrent replay invoked callback again", calls.Load())
	}
	for id, c := range privateActivityFatigue030Snapshot(t, repos, q) {
		if c.Stats["fatigue"] != 0 || c.Stats["private_activity_v1_progress_6"] != 8 {
			t.Fatal("concurrent activity changed", id, c.Stats)
		}
	}
}
