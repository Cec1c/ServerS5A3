package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/buffswap"
)

// The official client's 00994D80 emits CMD1497/u16 target. Its success
// callback 01FC0E10 reads the target followed by the native Buff block and
// opens window F2. The legacy enum's OutsideGameReward label is not this ABI.
const currentPeerBuffInspectCommand uint16 = 1497

type peerBuffInspectionCursor struct {
	viewerCharacter  uint16
	viewerGeneration uint64
	targetCharacter  uint16
	targetGeneration uint64
	skill            uint16
}

func peerBuffInspectionSkillIndex(skills []buffswap.OwnedRegisteredSkill, skill uint16) int {
	for index, registration := range skills {
		if registration.Skill == skill {
			return index
		}
	}
	return -1
}

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
		session.peerBuffInspectMu.Lock()
		defer session.peerBuffInspectMu.Unlock()

		r := csharpLegacyUserInfoReader{ctx: ctx, service: s, session: actor.session, characterID: targetID}
		end := r.beginNativeBuffOwnedSerialization()
		defer end()
		projection, err := r.loadNativeBuffOwnedProjection()
		if err != nil {
			s.logGameEvent(session, "game-peer-buff-inspect-unavailable", "target_char_id", actor.character, "reason", err)
			return nil
		}
		state := projection.State
		items := projection.Items
		pageCount := len(projection.RegisteredSkills)
		pageIndex := -1
		if pageCount != 0 {
			// The native 1497 response stays single-skill. A repeated click by
			// the same viewer for the same target reopens F2 on the next stable
			// registration row; the cursor commits only after a successful write.
			cursor := session.peerBuffInspectCursor
			if cursor.viewerCharacter == viewer.character &&
				cursor.viewerGeneration == viewer.generation &&
				cursor.targetCharacter == actor.character &&
				cursor.targetGeneration == actor.generation {
				if prior := peerBuffInspectionSkillIndex(projection.RegisteredSkills, cursor.skill); prior >= 0 {
					pageIndex = (prior + 1) % pageCount
				}
			}
			if pageIndex < 0 {
				pageIndex = peerBuffInspectionSkillIndex(projection.RegisteredSkills, state.Skill)
				if pageIndex < 0 {
					pageIndex = 0
				}
			}
			registration := projection.RegisteredSkills[pageIndex]
			state.Skill = registration.Skill
			items = registration.Items
		}
		rows := r.nativeBuffSwapProjectionRows(projection, items)
		var w packetWriter
		w.writeUint16(actor.character)
		writeNativeBuffSwapRows(&w, state, rows)
		if !s.currentPeerInspectionPageAllowed(ctx, viewer, actor) {
			return nil
		}
		if err := s.sendGameUpperSuccess(session, currentPeerBuffInspectCommand, w.bytes()); err != nil {
			return err
		}
		if pageCount != 0 {
			session.peerBuffInspectCursor = peerBuffInspectionCursor{
				viewerCharacter:  viewer.character,
				viewerGeneration: viewer.generation,
				targetCharacter:  actor.character,
				targetGeneration: actor.generation,
				skill:            state.Skill,
			}
		}
		s.logGameEvent(session, "game-peer-buff-inspect-page",
			"source_char_id", viewer.character,
			"target_char_id", actor.character,
			"skill", state.Skill,
			"page_index", pageIndex,
			"page_count", pageCount,
			"rows", len(rows))
		return nil
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
