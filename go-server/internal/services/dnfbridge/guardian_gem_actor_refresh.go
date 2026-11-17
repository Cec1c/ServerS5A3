package dnfbridge

import (
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func (r *csharpLegacyUserInfoReader) ownsCurrentGuardianGemProjection() bool {
	return r != nil && r.session != nil && r.session.selectedCharacterID != 0 && r.characterID == strconv.Itoa(int(r.session.selectedCharacterID))
}

func (r *csharpLegacyUserInfoReader) resetCurrentGuardianGemRow() {
	if !r.ownsCurrentGuardianGemProjection() {
		return
	}
	r.session.pendingGuardianGemCharacterID = r.session.selectedCharacterID
	r.session.pendingGuardianGemRow = nil
}

func (r *csharpLegacyUserInfoReader) noteCurrentGuardianGemRow(row currentItemListEntry) {
	if !r.ownsCurrentGuardianGemProjection() || binary.LittleEndian.Uint16(row.data[:2]) != uint16(currentGuildMedalActorSlot) || !currentGuardianGemRawSocketOccupied(row.data[:]) {
		return
	}
	copy := row
	r.session.pendingGuardianGemCharacterID = r.session.selectedCharacterID
	r.session.pendingGuardianGemRow = &copy
}

// The full mode1 builder already read the durable equipment snapshot. Replay
// that exact medal row once, after creation; do not reload unrelated containers.
func (s *Service) sendSelectedActorGuardianGemRowRefresh(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 || session.pendingGuardianGemCharacterID != session.selectedCharacterID || session.pendingGuardianGemRow == nil {
		return nil
	}
	row := *session.pendingGuardianGemRow
	if binary.LittleEndian.Uint16(row.data[:2]) != uint16(currentGuildMedalActorSlot) || !currentGuardianGemRawSocketOccupied(row.data[:]) {
		return nil
	}
	body := buildCurrentItemUpdateBody(currentSocketListEquipment, []currentItemListEntry{row})
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0); err != nil {
		return err
	}
	session.pendingGuardianGemRow = nil
	s.logGameEvent(session, "game-current-guardian-gem-actor-row-refresh", "source", source, "item_id", binary.LittleEndian.Uint32(row.data[2:6]), "actor_slot", currentGuildMedalActorSlot)
	return nil
}

// deferSelectedActorGuardianGemRowRefreshUntilFinishLoading keeps the exact
// mode1-generation medal row alive through dungeon room activation. The
// current client applies the worn-equipment object only after the
// op120/op3/op37/op30/op19 entry sequence, so replaying list-3 op14 earlier can
// be overwritten by that activation. Other full actor rebuilds continue to use
// sendSelectedActorGuardianGemRowRefresh immediately.
func (s *Service) deferSelectedActorGuardianGemRowRefreshUntilFinishLoading(session *gameSession, source string) {
	if session == nil || session.selectedCharacterID == 0 ||
		session.pendingGuardianGemCharacterID != session.selectedCharacterID ||
		session.pendingGuardianGemRow == nil {
		if session != nil {
			session.pendingGuardianGemAwaitFinishLoading = false
		}
		return
	}
	row := *session.pendingGuardianGemRow
	if binary.LittleEndian.Uint16(row.data[:2]) != uint16(currentGuildMedalActorSlot) ||
		!currentGuardianGemRawSocketOccupied(row.data[:]) {
		session.pendingGuardianGemAwaitFinishLoading = false
		return
	}
	session.pendingGuardianGemAwaitFinishLoading = true
	if s != nil {
		s.logGameEvent(session, "game-current-guardian-gem-actor-row-refresh-deferred",
			"source", source,
			"item_id", binary.LittleEndian.Uint32(row.data[2:6]),
			"actor_slot", currentGuildMedalActorSlot,
			"deferred_until", "dungeon_finish_loading_after_op19_and_op120",
			"reason", "room_activation_precedes_the_current_client_equipment_apply_boundary")
	}
}

func (s *Service) sendDeferredSelectedActorGuardianGemRowRefresh(session *gameSession, source string) error {
	if session == nil || !session.pendingGuardianGemAwaitFinishLoading {
		return nil
	}
	if session.selectedCharacterID == 0 ||
		session.pendingGuardianGemCharacterID != session.selectedCharacterID ||
		session.pendingGuardianGemRow == nil {
		session.pendingGuardianGemAwaitFinishLoading = false
		return nil
	}
	if err := s.sendSelectedActorGuardianGemRowRefresh(session, source); err != nil {
		// The exact mode1-generation row and the deferred marker survive a
		// failed write so a later finish-loading retry can resend it.
		return err
	}
	session.pendingGuardianGemAwaitFinishLoading = false
	return nil
}

func clearDeferredSelectedActorGuardianGemRowRefresh(session *gameSession) {
	if session != nil {
		session.pendingGuardianGemAwaitFinishLoading = false
	}
}
