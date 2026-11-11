package dnfbridge

import "longheng.io/server/internal/modules/dnf/alignedcmd"

// Waiting-room free parties own the live group projection after raid start.
// A solo marker hides a former placement and must not reserve its group seats.
// Other parties keep their groups; frozen reward admission is never modified.
// Only a newly formed two-member party picks the first free group; a later
// join/refresh keeps the already-live party's number when it remains available.
func planStartedRaidOrdinaryPartyGroup(raid runtimeRaidState, party alignedcmd.PartyState, newlyFormed ...bool) ([]runtimeRaidMemberState, bool) {
	members := runtimePartyMembers(party)
	maximum, capacity := runtimeRaidMemberPlacementLimits(raid)
	anchor := runtimeRaidMemberIndex(raid, party.UserID)
	if anchor < 0 || len(members) < 2 || len(members) > capacity {
		return nil, false
	}
	ids := make(map[uint16]bool, len(members))
	for _, member := range members {
		if runtimeRaidMemberIndex(raid, member.UserID) < 0 || currentRaidMemberActiveForPartySync(&raid, member.UserID) {
			return nil, false
		}
		ids[member.UserID] = true
	}
	if !ids[party.UserID] {
		return nil, false
	}
	blocked := make(map[byte]bool)
	for _, member := range raid.Members {
		if !ids[member.CharID] && (!member.OrdinaryPartySolo || currentRaidMemberActiveForPartySync(&raid, member.CharID)) {
			blocked[normalizedRuntimeRaidGroup(member.GroupIndex)] = true
		}
	}
	group := normalizedRuntimeRaidGroup(raid.Members[anchor].GroupIndex)
	fresh := len(newlyFormed) > 0 && newlyFormed[0]
	if fresh || group < 1 || group > maximum || blocked[group] {
		group = 0
		for candidate := byte(1); candidate <= maximum; candidate++ {
			if !blocked[candidate] {
				group = candidate
				break
			}
		}
	}
	if group == 0 {
		return nil, false
	}
	candidate := append([]runtimeRaidMemberState(nil), raid.Members...)
	changed := false
	for _, member := range members {
		i := runtimeRaidMemberIndex(raid, member.UserID)
		if candidate[i].GroupIndex != group || candidate[i].SlotOrder != member.Slot || candidate[i].OrdinaryPartySolo {
			candidate[i].GroupIndex, candidate[i].SlotOrder, candidate[i].OrdinaryPartySolo = group, member.Slot, false
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	return candidate, true
}
