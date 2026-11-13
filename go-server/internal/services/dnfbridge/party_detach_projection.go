package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/raid"
)

// Membership and asset cleanup stay synchronous. Only the committed peer
// projection moves to its recipient's bounded queue, so returning to selection
// never waits for that peer's repository reads or socket deadline.
func (s *Service) queueDetachedPartyProjection(identity boundGameSessionCharacter, state alignedcmd.PartyState, deliver func() error) error {
	current := s.currentPartyProjectionMembershipGuard(identity, &state)
	return s.tryPostGameSessionCharacterEvent(identity.session, "party-detach-projection", identity.character, identity.generation, func() error {
		if !s.boundGameSessionCharacterCurrent(identity) || !current() {
			return nil
		}
		return deliver()
	})
}

func (s *Service) queueDetachedPartyMemberRemoved(previous, next alignedcmd.PartyState, removed uint16) {
	members := runtimePartyMembers(next)
	if next.PartyID <= 0 {
		members = runtimePartyMembers(previous)
	}
	for _, member := range members {
		if member.UserID == removed {
			continue
		}
		identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok || !s.storeCurrentPartyProjection(identity, next) {
			continue
		}
		if err := s.queueDetachedPartyProjection(identity, next, func() error {
			return s.sendRuntimePartyMemberRemoved(identity.session, removed, next)
		}); err != nil {
			s.logGameEvent(identity.session, "game-party-detach-projection-deferred", "error", err)
		}
	}
	s.queueDetachedPartyRaidSoloRefresh(runtimePartySoloMemberIDs(previous, next))
}

func (s *Service) queueDetachedPartyRaidSoloRefresh(ids []uint16) {
	for _, snapshot := range s.updateCurrentRaidSoloForPartyMembers(ids, false) {
		for _, member := range snapshot.Members {
			identity, ok := s.onlineGameSessionCharacterSnapshot(member.CharID)
			if !ok {
				continue
			}
			err := s.tryPostGameSessionCharacterEvent(identity.session, "party-detach-raid-roster", identity.character, identity.generation, func() error {
				if !s.boundGameSessionCharacterCurrent(identity) {
					return nil
				}
				live, found := s.runtimeRaidForMember(identity.character)
				if !found || live.RaidKey != snapshot.RaidKey {
					return nil
				}
				body, err := s.buildRuntimeRaidRosterBody(live)
				if err != nil {
					return err
				}
				return s.withGameWire(identity.session, func() error {
					current, present := s.runtimeRaidForMember(identity.character)
					if !s.boundGameSessionCharacterCurrent(identity) || !present || current.RaidKey != snapshot.RaidKey {
						return nil
					}
					return s.sendGameUpperRawClassCodecLocked(identity.session, raid.RaidMemberRefreshMsgID, body, 0, true)
				})
			})
			if err != nil {
				s.logGameEvent(identity.session, "game-party-detach-raid-roster-deferred", "error", err)
			}
		}
	}
}

func (s *Service) queueDetachedPartySnapshots(state alignedcmd.PartyState) {
	if state.PartyID <= 0 || state.PartyID >= int(^uint16(0)) {
		return
	}
	snapshot, found := s.runtimePartyManagerForService().SnapshotByID(uint16(state.PartyID))
	if !found {
		return
	}
	for _, member := range snapshot.Members {
		identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || identity.generation != member.SessionGeneration {
			continue
		}
		next := snapshot.StateFor(member.UserID)
		if err := s.queueDetachedPartyProjection(identity, next, func() error {
			if err := s.sendRuntimePartySnapshot(identity.session, next); err != nil {
				return err
			}
			s.sendDungeonPartyFrameRosterForReceiver(next, "runtime_party_snapshot_dungeon_roster", identity.session)
			s.sendCurrentPartyCrossSceneProfilesForReceiver(snapshot, identity.session)
			return nil
		}); err != nil {
			s.logGameEvent(identity.session, "game-party-detach-snapshot-deferred", "error", err)
		}
	}
}
