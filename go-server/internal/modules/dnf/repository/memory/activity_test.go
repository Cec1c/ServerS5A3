package memory

import (
	"context"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sync"
	"testing"
	"time"
)

func activityFixture(t *testing.T) repo.Group {
	t.Helper()
	g := NewMemoryGroup()
	ctx := context.Background()
	for _, id := range []string{"1", "2"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: id, Name: "角色" + id, Stats: map[string]int64{}}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}
func activityEvent(id, ch string, at time.Time) repo.ActivityEvent {
	return repo.ActivityEvent{ID: id, AccountID: "a", CharacterID: ch, Metric: repo.ActivityFatigue, Amount: 1, Source: "dungeon_room", DungeonID: 310, OccurredAt: at}
}
func recordActivity(g repo.Group, e repo.ActivityEvent) error {
	return g.CharacterSettlement.WithinCharacterSettlement(context.Background(), e.CharacterID, func(tx repo.Group) error { _, err := tx.Activity.Record(context.Background(), e); return err })
}

func TestActivityAccountPeriodsDetailsDeletionAndPagination(t *testing.T) {
	g := activityFixture(t)
	ctx := context.Background()
	before := time.Date(2026, 10, 23, 21, 59, 59, 0, time.UTC)
	for i, e := range []repo.ActivityEvent{activityEvent("a", "1", before), activityEvent("b", "2", before.Add(time.Second)), activityEvent("c", "1", before.Add(2*time.Second))} {
		if err := recordActivity(g, e); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if err := recordActivity(g, activityEvent("a", "1", before.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	q := repo.ActivityQuery{AccountID: "a"}
	summary, err := g.Activity.Summary(ctx, q)
	if err != nil || summary.Metrics[repo.ActivityFatigue] != 3 || len(summary.Characters) != 2 {
		t.Fatalf("账号汇总错误：%+v %v", summary, err)
	}
	q.FromDay, q.ThroughDay = "2026-10-23", "2026-10-23"
	daily, _ := g.Activity.Summary(ctx, q)
	if daily.Metrics[repo.ActivityFatigue] != 1 {
		t.Fatal(daily)
	}
	q.FromDay, q.ThroughDay = "2026-10-24", "2026-10-24"
	daily, _ = g.Activity.Summary(ctx, q)
	if daily.Metrics[repo.ActivityFatigue] != 2 {
		t.Fatal(daily)
	}
	ch, _, _ := g.Character.Load(ctx, "1")
	if ch.Stats == nil {
		ch.Stats = map[string]int64{}
	}
	ch.Stats["delete_flag"] = 1
	g.Character.Save(ctx, ch)
	q = repo.ActivityQuery{AccountID: "a", Limit: 1}
	first, _ := g.Activity.Events(ctx, q)
	if len(first) != 1 || first[0].ID != "c" || first[0].CharacterName != "角色1" {
		t.Fatal(first)
	}
	q.BeforeTime, q.BeforeID = first[0].OccurredAt, first[0].ID
	next, _ := g.Activity.Events(ctx, q)
	if len(next) != 1 || next[0].ID != "b" {
		t.Fatal(next)
	}
	q = repo.ActivityQuery{AccountID: "other"}
	other, _ := g.Activity.Summary(ctx, q)
	if len(other.Characters) != 0 {
		t.Fatal("跨账号泄露")
	}
	q = repo.ActivityQuery{AccountID: "a", CharacterID: "1"}
	own, _ := g.Activity.Summary(ctx, q)
	if own.Metrics[repo.ActivityFatigue] != 2 {
		t.Fatal("删除角色历史丢失")
	}
}

func TestActivityConcurrentReplayOwnershipConflictAndRollback(t *testing.T) {
	g := activityFixture(t)
	ctx := context.Background()
	at := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := activityEvent(fmt.Sprintf("event-%d", i%20), fmt.Sprint(i%2+1), at)
			errs <- recordActivity(g, e)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	summary, _ := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if summary.Metrics[repo.ActivityFatigue] != 20 {
		t.Fatal(summary)
	}
	wrong := activityEvent("wrong", "1", at)
	wrong.AccountID = "b"
	if !errors.Is(recordActivity(g, wrong), repo.ErrActivityTransaction) {
		t.Fatal("接受了错误所有者")
	}
	conflict := activityEvent("event-0", "1", at)
	conflict.DungeonID = 9
	if !errors.Is(recordActivity(g, conflict), repo.ErrActivityConflict) {
		t.Fatal("接受了同编号不同内容")
	}
	failed := errors.New("模拟保存失败")
	err := g.CharacterSettlement.WithinCharacterSettlement(ctx, "1", func(tx repo.Group) error {
		if _, err := tx.Activity.Record(ctx, activityEvent("rollback", "1", at)); err != nil {
			return err
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if _, err := g.Activity.Record(ctx, activityEvent("unsafe", "1", at)); !errors.Is(err, repo.ErrActivityTransaction) {
		t.Fatal("允许事务外写入")
	}
	summary, _ = g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if summary.Metrics[repo.ActivityFatigue] != 20 {
		t.Fatal("失败事件被保存")
	}
	u := g.CharacterSettlement.(*memoryCharacterSettlementUnitOfWork)
	original := u.character
	u.character = &activityFailCharacter{CharacterRepository: original, fail: true}
	err = u.WithinCharacterSettlement(ctx, "1", func(tx repo.Group) error {
		_, e := tx.Activity.Record(ctx, activityEvent("save-fail", "1", at))
		if e != nil {
			return e
		}
		ch, _, _ := tx.Character.Load(ctx, "1")
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		ch.Stats["test"] = 1
		return tx.Character.Save(ctx, ch)
	})
	if err == nil {
		t.Fatal("预期写后失败")
	}
	summary, _ = g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	ch, _, _ := original.Load(ctx, "1")
	if summary.Metrics[repo.ActivityFatigue] != 20 || ch.Stats["test"] != 0 {
		t.Fatal("资产与事件回滚不一致")
	}
}

type activityFailCharacter struct {
	repo.CharacterRepository
	fail bool
}

func (r *activityFailCharacter) Save(ctx context.Context, ch repo.CharacterRecord) error {
	err := r.CharacterRepository.Save(ctx, ch)
	if err == nil && r.fail {
		r.fail = false
		return errors.New("模拟写后失败")
	}
	return err
}
