package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/party"
)

// The first mover has already left the origin area before the temporary party
// exists. Its ordinary town op6 can therefore have removed that actor from the
// stationary member's client. Neither op11 nor same-channel op9 creates a
// missing actor, and the pre-dungeon player state only describes the receiver.
// Restore every admitted remote actor before the first endpoint/roster packet;
// waiting until dungeon landing leaves the first peer creation without it.
func (s *Service) prepareCurrentFiendWarFormationActors(
	leader *gameSession,
	snapshot party.RuntimePartySnapshot,
	dungeonID int64,
) error {
	if len(snapshot.Members) < 2 {
		return nil
	}
	if s == nil || leader == nil || !isCurrentFiendWarChannel(leader) ||
		s.onlinePlayers == nil || snapshot.Leader != leader.selectedCharacterID {
		return fmt.Errorf("fiendwar formation actor context unavailable")
	}
	identities := make([]boundGameSessionCharacter, 0, len(snapshot.Members))
	actors := make([]onlinePlayerInfo, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || identity.generation != member.SessionGeneration ||
			identity.session.channel.ID != leader.channel.ID {
			return fmt.Errorf("fiendwar formation actor generation changed: %d", member.UserID)
		}
		if member.UserID != snapshot.Leader &&
			!s.currentFiendWarAdmissionAllowsPeer(leader, member.UserID, int(snapshot.ID), dungeonID) {
			return fmt.Errorf("fiendwar formation actor admission changed: %d", member.UserID)
		}
		actor, present := s.onlinePlayers.PlayerForCharacter(member.UserID)
		if !present || actor.Session != identity.session || actor.ChannelID != leader.channel.ID ||
			currentTownActorOwnerContext(identity.session) == currentSceneObjectContext {
			return fmt.Errorf("fiendwar formation actor town presence unavailable: %d", member.UserID)
		}
		identities = append(identities, identity)
		actors = append(actors, actor)
	}
	for i, receiver := range identities {
		for j, actor := range actors {
			if i == j {
				continue
			}
			if !s.boundGameSessionCharacterCurrent(receiver) || !s.boundGameSessionCharacterCurrent(identities[j]) {
				return fmt.Errorf("fiendwar formation actor selection changed")
			}
			const source = "fiendwar_before_first_party_endpoints"
			if err := s.sendTownRemoteActorStateWithPartyFrame(receiver.session, actor, source, false); err != nil {
				return fmt.Errorf("prepare fiendwar remote actor: %w", err)
			}
			if !s.boundGameSessionCharacterCurrent(receiver) || !s.boundGameSessionCharacterCurrent(identities[j]) {
				return fmt.Errorf("fiendwar formation actor selection changed after construction")
			}
			// Preserve the actual town presence: creation is not a claim that a
			// follower still at the origin has already crossed the door.
			areaBody := buildCurrentTownUserAreaNotificationBody(
				currentSceneActorObjectKey(actor.CharacterID), actor.TownID, actor.AreaID,
				actor.PositionX, actor.PositionY, actor.Direction, actor.AreaState,
			)
			if err := s.sendCurrentSceneFixedClass0Packet(receiver.session,
				currentTownUserAreaNotificationMsgID, areaBody, source+"_actual_area"); err != nil {
				return err
			}
			s.logInfo("dnfbridge fiendwar formation actor prepared",
				"kind", "game-fiendwar-formation-actor-prepared",
				"conn_id", receiver.session.connID, "party_id", snapshot.ID,
				"receiver_char_id", receiver.character, "actor_char_id", actor.CharacterID,
				"receiver_generation", receiver.generation, "actor_generation", identities[j].generation,
				"owner_channel", currentTownRemoteActorOwnerContext(receiver.session, actor.CharacterID),
				"actor_town", actor.TownID, "actor_area", actor.AreaID,
				"party_frame", false, "event_phase", "send_calls_returned")
		}
	}
	for _, identity := range identities {
		if !s.boundGameSessionCharacterCurrent(identity) || (identity.character != snapshot.Leader &&
			!s.currentFiendWarAdmissionAllowsPeer(leader, identity.character, int(snapshot.ID), dungeonID)) {
			return fmt.Errorf("fiendwar formation actor admission changed after preparation")
		}
	}
	return nil
}
