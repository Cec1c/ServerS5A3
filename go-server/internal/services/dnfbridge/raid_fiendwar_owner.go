package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"time"
)

func isCurrentFiendWarChannel(p *gameSession) bool { return p != nil && p.channel.Type == 42 }
func currentRaidMemberMaximum(s runtimeRaidState) int {
	if s.RouteOrRaidType == 2 {
		return 8
	}
	return raid.MaxAttackPartyMembers
}
func currentRaidGroupMaximum(s runtimeRaidState) byte {
	if s.RouteOrRaidType == 2 {
		return 8
	}
	return runtimeRaidMaxGroups
}
func currentRaidGroupCapacity(s runtimeRaidState) int {
	if s.RouteOrRaidType == 2 {
		return 1
	}
	return runtimeRaidGroupSize
}
func (s *Service) loadCurrentFiendWarConfig() (*fiendwar.FiendWarConfig, error) {
	s.initialEquipmentMu.Lock()
	defer s.initialEquipmentMu.Unlock()
	if s.fiendWarConfig != nil {
		return s.fiendWarConfig, nil
	}
	a, e := s.initialEquipmentArchiveLocked()
	if e != nil {
		return nil, e
	}
	c, e := fiendwar.LoadFiendWarConfig(a)
	if e == nil {
		s.fiendWarConfig = c
	}
	return c, e
}
func (s *Service) prepareCurrentFiendWarBattle(p *gameSession, state runtimeRaidState) (*runtimeRaidBattle, error) {
	if state.RouteOrRaidType != 2 || state.CurrentUIFlags != [3]byte{} || !s.currentRaidAuthenticated(p) {
		return nil, fmt.Errorf("超时空创建身份或模式不符")
	}
	c, err := s.loadCurrentFiendWarConfig()
	if err != nil {
		return nil, err
	}
	if len(state.Members) < c.StartMinimumMembers || len(state.Members) > c.MemberMax || runtimeRaidHasOversizedGroup(state) {
		return nil, fmt.Errorf("超时空编制不满足PVF")
	}
	r, ok := s.repositoryGroup()
	if !ok || r.Character == nil {
		return nil, fmt.Errorf("超时空角色仓储不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	now := time.Now()
	policy, err := currentRaidWeeklyPolicy()
	if err != nil {
		return nil, err
	}
	b := &runtimeRaidBattle{Mode: 3, ChannelID: p.channel.ID, ChannelType: p.channel.Type, StartedAt: now, FiendWarConfig: c, MemberAccounts: map[uint16]string{}, MemberGroups: map[uint16]byte{}, FiendWarClearCounts: map[uint16]uint32{}}
	accounts := map[string]bool{}
	groups := map[byte]bool{}
	for _, m := range state.Members {
		peer, present := s.onlineGameSession(m.CharID)
		if m.CharID == p.selectedCharacterID {
			peer, present = p, true
		}
		if !present || !s.currentRaidAuthenticated(peer) || peer.channel.ID != p.channel.ID || peer.channel.Type != 42 || m.GroupIndex < 1 || m.GroupIndex > 8 || groups[m.GroupIndex] {
			return nil, fmt.Errorf("超时空成员不在线或编制重复")
		}
		actor, present, err := r.Character.Load(ctx, strconv.Itoa(int(m.CharID)))
		if err != nil {
			return nil, err
		}
		if !present || actor.AccountID != s.accountIDForSession(peer) || accounts[actor.AccountID] || actor.Stats["delete_flag"] != 0 {
			return nil, fmt.Errorf("超时空成员账号不符或重复")
		}
		accounts[actor.AccountID], groups[m.GroupIndex] = true, true
		used, err := raid.WeeklyClears(actor, 3, now, policy)
		if err != nil {
			return nil, err
		}
		if used >= int(c.WeeklyClearLimit) {
			return nil, raid.ErrWeeklyClearLimit
		}
		b.MemberAccounts[m.CharID] = actor.AccountID
		b.MemberGroups[m.CharID] = m.GroupIndex
	}
	b.EnterAfter = now.Add(time.Duration(c.Battle.Raid.StartDelaySeconds) * time.Second)
	b.Deadline = b.EnterAfter.Add(time.Duration(c.Battle.Raid.PhaseTimeoutSeconds[0]) * time.Second)
	b.Configuration = raid.BattleConfiguration{WeeklyClearLimit: int(c.WeeklyClearLimit), DisconnectWaitSeconds: uint32(c.DisconnectMemberWaitSeconds), StartDelaySeconds: uint32(c.Battle.Raid.StartDelaySeconds), PhaseLimitSeconds: []uint32{uint32(c.Battle.Raid.PhaseTimeoutSeconds[0])}}
	b.FiendWar, err = fiendwar.NewFiendWarBattle(c.Battle)
	if err != nil {
		return nil, err
	}
	if err = b.FiendWar.Apply(fiendwar.FiendWarEvent{}, b.EnterAfter); err != nil {
		return nil, err
	}
	var dependencies []uint32
	for _, a := range c.Battle.Areas {
		if a.Dungeon > 0 {
			dependencies = append(dependencies, uint32(a.Dungeon))
		}
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err = raid.ValidateDungeonDependencies(archive, dependencies); err != nil {
		return nil, err
	}
	rewards, err := raid.LoadFiendWarRewardConfiguration(archive)
	if err != nil {
		return nil, err
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		return nil, err
	}
	if err = validateCurrentAntonRewardDependencies(catalog, rewards, now); err != nil {
		return nil, err
	}
	b.AntonRewards = &rewards
	for i, n := range c.Battle.Raid.MovieEndSeconds {
		b.AntonFlowConfig.MovieEndSeconds[i] = uint32(n)
	}
	for id, group := range b.MemberGroups {
		b.FiendWar.MemberAreas[id] = c.Battle.WaitingArea
		b.FiendWar.GroupAreas[group] = c.Battle.WaitingArea
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	b.InstanceID = hex.EncodeToString(nonce[:])
	syncCurrentFiendWarDungeons(b)
	return b, nil
}
func syncCurrentFiendWarDungeons(b *runtimeRaidBattle) {
	if b == nil || b.FiendWar == nil {
		return
	}
	b.Dungeons = nil
	for _, a := range b.FiendWar.Config.Areas {
		if a.Dungeon <= 0 {
			continue
		}
		status := byte(2)
		if b.FiendWar.AreaOpen(a.Index) || b.FiendWar.Areas[a.Index].Group != 0 {
			status = 0
		}
		b.Dungeons = append(b.Dungeons, raid.DungeonState{ID: uint32(a.Dungeon), State: status})
	}
}

// 调用方持有 raidMu；资格、全员门票、账号日次数与 Started 的提交连续进行。
func (s *Service) consumeCurrentFiendWarEntry(p *gameSession, state runtimeRaidState, b *runtimeRaidBattle) error {
	r, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("超时空仓储不可用")
	}
	u, ok := r.CharacterTrade.(repo.RaidEntryAssetUnitOfWork)
	if !ok {
		return fmt.Errorf("超时空原子入场事务不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	ids := make([]string, 0, len(b.MemberAccounts))
	for id := range b.MemberAccounts {
		ids = append(ids, strconv.Itoa(int(id)))
	}
	rule := raid.AdmissionRule{ItemID: b.FiendWarConfig.EntryCost.ItemID, Count: int64(b.FiendWarConfig.EntryCost.Count)}
	policy, err := currentRaidWeeklyPolicy()
	if err != nil {
		return err
	}
	return u.WithinRaidEntryAssets(ctx, ids, fiendwar.FiendWarDailyScope, func(c repo.CharacterRepository, i repo.InventoryRepository, settings repo.SettingsRepository) error {
		daily, _, err := settings.Load(ctx, fiendwar.FiendWarDailyScope)
		if err != nil {
			return err
		}
		accounts := make([]string, 0, len(ids))
		bags := make([]repo.InventoryRecord, 0, len(ids))
		for _, id := range ids {
			actor, present, err := c.Load(ctx, id)
			if err != nil {
				return err
			}
			n, _ := strconv.Atoi(id)
			if !present || actor.AccountID != b.MemberAccounts[uint16(n)] || actor.Stats["delete_flag"] != 0 {
				return fmt.Errorf("超时空入场角色归属变更")
			}
			used, err := raid.WeeklyClears(actor, 3, b.StartedAt, policy)
			if err != nil {
				return err
			}
			if used >= b.Configuration.WeeklyClearLimit {
				return raid.ErrWeeklyClearLimit
			}
			bag, present, err := i.Load(ctx, id)
			if err != nil {
				return err
			}
			if !present {
				return fmt.Errorf("超时空入场背包缺失")
			}
			bag = repo.CloneInventory(bag)
			if err = takeRaidEntryCost(&bag, rule); err != nil {
				return err
			}
			bags = append(bags, bag)
			accounts = append(accounts, actor.AccountID)
		}
		next, err := fiendwar.ReserveFiendWarDailyEntries(daily, accounts, b.FiendWarConfig.Battle.DailyAccountEnterLimit, b.StartedAt)
		if err != nil {
			return err
		}
		for _, bag := range bags {
			if err = repo.SaveInventoryFields(ctx, i, bag, repo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		return settings.Save(ctx, next)
	})
}
