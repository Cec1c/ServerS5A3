package dnfbridge

import (
	"fmt"
	"time"
)

func (s *Service) moveCurrentFiendWarEncounterRoster(captured runtimeRaidState, members []runtimeRaidMemberState, target int) (map[uint16]int, error) {
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[captured.RaidKey]
	if state == nil || state.Battle == nil || captured.Battle == nil || state.Battle.InstanceID != captured.Battle.InstanceID || state.Battle.FiendWar == nil || state.Battle.TimedOut || state.Battle.PhaseCompleted {
		return nil, fmt.Errorf("超时空移动组队战场已更换")
	}
	b := state.Battle
	next := b.FiendWar.Clone()
	previous := map[uint16]int{}
	for _, m := range members {
		previous[m.CharID] = next.MemberArea(m.CharID, b.MemberGroups[m.CharID])
		if err := next.MoveMember(m.CharID, b.MemberGroups[m.CharID], target, time.Now()); err != nil {
			return nil, err
		}
	}
	b.FiendWar = next
	b.ProgressVersion++
	return previous, nil
}
func (s *Service) rollbackCurrentFiendWarEncounterRoster(captured runtimeRaidState, previous map[uint16]int, target int) {
	// Inspect runtimes outside raidMu; only the exact new battle can retain them.
	entered := map[uint16]bool{}
	for id := range previous {
		if p, ok := s.onlineGameSession(id); ok {
			p.dungeon.mu.Lock()
			r := p.dungeon.runtime
			entered[id] = r != nil && r.raidEntry != nil && r.raidEntry.InstanceID == captured.Battle.InstanceID
			p.dungeon.mu.Unlock()
		}
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[captured.RaidKey]
	if state == nil || state.Battle == nil || state.Battle.InstanceID != captured.Battle.InstanceID || state.Battle.FiendWar == nil {
		return
	}
	b := state.Battle
	next := b.FiendWar.Clone()
	changed := false
	for id, area := range previous {
		if !entered[id] && next.MemberArea(id, b.MemberGroups[id]) == target {
			next.MemberAreas[id] = area
			changed = true
		}
	}
	if changed {
		b.FiendWar = next
		b.ProgressVersion++
	}
}
