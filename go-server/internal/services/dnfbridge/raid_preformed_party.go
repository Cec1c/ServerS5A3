package dnfbridge

import "longheng.io/server/internal/modules/dnf/alignedcmd"

// An unchanged pre-raid party already owns a live peer transport. Recreating
// it retires that transport and replays formation packets for the same peers.
// Reuse only exact current-generation membership and slots. Without explicit
// regrouping, leadership remains with the real party even when slot0 is not
// its leader. Explicit regrouping still uses the manager's replacement transition.
func (s *Service) matchPreformedRuntimeRaidParty(raid runtimeRaidState, desired alignedcmd.PartyState) (alignedcmd.PartyState, bool) {
	if s.options.currentClientVersion != "official-24.9" || !raid.CurrentLayout || raid.RouteOrRaidType > 1 {
		return alignedcmd.PartyState{}, false
	}
	snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(desired.UserID, 0)
	members := runtimePartyMembers(desired)
	if !found || len(snapshot.Members) != len(members) {
		return alignedcmd.PartyState{}, false
	}
	explicit, containsLeader := false, false
	for _, wanted := range members {
		index := runtimeRaidMemberIndex(raid, wanted.UserID)
		if index < 0 {
			return alignedcmd.PartyState{}, false
		}
		explicit = explicit || raid.Members[index].RecruitingPlacementExplicit
		containsLeader = containsLeader || wanted.UserID == snapshot.Leader
	}
	if !containsLeader || ((explicit || !runtimeRaidKeepsUneditedSolos(raid)) && snapshot.Leader != desired.UserID) {
		return alignedcmd.PartyState{}, false
	}
	for _, wanted := range members {
		matched := false
		for _, member := range snapshot.Members {
			if member.UserID != wanted.UserID || member.Slot != wanted.Slot {
				continue
			}
			identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
			if !online || identity.generation != member.SessionGeneration {
				return alignedcmd.PartyState{}, false
			}
			matched = true
			break
		}
		if !matched {
			return alignedcmd.PartyState{}, false
		}
	}
	return s.publishRuntimePartySnapshot(snapshot), true
}
