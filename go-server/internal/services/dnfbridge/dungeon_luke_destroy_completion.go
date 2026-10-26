package dnfbridge

import "longheng.io/server/internal/modules/dnf/worldmap"

// The two Luke energy maps complete via a PVF client-owned passive object,
// not an announced hostile actor. An accepted report must reach each present
// member's completion owner without inventing an op38 for that passive ID.
// Called under the reporter's dungeon lock; the returned work runs after unlock.
func (s *Service) currentLukeDestroyObjectPeerCompletion(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, objectKey uint32) func() {
	if s == nil || runtime == nil || runtime.partyInstance == nil || runtime.raidEntry == nil ||
		(runtime.Dungeon.ID != 5063 && runtime.Dungeon.ID != 5066) ||
		!runtime.ordinaryFinalRoomClearAccepted || !runtime.bossDieCheckAccepted ||
		!s.currentRaidAuthenticated(session) || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) ||
		currentDungeonDestroyObjectClearConditionSource(runtime, scene, objectKey) == "" {
		return nil
	}
	instance, room, dungeonID, maze := runtime.partyInstance, runtime.Room, runtime.Dungeon.ID, runtime.MazeIndex
	entry := *runtime.raidEntry
	entry.Participants = append([]uint16(nil), entry.Participants...)
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	party, joined := s.runtimePartyManagerForService().SnapshotByUser(identity.character, identity.generation)
	if !joined || party.ID != instance.key.PartyID || entry.RunID == "" {
		return nil
	}
	peers := s.partyDungeonInstancePeers(runtime, identity.character)
	identities := make(map[*gameSession]boundGameSessionCharacter, len(peers))
	for _, peer := range peers {
		if id, ok := s.boundGameSessionCharacterSnapshot(peer.Session); ok && id.character == peer.CharacterID {
			identities[peer.Session] = id
		}
	}
	return func() {
		for _, peer := range peers {
			peer := peer
			peerIdentity, bound := identities[peer.Session]
			if !bound {
				continue
			}
			s.dispatchPartyDungeonPeerFinalRoomCompletionChecked(peer.Session, instance.id, objectKey, func(r *runtimeDungeonState, current worldmap.DungeonRoomScene) bool {
				if !s.boundGameSessionCharacterCurrent(peerIdentity) || r.partyInstance != instance || r.Room != room || r.Session != peer.Scene ||
					r.Dungeon.ID != dungeonID || r.MazeIndex != maze || r.raidEntry == nil ||
					current.Coordinate != scene.Coordinate || current.Map.Map.ID != scene.Map.Map.ID ||
					r.townReturnPending || r.townReturnOp24Sent ||
					!dungeonRuntimeOwnsCharacter(r, peer.CharacterID) ||
					peer.Session.selectedCharacterID != peer.CharacterID || !s.currentRaidAuthenticated(peer.Session) ||
					currentDungeonDestroyObjectClearConditionSource(r, current, objectKey) == "" {
					return false
				}
				e := r.raidEntry
				if e.RaidKey != entry.RaidKey || e.InstanceID != entry.InstanceID || e.RunID != entry.RunID ||
					e.Phase != entry.Phase || e.Attempt != entry.Attempt || e.ChannelID != entry.ChannelID || e.ChannelType != entry.ChannelType ||
					peer.Session.channel.ID != e.ChannelID || peer.Session.channel.Type != e.ChannelType ||
					r.AccountID != s.accountIDForSession(peer.Session) || !sameRaidParticipants(e.Participants, entry.Participants) {
					return false
				}
				id, ok := s.boundGameSessionCharacterSnapshot(peer.Session)
				if !ok {
					return false
				}
				p, ok := s.runtimePartyManagerForService().SnapshotByUser(id.character, id.generation)
				if !ok || p.ID != party.ID {
					return false
				}
				instance.mu.Lock()
				defer instance.mu.Unlock()
				_, present := instance.members[peer.CharacterID]
				return present && instance.sessions[peer.CharacterID] == peer.Session && instance.scenes[peer.CharacterID] == r.Session
			})
		}
	}
}
