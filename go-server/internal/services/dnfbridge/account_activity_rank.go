package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/activityrank"
	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/repository"
	"time"
)

func (s *Service) currentActivityRanks() (*activityrank.Service, error) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, repository.ErrActivityTransaction
	}
	store, ok := repos.Activity.(repository.ActivityRankingRepository)
	if !ok {
		return nil, repository.ErrActivityTransaction
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activityRankService == nil {
		var err error
		s.activityRankService, err = activityrank.New(activityrank.ReuseAdventureTotals(store, repos.Character), repository.ActivityRankPolicy{Version: "account-ranking-v1"})
		if err != nil {
			return nil, err
		}
	}
	return s.activityRankService, nil
}

// 暂经原鉴权管理接口联调；图标/原生窗口后续只接此领域逻辑，不复用公会协议。
func (s *Service) handleActivityRankAdmin(parent context.Context, req adminapi.Request, out *adminapi.Response) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	service, err := s.currentActivityRanks()
	if err != nil {
		out.OK = false
		out.Error = "账号排行榜仓储不可用"
		return
	}
	now := time.Now().UTC()
	switch req.Action {
	case "activity_rank":
		if req.RankQuery == nil {
			err = repository.ErrActivityRank
			break
		}
		var v activityrank.Board
		v, err = service.Board(ctx, *req.RankQuery, now)
		if err == nil {
			out.RankBoard = &v
		}
	case "activity_rank_settle":
		if req.RankSettlement == nil {
			err = repository.ErrActivityRank
			break
		}
		var v repository.ActivityRankSnapshot
		v, out.RankCreated, err = service.Settle(ctx, *req.RankSettlement, now)
		if err == nil {
			out.RankSnapshot = &v
		}
	case "activity_rank_rewards":
		if req.RankQuery == nil {
			err = repository.ErrActivityRank
			break
		}
		out.RankRewards, err = service.Pending(ctx, req.RankQuery.AccountID, req.RankQuery.Limit)
	}
	if err != nil {
		out.OK = false
		out.Error = "排行榜请求失败：" + err.Error()
	}
}
