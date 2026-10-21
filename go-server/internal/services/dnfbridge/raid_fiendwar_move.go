package dnfbridge

import (
	"fmt"
	"time"
)

type currentFiendWarMovePlan struct {
	raid               uint32
	instance           string
	character          uint16
	from, to           int
	applied, confirmed bool
}

func (s *Service) prepareCurrentFiendWarMove(p *gameSession, q currentTownSetUserAreaRequest) (*currentFiendWarMovePlan, error) {
	if !isCurrentFiendWarChannel(p) {
		return nil, nil
	}
	p.dungeon.mu.Lock()
	r := p.dungeon.runtime
	pending := r != nil && r.raidEntry != nil && r.raidEntry.ChannelType == 42 && r.townReturnPending && r.townReturnOp24Sent && r.townReturnTransition.TownID == q.TownID && r.townReturnTransition.AreaID == q.AreaID
	p.dungeon.mu.Unlock()
	if pending {
		return &currentFiendWarMovePlan{}, nil
	}
	c, err := s.loadCurrentFiendWarConfig()
	if err != nil {
		return nil, err
	}
	if int(q.TownID) != c.Battle.WaitingTown {
		return nil, fmt.Errorf("超时空频道不能跨入普通城镇")
	}
	if q.AreaID < 2 {
		return &currentFiendWarMovePlan{}, nil
	}
	if _, ok := c.Battle.Areas[int(q.AreaID)]; !ok {
		return nil, fmt.Errorf("超时空目标不在真实PVF区域图中")
	}
	if !s.currentRaidAuthenticated(p) {
		return nil, fmt.Errorf("超时空移动角色未认证")
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(p.selectedCharacterID)
	if state == nil || state.RouteOrRaidType != 2 {
		return nil, fmt.Errorf("超时空区域需要当前攻坚队")
	}
	if !state.Started {
		if int(q.AreaID) != c.Battle.WaitingArea {
			return nil, fmt.Errorf("超时空尚未开战")
		}
		return &currentFiendWarMovePlan{}, nil
	}
	b := state.Battle
	if b == nil || b.FiendWar == nil || b.TimedOut || b.PhaseCompleted || b.MemberAccounts[p.selectedCharacterID] != s.accountIDForSession(p) || b.ChannelID != p.channel.ID || time.Now().Before(b.EnterAfter) || !time.Now().Before(b.Deadline) {
		return nil, fmt.Errorf("超时空移动的战场或角色归属已失效")
	}
	group := b.MemberGroups[p.selectedCharacterID]
	if b.FiendWarAdmissionClaim.contains(p.selectedCharacterID) {
		return nil, errCurrentFiendWarAdmissionBusy
	}
	from := b.FiendWar.MemberArea(p.selectedCharacterID, group)
	preview := b.FiendWar.Clone()
	if err := preview.MoveMember(p.selectedCharacterID, group, int(q.AreaID), time.Now()); err != nil {
		return nil, err
	}
	return &currentFiendWarMovePlan{raid: state.RaidKey, instance: b.InstanceID, character: p.selectedCharacterID, from: from, to: int(q.AreaID)}, nil
}
func (s *Service) applyCurrentFiendWarMove(plan *currentFiendWarMovePlan) error {
	if plan == nil || plan.instance == "" {
		return nil
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[plan.raid]
	if state == nil || state.Battle == nil || state.Battle.InstanceID != plan.instance || state.Battle.FiendWar == nil || state.Battle.TimedOut || state.Battle.PhaseCompleted {
		return fmt.Errorf("超时空移动提交时战场已更换")
	}
	b := state.Battle
	if b.FiendWarAdmissionClaim.contains(plan.character) {
		return errCurrentFiendWarAdmissionBusy
	}
	group := b.MemberGroups[plan.character]
	if b.FiendWar.MemberArea(plan.character, group) != plan.from {
		return fmt.Errorf("超时空移动原区域已变化")
	}
	next := b.FiendWar.Clone()
	if err := next.MoveMember(plan.character, group, plan.to, time.Now()); err != nil {
		return err
	}
	b.FiendWar = next
	b.ProgressVersion++
	plan.applied = true
	return nil
}
func (s *Service) cancelCurrentFiendWarMove(plan *currentFiendWarMovePlan) {
	if plan == nil || !plan.applied || plan.confirmed {
		return
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[plan.raid]
	if state == nil || state.Battle == nil || state.Battle.InstanceID != plan.instance || state.Battle.FiendWar == nil {
		return
	}
	b := state.Battle
	if b.FiendWar.MemberAreas[plan.character] != plan.to {
		return
	}
	next := b.FiendWar.Clone()
	next.MemberAreas[plan.character] = plan.from
	next.GroupAreas[b.MemberGroups[plan.character]] = plan.from
	b.FiendWar = next
	b.ProgressVersion++
}
func (s *Service) currentFiendWarTownArea(townID, areaID byte) bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" || (areaID < 2 || townID != 98) {
		return false
	}
	c, err := s.loadCurrentFiendWarConfig()
	if err != nil || int(townID) != c.Battle.WaitingTown {
		return false
	}
	_, ok := c.Battle.Areas[int(areaID)]
	return ok
}
