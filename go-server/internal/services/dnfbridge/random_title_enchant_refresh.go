package dnfbridge

import (
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"strconv"
)

func (r *csharpLegacyUserInfoReader) ownsCurrentTitleEnchantProjection() bool {
	return r != nil && r.session != nil && r.session.selectedCharacterID != 0 && r.characterID == strconv.Itoa(int(r.session.selectedCharacterID))
}
func (r *csharpLegacyUserInfoReader) resetCurrentRandomTitleEnchantRow() {
	if !r.ownsCurrentTitleEnchantProjection() {
		return
	}
	r.session.pendingTitleEnchantCharacterID = r.session.selectedCharacterID
	r.session.pendingTitleEnchantRow = nil
}
func (r *csharpLegacyUserInfoReader) noteCurrentRandomTitleEnchantRow(row currentItemListEntry) {
	if !r.ownsCurrentTitleEnchantProjection() || binary.LittleEndian.Uint16(row.data[:2]) != 13 || !currentItemListEntryCarriesRandomTitleEnchant(row) {
		return
	}
	copy := row
	r.session.pendingTitleEnchantCharacterID = r.session.selectedCharacterID
	r.session.pendingTitleEnchantRow = &copy
}

// Replay the same durable equipment snapshot used for mode1 creation. Consume
// it once, scoped to the selected character. Ordinary actors need no extra read.
func (s *Service) sendSelectedWornTitleEnchantRowRefresh(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 || session.pendingTitleEnchantCharacterID != session.selectedCharacterID || session.pendingTitleEnchantRow == nil {
		return nil
	}
	row := *session.pendingTitleEnchantRow
	if binary.LittleEndian.Uint16(row.data[:2]) != 13 || !currentItemListEntryCarriesRandomTitleEnchant(row) {
		return nil
	}
	body := buildCurrentItemUpdateBody(currentSocketListEquipment, []currentItemListEntry{row})
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0); err != nil {
		return err
	}
	session.pendingTitleEnchantRow = nil
	s.logGameEvent(session, "game-current-worn-title-enchant-row-refresh", "source", source, "item_id", binary.LittleEndian.Uint32(row.data[2:6]), "actor_slot", 13)
	return nil
}
