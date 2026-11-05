package dnfbridge

import "longheng.io/server/internal/modules/dnf/alignedcmd"

// An unchanged pre-raid party already owns a live peer transport. Recreating
// it retires that transport and replays formation packets for the same peers.
// Reuse only an exact current-generation membership/leader/slot match; actual
// raid regrouping still uses the central manager's replacement transition.
func (s *Service) matchPreformedRuntimeRaidParty(raid runtimeRaidState, desired alignedcmd.PartyState) (alignedcmd.PartyState, bool) {
	if s.options.currentClientVersion != "official-24.9" || !raid.CurrentLayout || raid.RouteOrRaidType > 1 {
		return alignedcmd.PartyState{}, false
	}
	snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(desired.UserID, 0)
	members := runtimePartyMembers(desired)
	if !found || snapshot.Leader != desired.UserID || len(snapshot.Members) != len(members) {
		return alignedcmd.PartyState{}, false
	}
	for slot, wanted := range members {
		matched := false
		for _, member := range snapshot.Members {
			if member.UserID != wanted.UserID || int(member.Slot) != slot {
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
