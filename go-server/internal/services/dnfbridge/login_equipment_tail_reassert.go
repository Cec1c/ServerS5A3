package dnfbridge

// Mode1 creation does not project the 0x77 tail bytes. After the login
// equipment container, resend affected authoritative rows through existing op14.
// Native cold-login acceptance is required to verify the client presentation.

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	loginEquipmentTailReassertEnv = "DNFBRIDGE_LOGIN_EQUIPMENT_TAIL_REASSERT"

	loginEquipmentTailReassertOn  = "reassert"
	loginEquipmentTailReassertOff = "off"

	loginEquipmentTailBlockFirstByte = 0x57
	loginEquipmentTailBlockLastByte  = 0x59
)

func loginEquipmentTailReassertEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginEquipmentTailReassertEnv))) {
	case loginEquipmentTailReassertOff, "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

func loginEquipmentTailReassertSwitch() string {
	if loginEquipmentTailReassertEnabled() {
		return loginEquipmentTailReassertOn
	}
	return loginEquipmentTailReassertOff
}

func loginEquipmentRowTailBlock(entry currentItemListEntry) [3]byte {
	return [3]byte{
		entry.data[loginEquipmentTailBlockFirstByte],
		entry.data[loginEquipmentTailBlockFirstByte+1],
		entry.data[loginEquipmentTailBlockFirstByte+2],
	}
}

func loginEquipmentRowTailBlockNonZero(entry currentItemListEntry) bool {
	for _, value := range loginEquipmentRowTailBlock(entry) {
		if value != 0 {
			return true
		}
	}
	return false
}

func loginEquipmentRowsNeedingTailReassert(entries []currentItemListEntry) []currentItemListEntry {
	rows := make([]currentItemListEntry, 0, len(entries))
	for _, entry := range entries {
		if loginEquipmentRowTailBlockNonZero(entry) {
			rows = append(rows, entry)
		}
	}
	return rows
}

func loginEquipmentTailReassertRowSummary(rows []currentItemListEntry) string {
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		slot := binary.LittleEndian.Uint16(row.data[0x00:0x02])
		itemID := binary.LittleEndian.Uint32(row.data[0x02:0x06])
		instance := binary.LittleEndian.Uint32(row.data[0x06:0x0A])
		parts = append(parts, strconv.Itoa(int(slot))+":"+
			strconv.FormatUint(uint64(itemID), 10)+":"+
			strconv.FormatUint(uint64(instance), 10)+":forge="+
			strconv.Itoa(int(row.data[loginEquipmentTailBlockFirstByte])))
	}
	return strings.Join(parts, ";")
}

func (s *Service) reassertLoginEquipmentTailRows(session *gameSession, source string) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	if !loginEquipmentTailReassertEnabled() {
		s.logPacketEvent("game-login-equipment-tail-reassert-suppressed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"rows", 0,
			"switch", loginEquipmentTailReassertSwitch(),
			"switch_env", loginEquipmentTailReassertEnv,
			"reason", "rollback_literal_restores_the_container_only_login_delivery")
		return
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Equipment == nil {
		s.logPacketEvent("game-login-equipment-tail-reassert-skipped",
			"conn_id", session.connID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"reason", "equipment_repository_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	entries, known, err := currentItemListEntriesFromEquipment(ctx, repos.Equipment, characterID, repos.Pet)
	if err != nil {
		s.logWarn("dnfbridge login equipment tail reassert load failed",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"error", err)
		return
	}
	if !known {
		s.logPacketEvent("game-login-equipment-tail-reassert-skipped",
			"conn_id", session.connID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"reason", "equipment_snapshot_unavailable_no_invented_rows")
		return
	}
	rows := loginEquipmentRowsNeedingTailReassert(entries)
	if len(rows) == 0 {
		s.logPacketEvent("game-login-equipment-tail-reassert-skipped",
			"conn_id", session.connID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"equipment_row_count", len(entries),
			"rows", 0,
			"reason", "no_equipped_row_carries_a_tail_block")
		return
	}
	body := buildCurrentItemUpdateBody(currentSocketListEquipment, rows)
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0); err != nil {
		s.logWarn("dnfbridge login equipment tail reassert deferred",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"list_type", currentSocketListEquipment,
			"rows", len(rows),
			"error", err)
		return
	}
	s.logPacketEvent("game-login-equipment-tail-reassert-sent",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"list", currentSocketListEquipment,
		"rows", len(rows),
		"equipment_row_count", len(entries),
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"classification", 0,
		"list_type", currentSocketListEquipment,
		"body_len", len(body),
		"entry_size", currentItemListEntryWireSize,
		"tail_block_offset", loginEquipmentTailBlockFirstByte,
		"row_summary", loginEquipmentTailReassertRowSummary(rows),
		"switch", loginEquipmentTailReassertSwitch(),
		"switch_env", loginEquipmentTailReassertEnv,
		"evidence", "mode1_create_row_field_sequence_sub_1D77560_has_no_0x57_slot_detail_so_cold_login_needs_the_row_carrier")

}
