package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"sync"
)

type activityCounterKey struct{ account, character, bucket, metric string }
type memoryActivityStore struct {
	mu        sync.Mutex
	events    map[string]repository.ActivityEvent
	counters  map[activityCounterKey]int64
	rankWeeks map[string]repository.ActivityRankSnapshot
}

func newMemoryActivityStore() *memoryActivityStore {
	return &memoryActivityStore{events: map[string]repository.ActivityEvent{}, counters: map[activityCounterKey]int64{}}
}
func (s *memoryActivityStore) Record(context.Context, repository.ActivityEvent) (bool, error) {
	return false, repository.ErrActivityTransaction
}
func (s *memoryActivityStore) Summary(ctx context.Context, q repository.ActivityQuery) (repository.ActivitySummary, error) {
	out := repository.NewActivitySummary(q.AccountID)
	if err := q.Validate(); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, n := range s.counters {
		if k.account != q.AccountID || (q.CharacterID != "" && k.character != q.CharacterID) {
			continue
		}
		if q.FromDay == "" {
			if k.bucket != "all" {
				continue
			}
		} else if k.bucket < q.FromDay || k.bucket > q.ThroughDay {
			continue
		}
		if err := out.Add(k.character, k.metric, n); err != nil {
			return out, err
		}
	}
	if q.IncludeEpicSources {
		type key struct {
			character string
			dungeon   int64
		}
		counts := map[key]int64{}
		for _, e := range s.events {
			if e.AccountID != q.AccountID || e.Metric != repository.ActivityEpic || (q.CharacterID != "" && e.CharacterID != q.CharacterID) {
				continue
			}
			day := repository.ActivityDay(e.OccurredAt)
			if q.FromDay != "" && (day < q.FromDay || day > q.ThroughDay) {
				continue
			}
			k := key{e.CharacterID, e.DungeonID}
			if counts[k] > math.MaxInt64-e.Amount {
				return out, repository.ErrActivityInvalid
			}
			counts[k] += e.Amount
		}
		for k, n := range counts {
			out.EpicSources = append(out.EpicSources, repository.ActivityEpicSource{CharacterID: k.character, DungeonID: k.dungeon, Amount: n})
		}
	}
	out.Sort()
	return out, nil
}
func (s *memoryActivityStore) Events(ctx context.Context, q repository.ActivityQuery) ([]repository.ActivityEvent, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []repository.ActivityEvent{}
	for _, e := range s.events {
		if e.AccountID != q.AccountID || (q.CharacterID != "" && e.CharacterID != q.CharacterID) {
			continue
		}
		day := repository.ActivityDay(e.OccurredAt)
		if q.FromDay != "" && (day < q.FromDay || day > q.ThroughDay) {
			continue
		}
		if !q.BeforeTime.IsZero() && (e.OccurredAt.After(q.BeforeTime) || (e.OccurredAt.Equal(q.BeforeTime) && e.ID >= q.BeforeID)) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// 暂存账本直到原资产全部保存成功；失败、取消、panic均不发布事件。
type memoryActivityTransaction struct {
	*memoryActivityStore
	pending    map[string]repository.ActivityEvent
	increments map[activityCounterKey]int64
}

func (s *memoryActivityStore) transaction() *memoryActivityTransaction {
	return &memoryActivityTransaction{memoryActivityStore: s, pending: map[string]repository.ActivityEvent{}, increments: map[activityCounterKey]int64{}}
}
func (tx *memoryActivityTransaction) Record(ctx context.Context, e repository.ActivityEvent) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := e.Validate(); err != nil {
		return false, err
	}
	e.OccurredAt = e.OccurredAt.UTC().Truncate(1000)
	key := e.AccountID + "\x00" + e.ID
	tx.mu.Lock()
	defer tx.mu.Unlock()
	old, exists := tx.pending[key]
	if !exists {
		old, exists = tx.events[key]
	}
	if exists {
		if !repository.SameActivityEvent(old, e) {
			return false, repository.ErrActivityConflict
		}
		return false, nil
	}
	keys := []activityCounterKey{{e.AccountID, e.CharacterID, "all", e.Metric}, {e.AccountID, e.CharacterID, repository.ActivityDay(e.OccurredAt), e.Metric}}
	for _, k := range keys {
		if tx.counters[k] > math.MaxInt64-tx.increments[k]-e.Amount {
			return false, repository.ErrActivityInvalid
		}
	}
	for _, k := range keys {
		tx.increments[k] += e.Amount
	}
	tx.pending[key] = e
	return true, nil
}
func (tx *memoryActivityTransaction) commit() {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	for k, e := range tx.pending {
		tx.events[k] = e
	}
	for k, n := range tx.increments {
		tx.counters[k] += n
	}
}
