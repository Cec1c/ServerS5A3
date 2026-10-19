package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"sort"
	"time"
)

func prepareCurrentFiendWarResult(state runtimeRaidState, option byte) (r currentAntonResultProjection, err error) {
	b := state.Battle
	if !state.Started || b == nil || b.Mode != 3 || b.FiendWar == nil || b.Phase != 0 || option > 1 || b.PhaseCompleted == b.TimedOut || b.AntonRewards == nil || len(b.AntonRewards.Phases) != 1 || b.EnterAfter.IsZero() || !b.Deadline.After(b.EnterAfter) {
		return r, fmt.Errorf("超时空结果实例无效")
	}
	at := b.PhaseCompletedAt
	if b.TimedOut {
		at = b.Deadline
		if !b.FiendWarEndedAt.IsZero() {
			at = b.FiendWarEndedAt
		}
	} else if !b.FiendWar.Complete {
		return r, fmt.Errorf("超时空尚未完成PVF通关条件")
	}
	if at.Before(b.EnterAfter) || at.After(b.Deadline) || !b.TimedOut && !at.Before(b.Deadline) {
		return r, fmt.Errorf("超时空结果时间无效")
	}
	var deaths uint64
	for k, life := range b.MemberLives {
		if k.RunID == "" || b.MemberAccounts[k.CharacterID] == "" || life.Phase != 0 {
			return r, fmt.Errorf("超时空死亡记录归属无效")
		}
		deaths += uint64(life.Deaths)
	}
	if deaths != uint64(b.PhaseDeaths[0]) {
		return r, fmt.Errorf("超时空死亡汇总不符")
	}
	r = currentAntonResultProjection{Phase: 0, Failed: b.TimedOut, ClearSeconds: uint32(at.Sub(b.EnterAfter) / time.Second), Deaths: b.PhaseDeaths[0]}
	r.FinalWeeklyRequired = !r.Failed
	if !r.Failed {
		r.DomainRank, err = b.AntonRewards.Phases[0].RankForDeaths(r.Deaths)
		if err != nil {
			return r, err
		}
	}
	r.Body, err = raid.BuildAntonResultBody(r.Failed, 0, r.ClearSeconds, uint16(min(r.Deaths, 65535)), r.DomainRank, option)
	if err == nil && r.Failed {
		// 现用601先写raidObj+0x2c，1484状态6只有读到3才走超时空失败文案。
		// 安图恩/卢克的通用失败类型1在此无效，成功结果与奖励资格保持。
		r.Body[0] = 3
	}
	return r, err
}

// Only an accepted server completion creates this frozen entitlement. The
// existing receipt transaction handles offline members, full bags and retries.
func (s *Service) freezeCurrentFiendWarRewardFlowLocked(state runtimeRaidState) error {
	b := state.Battle
	if b == nil || b.FiendWar == nil || !b.PhaseCompleted {
		return nil
	}
	if b.RewardFlow[0] != nil {
		return s.freezeCurrentRaidFinalWeeklyIntentLocked(state)
	}
	result, err := prepareCurrentFiendWarResult(state, 0)
	if err != nil || result.Failed {
		return raid.ErrRewardIneligible
	}
	weekly, err := prepareCurrentRaidFinalWeeklyCommand(state)
	if err != nil {
		return err
	}
	f := &currentAntonRewardFlow{InstanceID: b.InstanceID, RaidKey: state.RaidKey, Mode: 3, CompletedAt: b.PhaseCompletedAt, Configuration: b.AntonFlowConfig, Rewards: b.AntonRewards.Clone().Phases[0], Rank: result.DomainRank, Party: map[uint16]*currentAntonRewardCard{}, Squad: map[uint16]*currentAntonRewardCard{}}
	f.Weekly = weekly
	for id, account := range b.MemberAccounts {
		clears := currentAntonPhaseClearCount(b, id)
		if clears < f.Rewards.MinimumClears {
			continue
		}
		g := b.MemberGroups[id]
		if account == "" || g < 1 || g > 8 {
			return raid.ErrRewardOwnership
		}
		f.Recipients = append(f.Recipients, currentAntonRewardRecipient{id, account, g, clears})
	}
	if len(f.Recipients) > 8 {
		return raid.ErrRewardIneligible
	}
	sort.Slice(f.Recipients, func(i, j int) bool { return f.Recipients[i].CharacterID < f.Recipients[j].CharacterID })
	var movie uint32
	for _, v := range f.Configuration.MovieEndSeconds {
		movie = max(movie, v)
	}
	if movie == 0 {
		return fmt.Errorf("超时空PVF结算动画时长缺失")
	}
	f.AutoSelectAt = f.CompletedAt.Add(2 * time.Duration(movie) * time.Second)
	if s.antonRewardFlows == nil {
		s.antonRewardFlows = map[string]*currentAntonRewardFlow{}
	}
	key := currentAntonFlowKey(f.InstanceID, 0)
	if s.antonRewardFlows[key] != nil {
		return raid.ErrRewardReceipt
	}
	if err := s.freezeCurrentRaidFinalWeeklyIntentLocked(state); err != nil {
		return err
	}
	s.antonRewardFlows[key] = f
	b.RewardFlow[0] = f
	b.RewardCards[0] = f.Party
	return nil
}
