package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"strings"
)

func (s *memoryActivityStore) rankRowsLocked(metric string, w repository.ActivityRankWindow) ([]repository.ActivityRankContribution, error) {
	if !repository.ValidActivityRankMetric(metric) || w.Validate() != nil {
		return nil, repository.ErrActivityRank
	}
	counts := map[[2]string]int64{}
	for k, n := range s.counters {
		if !repository.ActivityRankUsesMetric(metric, k.metric) {
			continue
		}
		if w.Period == "all" {
			if k.bucket != "all" {
				continue
			}
		} else if k.bucket < w.FromDay || k.bucket > w.ThroughDay {
			continue
		}
		key := [2]string{k.account, k.character}
		if n < 0 || counts[key] > math.MaxInt64-n {
			return nil, repository.ErrActivityRank
		}
		counts[key] += n
		if len(counts) > repository.ActivityRankMaxContributions {
			return nil, repository.ErrActivityRank
		}
	}
	rows := make([]repository.ActivityRankContribution, 0, len(counts))
	for k, n := range counts {
		if n > 0 {
			rows = append(rows, repository.ActivityRankContribution{AccountID: k[0], CharacterID: k[1], Score: n})
		}
	}
	return rows, nil
}
func (s *memoryActivityStore) RankContributions(ctx context.Context, metric string, w repository.ActivityRankWindow) ([]repository.ActivityRankContribution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rankRowsLocked(metric, w)
}
func (s *memoryActivityStore) RankWeek(ctx context.Context, metric, week string) (repository.ActivityRankSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	if _, err := repository.ActivityRankWeek(week); err != nil || !repository.ValidActivityRankMetric(metric) {
		return repository.ActivityRankSnapshot{}, false, repository.ErrActivityRank
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rankWeeks[metric+":"+week]
	return repository.CloneActivityRankSnapshot(v), ok, nil
}
func (s *memoryActivityStore) SettleRankWeek(ctx context.Context, req repository.ActivityRankSettlement) (repository.ActivityRankSnapshot, bool, error) {
	if err := req.Validate(); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.rankWeeks[req.Key()]; ok {
		digest, _ := req.Policy.Digest()
		if digest != old.PolicyDigest {
			return repository.ActivityRankSnapshot{}, false, repository.ErrActivityRankConflict
		}
		return repository.CloneActivityRankSnapshot(old), false, nil
	}
	w, _ := repository.ActivityRankWeek(req.Week)
	rows, err := s.rankRowsLocked(req.Metric, w)
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	snapshot, err := repository.NewActivityRankSnapshot(req, rows)
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	if s.rankWeeks == nil {
		s.rankWeeks = map[string]repository.ActivityRankSnapshot{}
	}
	s.rankWeeks[req.Key()] = repository.CloneActivityRankSnapshot(snapshot)
	return snapshot, true, nil
}
func (s *memoryActivityStore) PendingRankRewards(ctx context.Context, account string, limit int) ([]repository.ActivityRankReward, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(account) == "" || len(account) > 128 || limit < 1 || limit > 100 {
		return nil, repository.ErrActivityRank
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []repository.ActivityRankReward{}
	for _, w := range s.rankWeeks {
		for _, r := range w.Rewards {
			if r.AccountID == account && r.State == "pending" {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BoardKey < out[j].BoardKey })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
