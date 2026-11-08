package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
)

// The official client's 00994D80 emits CMD1497/u16 target. Its success
// callback 01FC0E10 reads the target followed by the native Buff block and
// opens window F2. The legacy enum's OutsideGameReward label is not this ABI.
const currentPeerBuffInspectCommand uint16 = 1497

func (s *Service) currentPeerInspectionPageAllowed(ctx context.Context, viewer, actor boundGameSessionCharacter) bool {
	if viewer.character == 0 || actor.character == 0 || viewer.character == actor.character ||
		!s.boundGameSessionCharacterCurrent(viewer) || !s.boundGameSessionCharacterCurrent(actor) ||
		currentTownPresenceChannelID(viewer.session) == 0 ||
		currentTownPresenceChannelID(viewer.session) != currentTownPresenceChannelID(actor.session) {
		return false
	}
	if s.sessionHasActiveDungeonRuntime(viewer.session) || s.sessionHasActiveDungeonRuntime(actor.session) {
		return s.currentDungeonPeerUserInfoAllowed(ctx, viewer.session, actor.session, viewer.character, actor.character)
	}
	return s.onlinePlayers != nil && s.onlinePlayers.PeerInSameArea(viewer.character, actor.character)
}

// Both requests contain only a scene character key, not an account or an
// arbitrary database identifier. The native Buff callback dereferences that
// scene actor, so do not answer for an offline or cross-scene cached profile.
func (s *Service) handleCurrentPeerInspectionPage(session *gameSession, body []byte, buff bool) error {
	if s.options.currentClientVersion != "official-24.9" || len(body) != 2 {
		return nil
	}
	viewer, viewerOK := s.boundGameSessionCharacterSnapshot(session)
	actor, actorOK := s.onlineGameSessionCharacterSnapshot(binary.LittleEndian.Uint16(body))
	if !viewerOK || !actorOK {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if !s.currentPeerInspectionPageAllowed(ctx, viewer, actor) {
		return nil
	}
	targetID := strconv.Itoa(int(actor.character))
	if buff {
		r := csharpLegacyUserInfoReader{ctx: ctx, service: s, session: actor.session, characterID: targetID}
		end := r.beginNativeBuffOwnedSerialization()
		defer end()
		state, rows, err := r.nativeBuffSwapUserInfoRows()
		if err != nil {
			s.logGameEvent(session, "game-peer-buff-inspect-unavailable", "target_char_id", actor.character, "reason", err)
			return nil
		}
		var w packetWriter
		w.writeUint16(actor.character)
		writeNativeBuffSwapRows(&w, state, rows)
		if !s.currentPeerInspectionPageAllowed(ctx, viewer, actor) {
			return nil
		}
		return s.sendGameUpperSuccess(session, currentPeerBuffInspectCommand, w.bytes())
	}
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Inventory == nil {
		return nil
	}
	inventory, found, err := repos.Inventory.Load(ctx, targetID)
	if err != nil {
		return err
	}
	// An empty book still needs all five category resets and the final open
	// marker. Otherwise the previous target's pages remain in the native cache.
	for category := int32(0); category < 5; category++ {
		if !s.currentPeerInspectionPageAllowed(ctx, viewer, actor) {
			return nil
		}
		var entries []currentTitleBookEntry
		if found {
			entries = currentTitleBookEntriesForCategory(inventory, category)
		}
		response := buildCurrentTitleBookListBody(actor.character, category, entries)
		response[0] = 1 // 02029D95: category4 + info_type1 + nonzero owner opens D4.
		if err := s.sendGameUpperRawClass(session, currentTitleBookMsgID, response, 0); err != nil {
			return err
		}
	}
	return nil
}
