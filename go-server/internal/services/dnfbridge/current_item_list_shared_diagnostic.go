package dnfbridge

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentSharedInventoryWireDiagnostic struct {
	Slot      uint16
	ItemID    uint32
	Count     uint32
	RowSHA256 string
}

// Inspect the outgoing plaintext, not a second repository snapshot. op13 raw
// logging is suppressed; these bounded numeric rows let first selection and
// reselection be compared without exposing the entire inventory packet.
func currentSharedInventoryWireDiagnostics(body []byte) ([]currentSharedInventoryWireDiagnostic, bool) {
	if len(body) < 5 || body[0] != dnfrepo.MainInventoryListType {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint16(body[3:5]))
	if len(body) != 5+n*currentItemListEntryWireSize {
		return nil, false
	}
	rows := make([]currentSharedInventoryWireDiagnostic, 0, 12)
	seen := make(map[uint16]bool, 12)
	for pos := 5; pos < len(body); pos += currentItemListEntryWireSize {
		row := body[pos : pos+currentItemListEntryWireSize]
		slot := binary.LittleEndian.Uint16(row[:2])
		if !dnfrepo.IsAccountSharedInventorySlot(0, int16(slot)) {
			continue
		}
		if seen[slot] {
			return nil, false
		}
		seen[slot] = true
		digest := sha256.Sum256(row)
		rows = append(rows, currentSharedInventoryWireDiagnostic{Slot: slot, ItemID: binary.LittleEndian.Uint32(row[2:6]), Count: binary.LittleEndian.Uint32(row[6:10]), RowSHA256: hex.EncodeToString(digest[:])})
	}
	return rows, true
}

func (s *Service) logCurrentSharedInventoryPacketWritten(session *gameSession, packet []byte) {
	// Only the established op13 fixed16/zero-prefix envelope is supported.
	// Inspect after suppression/replay and a complete socket write, so a
	// prepared or suppressed packet cannot be mistaken for wire evidence.
	if len(packet) < 16 || packet[0] != 0 || binary.LittleEndian.Uint16(packet[1:3]) != 13 || int(binary.LittleEndian.Uint32(packet[3:7])) != len(packet) {
		return
	}
	// Bytes 7..10 carry the checksum; only bytes 11..15 are the zero prefix.
	for _, value := range packet[11:16] {
		if value != 0 {
			return
		}
	}
	body := packet[16:]
	if session == nil || len(body) == 0 || body[0] != dnfrepo.MainInventoryListType {
		return
	}
	rows, valid := currentSharedInventoryWireDiagnostics(body)
	digest := sha256.Sum256(body)
	s.logGameEvent(session, "game-current-shared-inventory-wire-sent",
		"source", "wire_core_after_socket_write", "char_id", session.selectedCharacterID, "msg_id", 13,
		"body_len", len(body), "body_sha256", hex.EncodeToString(digest[:]),
		"rows_valid", valid, "shared_rows", len(rows), "write_completed", true)
	for _, row := range rows {
		s.logGameEvent(session, "game-current-shared-inventory-wire-row",
			"source", "wire_core_after_socket_write", "char_id", session.selectedCharacterID, "slot", row.Slot,
			"item_id", row.ItemID, "count", row.Count, "row_sha256", row.RowSHA256)
	}
}
