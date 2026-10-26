package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfemotion "longheng.io/server/internal/modules/dnf/emotion"
)

// currentChangeEmotionOpcode is CMD 254 (0x00FE): change emotion/mood.
const currentChangeEmotionOpcode uint16 = uint16(dnfenum.CmdPacketChangeEmotion)

func (s *Service) handleCurrentChangeEmotion(session *gameSession, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if len(body) < 2 {
		s.logGameEvent(session, "game-change-emotion-blocked", "body_len", len(body), "reason", "body_too_short")
		return nil
	}
	emotionIndex := binary.LittleEndian.Uint16(body[0:2])
	selectedID := session.selectedCharacterID
	characterID := strconv.FormatUint(uint64(selectedID), 10)
	identity, bound := s.boundGameSessionCharacterSnapshot(session)

	// Persist emotion_index to character stats.
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.sendGameUpperFailure(session, currentChangeEmotionOpcode, 3)
	}
	owner, err := dnfemotion.NewOwner(repos)
	if err == nil {
		_, err = owner.Apply(ctx, dnfemotion.Command{
			AccountID:           s.accountIDForSession(session),
			SelectedCharacterID: selectedID,
			EmotionIndex:        emotionIndex,
		})
	}
	if session.selectedCharacterID != selectedID || (bound && !s.boundGameSessionCharacterCurrent(identity)) {
		return nil
	}
	if err != nil {
		s.logGameEvent(session, "game-change-emotion-save-failed", "char_id", characterID, "emotion_index", emotionIndex, "reason", err)
		return s.sendGameUpperFailure(session, currentChangeEmotionOpcode, 3)
	}

	s.logGameEvent(session, "game-change-emotion",
		"char_id", characterID,
		"emotion_index", emotionIndex)

	// V179 01FC9AF0 updates the selection window. The actor itself consumes
	// mood from the mode0 scene projection at 022BE190.
	var w packetWriter
	w.writeUint16(emotionIndex)
	if err := s.sendGameUpperSuccess(session, currentChangeEmotionOpcode, w.bytes()); err != nil {
		return err
	}
	invalidateCurrentCrossSceneAppearance(session)
	if bound {
		if err := s.refreshCurrentEmotionActors(ctx, identity); err != nil {
			s.logGameEvent(session, "game-change-emotion-refresh-failed", "char_id", characterID, "reason", err)
		}
	}
	return nil
}

func (s *Service) refreshCurrentEmotionActors(ctx context.Context, identity boundGameSessionCharacter) error {
	session := identity.session
	current := func() bool {
		return s.boundGameSessionCharacterCurrent(identity) &&
			session.townSceneReadyCharacterID == identity.character && !s.sessionHasActiveDungeonRuntime(session)
	}
	if !current() {
		return nil
	}
	sent, err := s.sendSelectedActorMode0AppearanceRefreshGuarded(session, "change_emotion", "saved_then_ack_then_mode0", current)
	if err != nil || !sent || s.onlinePlayers == nil {
		return err
	}
	charID, name, character, found := s.selectedCharacterForEnter(ctx, session)
	if !found || charID != identity.character || !current() {
		return nil
	}
	for _, peer := range s.onlinePlayers.PeersInSameArea(charID) {
		peerIdentity, ok := s.boundGameSessionCharacterSnapshot(peer.Session)
		if !ok || peerIdentity.character != peer.CharacterID || peer.Session.townSceneReadyCharacterID != peer.CharacterID {
			continue
		}
		owner := currentTownRemoteActorOwnerContext(peer.Session, charID)
		if owner == currentSceneObjectContext {
			continue
		}
		body, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, session, charID, name, character, true, owner)
		if err == nil && current() && s.boundGameSessionCharacterCurrent(peerIdentity) &&
			peer.Session.townSceneReadyCharacterID == peer.CharacterID && s.onlinePlayers.PeerInSameArea(charID, peer.CharacterID) {
			err = s.sendGameUpperRawClass(peer.Session, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0)
		}
		if err != nil {
			s.logGameEvent(session, "game-change-emotion-peer-refresh-failed", "peer_char_id", peer.CharacterID, "reason", err)
		}
	}
	return nil
}
