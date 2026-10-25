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
