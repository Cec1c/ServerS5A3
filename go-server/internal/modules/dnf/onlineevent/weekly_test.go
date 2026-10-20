package onlineevent

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"sync"
	"testing"
	"time"
)

func weeklyTestRepos(t *testing.T) repo.Group {
	t.Helper()
	g := memory.NewMemoryGroup()
	for _, a := range []string{"a", "b"} {
		if err := g.Account.Save(nil, repo.AccountRecord{AccountID: a, Metadata: map[string]string{"keep": "yes"}}); err != nil {
			t.Fatal(err)
		}
	}
	for id, a := range map[string]string{"1": "a", "2": "a", "3": "b"} {
		if err := g.Character.Save(nil, repo.CharacterRecord{CharacterID: id, AccountID: a}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}
func TestWeeklySeasonFirstEnablePersistsAndNeverUsesControlsTimestamp(t *testing.T) {
	g := weeklyTestRepos(t)
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, ChinaCalendar)
	first, err := ActivateWeeklySeason(nil, g, "a", "1", now, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 8, 6, 0, 0, 0, ChinaCalendar)
	if !first.Starts.Equal(want) || !first.Ends.Equal(want.AddDate(0, 0, 70)) || !first.CreatedAt.Equal(now) {
		t.Fatalf("first=%+v", first)
	}
	if err := g.Settings.Save(nil, repo.SettingsRecord{Scope: ControlsScope, Values: map[string]string{"other": "changed"}, UpdatedAt: now.AddDate(0, 1, 0)}); err != nil {
		t.Fatal(err)
	}
	for _, after := range []time.Time{now.Add(time.Minute), now.AddDate(0, 1, 0), now.AddDate(0, 3, 0), now.AddDate(0, 0, -1)} {
		again, err := ActivateWeeklySeason(nil, g, "a", "2", after, func() bool { return true })
		if err != nil || again.Key() != first.Key() || !again.CreatedAt.Equal(now) {
			t.Fatalf("changed season=%+v err=%v", again, err)
		}
	}
	if first.Week(now.AddDate(0, 0, -1)) != -1 || first.Week(first.Ends) != -1 || first.Week(first.Ends.Add(-time.Nanosecond)) != 9 {
		t.Fatal("clock/end boundary invalid")
	}
	a, _, _ := g.Account.Load(nil, "a")
	if len(a.Metadata) != 1 || a.Metadata["keep"] != "yes" {
		t.Fatal("activation wrote account")
	}
}
func TestWeeklySeasonConcurrentSameOwnerAndForeignOwnerReject(t *testing.T) {
	g := weeklyTestRepos(t)
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, ChinaCalendar)
	var wg sync.WaitGroup
	results := make(chan WeeklySeason, 64)
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "1"
			if i%2 == 1 {
				id = "2"
			}
			s, e := ActivateWeeklySeason(context.Background(), g, "a", id, now.Add(time.Duration(i)*time.Second), func() bool { return true })
			results <- s
			errs <- e
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	stored, found, err := LoadWeeklySeason(nil, g.Settings)
	if err != nil || !found {
		t.Fatal(err)
	}
	for s := range results {
		if s.Key() != stored.Key() || !s.CreatedAt.Equal(stored.CreatedAt) {
			t.Fatal("concurrent activation split season")
		}
	}
	for _, bad := range []struct{ a, c string }{{"a", "3"}, {"b", "1"}, {"a", "99"}} {
		if _, e := ActivateWeeklySeason(nil, g, bad.a, bad.c, now, func() bool { return true }); e == nil {
			t.Fatal("foreign activation accepted")
		}
	}
	g2 := weeklyTestRepos(t)
	if _, e := ActivateWeeklySeason(nil, g2, "a", "1", now, func() bool { return false }); e == nil {
		t.Fatal("disabled owner initialized period")
	}
	if _, found, _ := LoadWeeklySeason(nil, g2.Settings); found {
		t.Fatal("rejected activation wrote period")
	}
}
func TestWeeklyActual369dCatalogHasTenWeeksAndThreeSixNine(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual369d required")
	}
	a, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Checksum != "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06" {
		t.Fatal("wrong real source")
	}
	c, err := LoadWeeklyCatalog(nil, a)
	if err != nil {
		t.Fatal(err)
	}
	if c.RequiredClears != 4 || c.CatchupClears != 20 || c.CatchupLimit != 2 || c.Thresholds != [3]uint32{3, 6, 9} {
		t.Fatalf("catalog=%+v", c)
	}
	for i, r := range c.Rewards {
		if r.ItemID != 490009970+int64(i) || r.Count != 1 {
			t.Fatalf("week%d=%+v", i, r)
		}
	}
	for i, r := range c.Bonus {
		if r.ItemID != 490009980+int64(i) || r.Count != 1 {
			t.Fatalf("bonus%d=%+v", i, r)
		}
	}
}

func TestWeeklySeasonPostSaveOwnerChangePanicAndCancelRollback(t *testing.T) {
	for _, mode := range []string{"invalid", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			g := weeklyTestRepos(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			_, err := ActivateWeeklySeason(ctx, g, "a", "1", time.Date(2026, 10, 31, 6, 0, 0, 0, ChinaCalendar), func() bool {
				calls++
				if calls == 4 {
					switch mode {
					case "invalid":
						return false
					case "panic":
						panic("after staged settings")
					case "cancel":
						cancel()
					}
				}
				return true
			})
			if err == nil {
				t.Fatal("failed activation accepted")
			}
			if _, found, _ := g.Settings.Load(nil, WeeklySeasonScope); found {
				t.Fatal("failed activation persisted season")
			}
			a, _, _ := g.Account.Load(nil, "a")
			if a.Metadata["keep"] != "yes" || len(a.Metadata) != 1 {
				t.Fatal("activation mutated account")
			}
		})
	}
}
