package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
	"time"
)

func (s *Service) freezeCurrentFiendWarRetryLocked(state runtimeRaidState, now time.Time) error {
	b := state.Battle
	if b == nil || b.FiendWar == nil || b.FiendWarConfig == nil || !b.TimedOut || b.PhaseCompleted || !b.FiendWarConfig.Retry {
		return nil
	}
	key := b.InstanceID + ":retry"
	if s.antonRewardFlows[key] != nil {
		return nil
	}
	cost := b.FiendWarConfig.RetryItem
	if cost.ItemID == 0 || cost.Count <= 0 {
		return fmt.Errorf("超时空PVF失败返票配置无效")
	}
	f := &currentAntonRewardFlow{InstanceID: b.InstanceID, RaidKey: state.RaidKey, Mode: 3, FiendWarRetryCount: uint32(cost.Count), CompletedAt: now, Party: map[uint16]*currentAntonRewardCard{}, Rewards: raid.AntonPhaseRewards{Choices: []raid.AntonRewardChoice{{Type: "fiendwar_retry", Weight: 1, ContainerID: cost.ItemID}}}}
	for id, account := range b.MemberAccounts {
		if account == "" || id == 0 {
			return raid.ErrRewardOwnership
		}
		f.Recipients = append(f.Recipients, currentAntonRewardRecipient{CharacterID: id, AccountID: account, Group: b.MemberGroups[id]})
	}
	sort.Slice(f.Recipients, func(i, j int) bool { return f.Recipients[i].CharacterID < f.Recipients[j].CharacterID })
	if s.antonRewardFlows == nil {
		s.antonRewardFlows = map[string]*currentAntonRewardFlow{}
	}
	s.antonRewardFlows[key] = f
	return nil
}
func (s *Service) advanceCurrentFiendWarRetryFlow(f *currentAntonRewardFlow, catalog *pvfDungeonDropCatalog, repos repo.Group, now time.Time) error {
	for _, recipient := range f.Recipients {
		s.raidMu.Lock()
		card := f.Party[recipient.CharacterID]
		if card == nil {
			choice := f.Rewards.Choices[0]
			rows, err := resolveCurrentBoosterRewards(catalog, map[uint32]uint32{choice.ContainerID: f.FiendWarRetryCount}, nil, repo.ItemStack{}, f.CompletedAt)
			if err != nil {
				s.raidMu.Unlock()
				return err
			}
			data, err := json.Marshal(struct {
				Choice  raid.AntonRewardChoice
				Rewards []currentBoosterReward
			}{choice, rows})
			if err != nil {
				s.raidMu.Unlock()
				return err
			}
			card = &currentAntonRewardCard{Group: recipient.Group, Plan: currentAntonRewardPlan{Choice: choice, Rewards: rows, Fingerprint: sha256.Sum256(data)}}
			f.Party[recipient.CharacterID] = card
		}
		if card.Committed {
			s.raidMu.Unlock()
			continue
		}
		plan := cloneCurrentAntonRewardPlan(card.Plan)
		cmd := raid.RewardCommand{AccountID: recipient.AccountID, CharacterID: strconv.Itoa(int(recipient.CharacterID)), InstanceID: f.InstanceID, Mode: 3, Kind: 4, Fingerprint: plan.Fingerprint, CompletedAt: f.CompletedAt}
		s.raidMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		_, err := grantCurrentAntonRewardPlan(ctx, repos, catalog, plan, cmd)
		cancel()
		s.raidMu.Lock()
		if err == nil {
			card.Committed = true
		} else {
			f.NextAttempt = now.Add(2 * time.Second)
		}
		s.raidMu.Unlock()
		if err != nil {
			return err
		}
	}
	s.raidMu.Lock()
	f.FinishedAt = now
	s.raidMu.Unlock()
	for _, recipient := range f.Recipients {
		if p, ok := s.onlineGameSession(recipient.CharacterID); ok {
			id := recipient.CharacterID
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				defer cancel()
				_ = s.callGameSession(ctx, p, "超时空失败返票刷新", func() error {
					if p.selectedCharacterID != id || s.accountIDForSession(p) != recipient.AccountID {
						return nil
					}
					return s.sendSelectedCurrentContainerListsRefresh(p, "超时空失败返票已提交")
				})
			}()
		}
	}
	return nil
}

func (s *Service) resetCurrentFiendWarFailureLocked(state *runtimeRaidState, now time.Time) bool {
	b := state.Battle
	if b == nil || b.FiendWar == nil || !b.TimedOut || b.PhaseCompleted || len(b.Participation) != 0 || len(b.FiendWarEncounters) != 0 {
		return false
	}
	if b.FiendWarConfig.Retry {
		f := s.antonRewardFlows[b.InstanceID+":retry"]
		if f == nil || f.FinishedAt.IsZero() {
			return false
		}
	}
	ended := b.FiendWarEndedAt
	if ended.IsZero() {
		ended = b.Deadline
	}
	movie := 0
	for _, n := range b.FiendWarConfig.Battle.Raid.MovieEndSeconds {
		movie = max(movie, n)
	}
	if now.Before(ended.Add(time.Duration(movie) * time.Second)) {
		return false
	}
	state.Started = false
	state.Battle = nil
	for i := range state.Members {
		state.Members[i].UserState = 1
	}
	return true
}
func (s *Service) queueCurrentFiendWarLobbyReset(state runtimeRaidState) {
	for _, m := range state.Members {
		if p, ok := s.onlineGameSession(m.CharID); ok {
			go func(p *gameSession) {
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				defer cancel()
				if err := s.callGameSession(ctx, p, "超时空失败后恢复攻坚队", func() error {
					s.raidMu.Lock()
					current := s.raids[state.RaidKey]
					valid := current != nil && !current.Started && current.Battle == nil && runtimeRaidMemberIndex(*current, p.selectedCharacterID) >= 0
					s.raidMu.Unlock()
					if !valid || !s.currentRaidAuthenticated(p) {
						return nil
					}
					if err := s.sendRuntimeRaidMetadata(p, state, 2); err != nil {
						return err
					}
					body, err := s.buildRuntimeRaidRosterBody(state)
					if err != nil {
						return err
					}
					return s.sendGameUpperRawClass(p, raid.RaidMemberRefreshMsgID, body, 0)
				}); err != nil {
					s.logGameEvent(p, "超时空失败后攻坚队恢复等待重试", "error", err)
				}
			}(p)
		}
	}
}
