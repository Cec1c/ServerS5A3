package dnfbridge

// Unedited current-layout solos show the native "单人" chip (NOTI591 group 0),
// so their raw GroupIndex/SlotOrder is an internal seat only. That raw seat is
// still frozen as reward ownership (Battle.MemberGroups) at every phase start,
// where the reward freeze allows at most four owners per group.
//
// 2026-11-20 90CN: two members left group 1's party and stayed solo with raw
// group 1; a new four-member party was then materialized in group 1 because
// solos do not block a group. Phase 1 started 14 s later, froze group 1 with
// six owners, and every reward freeze retry failed with frozen_group_capacity.
//
// relocateRuntimeRaidOverflowSolos moves inactive unedited solos out of a group
// whose raw seats exceed capacity into the lowest group with a free seat.
// Pinned rows (the party being materialized), seat-reserving rows and solos
// with active dungeon participation never move. It returns the moved IDs and
// whether every group now fits.
func relocateRuntimeRaidOverflowSolos(raid runtimeRaidState, members []runtimeRaidMemberState, pinned map[uint16]bool) ([]uint16, bool) {
	maximum, capacity := runtimeRaidMemberPlacementLimits(raid)
	movable := func(member runtimeRaidMemberState) bool {
		return runtimeRaidKeepsUneditedSolos(raid) && member.GroupIndex != 0 && !pinned[member.CharID] &&
			!runtimeRaidMemberReservesOrdinaryGroup(raid, member) && !currentRaidMemberActiveForPartySync(&raid, member.CharID)
	}
	kept := make(map[byte]int, maximum)
	for _, member := range members {
		if member.GroupIndex != 0 && !movable(member) {
			kept[member.GroupIndex]++
		}
	}
	var overflow []int
	for i, member := range members {
		if !movable(member) {
			continue
		}
		if member.GroupIndex <= maximum && kept[member.GroupIndex] < capacity {
			kept[member.GroupIndex]++
			continue
		}
		overflow = append(overflow, i)
	}
	var moved []uint16
	for _, i := range overflow {
		target := byte(0)
		for group := byte(1); group <= maximum; group++ {
			if kept[group] < capacity {
				target = group
				break
			}
		}
		if target == 0 {
			return moved, false
		}
		used := make(map[byte]bool, capacity)
		for j, member := range members {
			if j != i && member.GroupIndex == target {
				used[member.SlotOrder] = true
			}
		}
		slot := byte(0)
		for int(slot) < capacity && used[slot] {
			slot++
		}
		members[i].GroupIndex, members[i].SlotOrder = target, slot
		kept[target]++
		moved = append(moved, members[i].CharID)
	}
	return moved, !runtimeRaidHasOversizedSeatGroup(raid, members)
}

// runtimeRaidHasOversizedSeatGroup counts every placed raw seat, solos
// included: this is exactly what a phase start freezes as reward ownership.
// Unplaced rows (GroupIndex 0) are counted after their placement.
func runtimeRaidHasOversizedSeatGroup(raid runtimeRaidState, members []runtimeRaidMemberState) bool {
	_, capacity := runtimeRaidMemberPlacementLimits(raid)
	counts := make(map[byte]int)
	for _, member := range members {
		if member.GroupIndex == 0 {
			continue
		}
		counts[member.GroupIndex]++
		if counts[member.GroupIndex] > capacity {
			return true
		}
	}
	return false
}
