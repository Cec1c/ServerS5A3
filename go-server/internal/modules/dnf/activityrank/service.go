// Package activityrank提供账号榜查询、角色贡献和已结束周的幂等结算。
package activityrank

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"sync"
	"time"
)

const CacheLifetime = 15 * time.Second

type Query struct {
	Metric    string `json:"metric"`
	Period    string `json:"period"`
	Week      string `json:"week,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type Board struct {
	Metric        string                         `json:"metric"`
	Window        repository.ActivityRankWindow  `json:"window"`
	GeneratedAt   time.Time                      `json:"generated_at"`
	Settled       bool                           `json:"settled"`
	TotalAccounts int                            `json:"total_accounts"`
	Entries       []repository.ActivityRankEntry `json:"entries"`
	Self          *repository.ActivityRankEntry  `json:"self,omitempty"`
}
type cached struct {
	at          time.Time
	generatedAt time.Time
	settled     bool
	rows        []repository.ActivityRankEntry
}
type Service struct {
	mu     sync.Mutex
	store  repository.ActivityRankingRepository
	policy repository.ActivityRankPolicy
	cache  map[string]cached
}

func New(store repository.ActivityRankingRepository, policy repository.ActivityRankPolicy) (*Service, error) {
	if store == nil {
		return nil, repository.ErrActivityTransaction
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	policy.ExcludedAccounts = append([]string(nil), policy.ExcludedAccounts...)
	policy.Rewards = append([]repository.ActivityRankRewardTier(nil), policy.Rewards...)
	return &Service{store: store, policy: policy, cache: map[string]cached{}}, nil
}
func (s *Service) Board(ctx context.Context, q Query, now time.Time) (Board, error) {
	if s == nil || s.store == nil || !repository.ValidActivityRankMetric(q.Metric) || q.Offset < 0 || q.Offset > repository.ActivityRankMaxContributions || q.Limit < 0 || q.Limit > 100 || len(q.AccountID) > 128 || q.AccountID != "" && strings.TrimSpace(q.AccountID) == "" {
		return Board{}, repository.ErrActivityRank
	}
	w, err := repository.ActivityRankPeriod(q.Period, now)
	if err != nil {
		return Board{}, err
	}
	if q.Week != "" {
		if q.Period != "week" {
			return Board{}, repository.ErrActivityRank
		}
		old, e := repository.ActivityRankWeek(q.Week)
		if e != nil || old.FromDay > w.FromDay {
			return Board{}, repository.ErrActivityRank
		}
		w = old
	}
	if q.Limit == 0 {
		q.Limit = 50
	}
	out := Board{Metric: q.Metric, Window: w, GeneratedAt: now.UTC()}
	key := q.Metric + ":" + w.Period + ":" + w.FromDay
	// 缓存不可变，只在命中后复制当前分页；每个服务串行重建以合并查询。
	s.mu.Lock()
	if err = ctx.Err(); err != nil {
		s.mu.Unlock()
		return out, err
	}
	v, found := s.cache[key]
	if !found || now.Sub(v.at) < 0 || now.Sub(v.at) >= CacheLifetime {
		v = cached{at: now, generatedAt: now.UTC()}
		if w.Period == "week" && w.Closed(now) {
			snapshot, exists, e := s.store.RankWeek(ctx, q.Metric, w.FromDay)
			err = e
			if e == nil && exists {
				v.rows, v.settled, v.generatedAt = snapshot.Entries, true, snapshot.SettledAt
			}
		}
		if err == nil && !v.settled {
			var raw []repository.ActivityRankContribution
			raw, err = s.store.RankContributions(ctx, q.Metric, w)
			if err == nil {
				v.rows, err = repository.BuildActivityRank(raw, s.policy.ExcludedAccounts)
			}
		}
		if err == nil {
			if len(s.cache) >= 16 {
				s.cache = map[string]cached{}
			}
			s.cache[key] = v
		}
	}
	s.mu.Unlock()
	if err != nil {
		return out, err
	}
	rows := v.rows
	out.GeneratedAt, out.Settled = v.generatedAt, v.settled
	out.TotalAccounts = len(rows)
	for _, r := range rows {
		if r.AccountID == q.AccountID {
			copy := repository.CloneActivityRankEntries([]repository.ActivityRankEntry{r})[0]
			out.Self = &copy
			break
		}
	}
	start := min(q.Offset, len(rows))
	end := min(start+q.Limit, len(rows))
	out.Entries = repository.CloneActivityRankEntries(rows[start:end])
	return out, nil
}
func (s *Service) Settle(ctx context.Context, req repository.ActivityRankSettlement, now time.Time) (repository.ActivityRankSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return repository.ActivityRankSnapshot{}, false, repository.ErrActivityTransaction
	}
	req.Now = now
	v, created, err := s.store.SettleRankWeek(ctx, req)
	if err == nil {
		s.mu.Lock()
		delete(s.cache, req.Metric+":week:"+req.Week)
		s.mu.Unlock()
	}
	return v, created, err
}

// 调度器或管理入口可从明确启用周补结至上个完整周；每次最多8周，重复调用安全。
// 启用周及外观档位由运营配置提供，不自行生成物品或向历史周发放奖励。
func (s *Service) SettleDue(ctx context.Context, firstWeek string, now time.Time, policy repository.ActivityRankPolicy) ([]repository.ActivityRankSnapshot, error) {
	first, err := repository.ActivityRankWeek(firstWeek)
	if err != nil {
		return nil, err
	}
	if err = policy.Validate(); err != nil {
		return nil, err
	}
	d, _ := time.Parse("2006-01-02", first.FromDay)
	out := []repository.ActivityRankSnapshot{}
	for i := 0; i < 8; i++ {
		week := d.AddDate(0, 0, 7*i).Format("2006-01-02")
		w, _ := repository.ActivityRankWeek(week)
		if !w.Closed(now) {
			break
		}
		for _, metric := range []string{repository.ActivityFatigue, repository.ActivityEpic, repository.ActivityRecommendedDungeon, repository.ActivityColorless, repository.ActivityGoldSpent} {
			v, _, e := s.Settle(ctx, repository.ActivityRankSettlement{Metric: metric, Week: week, Policy: policy}, now)
			if e != nil {
				return out, e
			}
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *Service) Pending(ctx context.Context, account string, limit int) ([]repository.ActivityRankReward, error) {
	if s == nil || s.store == nil {
		return nil, repository.ErrActivityTransaction
	}
	if limit == 0 {
		limit = 50
	}
	return s.store.PendingRankRewards(ctx, account, limit)
}
