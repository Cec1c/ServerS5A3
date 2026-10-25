package dnfbridge

import (
	"sort"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// refreshCurrentRaidSoloAfterPartyLeave keeps the explicit leave/retry path
// separate from raid group placement. It is intentionally idempotent and always
// broadcasts so a repeated leave can repair a previous roster write failure.
func (s *Service) refreshCurrentRaidSoloAfterPartyLeave(identity boundGameSessionCharacter, previous alignedcmd.PartyState) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || !s.boundGameSessionCharacterCurrent(identity) {
		return
	}
	s.refreshCurrentRaidSoloForPartyMembers(
		runtimePartySoloMemberIDs(previous, alignedcmd.PartyState{}, identity.character),
		"ordinary_party_leave",
		true,
	)
}

// refreshCurrentRaidSoloForPartyMembers updates only NOTI591 presentation.
// GroupIndex/SlotOrder and Battle.MemberGroups remain raid/reward authority.
// Read party-manager snapshots while holding raidMu so an older projection
// cannot commit after a newer membership refresh. The manager never calls back
// into the bridge, and broadcasts remain outside both locks.
func (s *Service) refreshCurrentRaidSoloForPartyMembers(ids []uint16, reason string, forceBroadcast bool) {
	snapshots := s.updateCurrentRaidSoloForPartyMembers(ids, forceBroadcast)
	s.broadcastCurrentRaidSoloSnapshots(snapshots, reason)
}

// updateCurrentRaidSoloForPartyMembers commits marker changes and returns
// stable snapshots for the caller to broadcast after releasing raidMu. Join
// group synchronization uses this half directly so its final group placement
// can be included in one roster packet rather than broadcasting an intermediate
// group layout first.
func (s *Service) updateCurrentRaidSoloForPartyMembers(ids []uint16, forceBroadcast bool) []runtimeRaidState {
	if s == nil || s.options.currentClientVersion != "official-24.9" || len(ids) == 0 {
		return nil
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return nil
	}
	ids = uniqueRuntimePartySoloMemberIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	s.raidMu.Lock()
	solo := make(map[uint16]bool, len(ids))
	for _, id := range ids {
		party, found := manager.SnapshotByUser(id, 0)
		solo[id] = !found || len(party.Members) < 2
	}

	type affectedRaid struct {
		state *runtimeRaidState
		seen  bool
	}
	affected := make(map[uint32]*affectedRaid)
	for _, id := range ids {
		live := s.runtimeRaidByMemberLocked(id)
		if live == nil || !live.CurrentLayout || live.RouteOrRaidType > 1 {
			continue
		}
		index := runtimeRaidMemberIndex(*live, id)
		if index < 0 {
			continue
		}
		entry := affected[live.RaidKey]
		if entry == nil {
			entry = &affectedRaid{state: live}
			affected[live.RaidKey] = entry
		}
		if live.Members[index].OrdinaryPartySolo != solo[id] {
			live.Members[index].OrdinaryPartySolo = solo[id]
			entry.seen = true
		}
	}
	snapshots := make([]runtimeRaidState, 0, len(affected))
	for _, entry := range affected {
		if forceBroadcast || entry.seen {
			snapshots = append(snapshots, cloneRuntimeRaidState(*entry.state))
		}
	}
	s.raidMu.Unlock()
	return snapshots
}

func (s *Service) broadcastCurrentRaidSoloSnapshots(snapshots []runtimeRaidState, reason string) {
	if s == nil || len(snapshots) == 0 {
		return
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].RaidKey < snapshots[j].RaidKey })
	for _, snapshot := range snapshots {
		var anchor *gameSession
		if len(snapshot.Members) > 0 {
			anchor, _ = s.onlineGameSession(snapshot.Members[0].CharID)
		}
		if err := s.broadcastRuntimeRaidRefresh(snapshot); err != nil {
			if anchor != nil {
				s.logGameEvent(anchor, "game-raid-solo-roster-refresh-failed",
					"raid_key", snapshot.RaidKey, "reason", reason, "error", err)
			}
			continue
		}
		if anchor != nil {
			s.logGameEvent(anchor, "game-raid-solo-roster-refreshed",
				"raid_key", snapshot.RaidKey, "reason", reason,
				"wire", "noti591_group_index_zero_only")
		}
	}
}

func runtimePartySoloMemberIDs(previous, next alignedcmd.PartyState, extra ...uint16) []uint16 {
	ids := make([]uint16, 0, 8)
	for _, member := range runtimePartyMembers(previous) {
		ids = append(ids, member.UserID)
	}
	for _, member := range runtimePartyMembers(next) {
		ids = append(ids, member.UserID)
	}
	ids = append(ids, extra...)
	return uniqueRuntimePartySoloMemberIDs(ids)
}

func uniqueRuntimePartySoloMemberIDs(ids []uint16) []uint16 {
	unique := make(map[uint16]bool, len(ids))
	out := make([]uint16, 0, len(ids))
	for _, id := range ids {
		if id == 0 || unique[id] {
			continue
		}
		unique[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func resetRuntimeRaidSoloProjection(state *runtimeRaidState) {
	if state == nil {
		return
	}
	for i := range state.Members {
		state.Members[i].OrdinaryPartySolo = false
	}
}
