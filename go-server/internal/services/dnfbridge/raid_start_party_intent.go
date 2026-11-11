package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/party"
)

// prepareCurrentRaidStartPartyIntent resolves initial normal Anton/Luke party
// intent on a detached copy. Explicit manager seats may regroup members; an
// automatic admission seat may not merge distinct existing ordinary parties.
// This only reads one manager snapshot set, without publishing or mutating it.
func (s *Service) prepareCurrentRaidStartPartyIntent(state runtimeRaidState) (runtimeRaidState, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || !state.CurrentLayout ||
		state.RouteOrRaidType > 1 || state.CurrentUIFlags[0] != 0 || state.Started ||
		(state.Battle != nil && (state.Battle.Single || state.Battle.FiendWar != nil)) {
		return state, nil
	}
	planned := cloneRuntimeRaidState(state)
	maximum, capacity := runtimeRaidMemberPlacementLimits(state)
	indices := make(map[uint16]int, len(planned.Members))
	reserved := make(map[byte]bool)
	explicitSlots := make(map[byte]map[byte]bool)
	for i := range planned.Members {
		member := &planned.Members[i]
		if _, duplicate := indices[member.CharID]; member.CharID == 0 || duplicate {
			return state, fmt.Errorf("raid start party intent: invalid or duplicate raid member %d", member.CharID)
		}
		indices[member.CharID] = i
		member.OrdinaryPartySolo = true
		if !member.RecruitingPlacementExplicit {
			continue
		}
		if member.GroupIndex == 0 || member.GroupIndex > maximum || int(member.SlotOrder) >= capacity {
			return state, fmt.Errorf("raid start party intent: invalid explicit placement for member %d", member.CharID)
		}
		if explicitSlots[member.GroupIndex] == nil {
			explicitSlots[member.GroupIndex] = make(map[byte]bool)
		}
		if explicitSlots[member.GroupIndex][member.SlotOrder] {
			return state, fmt.Errorf("raid start party intent: explicit group %d repeats slot %d", member.GroupIndex, member.SlotOrder)
		}
		explicitSlots[member.GroupIndex][member.SlotOrder] = true
		reserved[member.GroupIndex] = true
		member.OrdinaryPartySolo = false
	}

	// Do not use the lazy accessor: preparing intent must not even create a
	// manager. An absent manager means no existing ordinary parties to preserve.
	s.mu.Lock()
	manager := s.runtimePartyManager
	s.mu.Unlock()
	var snapshots []party.RuntimePartySnapshot
	if manager != nil {
		snapshots = manager.Snapshots()
	}
	seen := make(map[uint16]bool)
	for _, snapshot := range snapshots {
		touchesRaid := false
		for _, member := range snapshot.Members {
			if _, found := indices[member.UserID]; found {
				touchesRaid = true
				break
			}
		}
		if !touchesRaid {
			continue
		}
		if snapshot.ID == 0 || len(snapshot.Members) == 0 || len(snapshot.Members) > capacity {
			return state, fmt.Errorf("raid start party intent: invalid ordinary party %d", snapshot.ID)
		}
		remaining := make([]party.RuntimePartyMember, 0, len(snapshot.Members))
		slots := make(map[byte]bool)
		leaderFound := false
		for _, member := range snapshot.Members {
			index, found := indices[member.UserID]
			if !found {
				return state, fmt.Errorf("raid start party intent: party %d includes non-raid member %d", snapshot.ID, member.UserID)
			}
			if seen[member.UserID] || int(member.Slot) >= capacity || slots[member.Slot] {
				return state, fmt.Errorf("raid start party intent: party %d has invalid member or fixed slot", snapshot.ID)
			}
			seen[member.UserID], slots[member.Slot] = true, true
			leaderFound = leaderFound || member.UserID == snapshot.Leader
			if !planned.Members[index].RecruitingPlacementExplicit {
				remaining = append(remaining, member)
			}
		}
		if !leaderFound {
			return state, fmt.Errorf("raid start party intent: party %d has no leader", snapshot.ID)
		}
		// Explicitly moving one half of a pair does not authorize attaching its
		// remaining member to another party or to the explicit manager group.
		if len(remaining) < 2 {
			continue
		}
		// Keep a consistent existing number when available. Otherwise allocate
		// a free number by actual PartyID order, never by automatic raw seats.
		group := planned.Members[indices[remaining[0].UserID]].GroupIndex
		for _, member := range remaining[1:] {
			if planned.Members[indices[member.UserID]].GroupIndex != group {
				group = 0
				break
			}
		}
		if group == 0 || group > maximum || reserved[group] {
			group = 0
			for candidate := byte(1); candidate <= maximum; candidate++ {
				if !reserved[candidate] {
					group = candidate
					break
				}
			}
		}
		if group == 0 {
			return state, fmt.Errorf("raid start party intent: no free group for ordinary party %d", snapshot.ID)
		}
		reserved[group] = true
		for _, member := range remaining {
			row := &planned.Members[indices[member.UserID]]
			row.GroupIndex, row.SlotOrder, row.OrdinaryPartySolo = group, member.Slot, false
		}
	}
	return planned, nil
}
