package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// Requires the paired current-EXE native same-area visual creator. Default
// remains off; public deployment must first deliver that client capability.
const townDisplayOnlyEnv = "DNFBRIDGE_TOWN_DISPLAY_ONLY"

func townDisplayOnlyEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(townDisplayOnlyEnv))) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

func (s *Service) currentTownDisplayOnlyProjection(target *gameSession, actor onlinePlayerInfo, source string) bool {
	if !townDisplayOnlyEnabled() || s.options.currentClientVersion != "official-24.9" || s.onlinePlayers == nil ||
		target == nil || actor.Session == nil || actor.CharacterID == target.selectedCharacterID {
		return false
	}
	switch source {
	case "town_copresence_actor_state_newcomer_to_existing", "town_copresence_actor_state_existing_to_newcomer",
		"town_copresence_actor_state_after_finish_loading":
	default:
		return false
	}
	observerIdentity, observerCurrent := s.boundGameSessionCharacterSnapshot(target)
	actorIdentity, actorCurrent := s.boundGameSessionCharacterSnapshot(actor.Session)
	if !observerCurrent || !actorCurrent || observerIdentity.character != target.selectedCharacterID ||
		actorIdentity.character != actor.CharacterID {
		return false
	}
	observer, found := s.onlinePlayers.PlayerForCharacter(target.selectedCharacterID)
	currentActor, present := s.onlinePlayers.PlayerForCharacter(actor.CharacterID)
	// Presence can retain a same-character auxiliary transport. The loader has
	// already selected the current directory owner; compare that owner's full
	// channel with the area record, not the cached transport pointer.
	if !found || !present || observer.Session == nil || currentActor.Session == nil ||
		observer.ChannelID == 0 || observer.ChannelID != currentActor.ChannelID ||
		currentTownPresenceChannelID(target) != observer.ChannelID ||
		currentTownPresenceChannelID(actor.Session) != currentActor.ChannelID ||
		observer.Instance != 0 || currentActor.Instance != 0 ||
		observer.RaidBattleInstance != "" || currentActor.RaidBattleInstance != "" ||
		!s.onlinePlayers.PeerInSameArea(actor.CharacterID, target.selectedCharacterID) {
		return false
	}
	// A real retained singleton lobby is not a multiplayer actor. Keep its
	// authoritative op9 row and invitation/join semantics while avoiding the
	// full equipment/skill initialization for this ordinary town projection.
	return townDisplayOnlySoloState(s.townCopresenceActorPartyState(target), target.selectedCharacterID) &&
		townDisplayOnlySoloState(s.townCopresenceActorPartyState(actor.Session), actor.CharacterID) &&
		s.boundGameSessionCharacterCurrent(observerIdentity) && s.boundGameSessionCharacterCurrent(actorIdentity)
}

func townDisplayOnlySoloState(state alignedcmd.PartyState, characterID uint16) bool {
	if state.PartyID == 0 {
		return true
	}
	if state.PartyID < 0 || characterID == 0 || state.UserID != characterID {
		return false
	}
	members := runtimePartyMembers(state)
	return len(members) == 1 && members[0].UserID == characterID
}

func (p *gameSession) rememberTownDisplayPeer(identity boundGameSessionCharacter) {
	if identity.session == nil || identity.character == 0 {
		return
	}
	p.townDisplayMu.Lock()
	defer p.townDisplayMu.Unlock()
	if p.townDisplayPeers == nil {
		p.townDisplayPeers = make(map[uint16]boundGameSessionCharacter)
	}
	p.townDisplayPeers[identity.character] = identity
}

func (p *gameSession) forgetTownDisplayPeer(id uint16) {
	p.townDisplayMu.Lock()
	defer p.townDisplayMu.Unlock()
	delete(p.townDisplayPeers, id)
}

func (p *gameSession) clearTownDisplayPeers() {
	p.townDisplayMu.Lock()
	defer p.townDisplayMu.Unlock()
	p.townDisplayPeers = nil
}

// All repository and wire work happens after taking a bounded snapshot of the
// party's own members. It never recursively emits an op9 frame. No promotion
// may bind a display-only actor as a full party actor by accident.
func (s *Service) upgradeTownDisplayPeersForParty(target *gameSession, state alignedcmd.PartyState) error {
	if state.PartyID <= 0 || target == nil || s.onlinePlayers == nil {
		return nil
	}
	members := runtimePartyMembers(state)
	// A singleton's kind0 record is still sent, but must not immediately undo
	// the display-only decision with a recursive full mode1 projection.
	if len(members) < 2 {
		return nil
	}
	var identities []boundGameSessionCharacter
	target.townDisplayMu.Lock()
	for _, member := range members {
		if identity, ok := target.townDisplayPeers[member.UserID]; ok {
			identities = append(identities, identity)
		}
	}
	target.townDisplayMu.Unlock()
	for _, identity := range identities {
		if !s.boundGameSessionCharacterCurrent(identity) ||
			!s.onlinePlayers.PeerInSameArea(identity.character, target.selectedCharacterID) {
			target.forgetTownDisplayPeer(identity.character)
			continue
		}
		actor, found := s.onlinePlayers.PlayerForCharacter(identity.character)
		if !found || actor.ChannelID != currentTownPresenceChannelID(identity.session) {
			continue
		}
		// Use the same canonical actor for promotion even when the area record
		// still belongs to its auxiliary transport.
		actor.Session = identity.session
		if err := s.sendTownRemoteActorStateWithPartyFrame(target, actor, "town_display_only_party_upgrade", false); err != nil {
			return err
		}
	}
	return nil
}
