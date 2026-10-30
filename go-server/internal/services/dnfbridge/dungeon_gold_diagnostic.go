package dnfbridge

import "encoding/binary"

// Off by default. A selected character gets at most 256 small events per
// connection; neither raw packets nor account/authentication data are retained.
const currentGoldDiagnosticLimit = 256

// currentGoldDiagnosticSnapshot is an immutable pair. Selection and cleanup
// replace the pointer atomically, so wire logging never reads mutable session
// identity fields while wireMu is held.
type currentGoldDiagnosticSnapshot struct {
	characterID uint16
	channelID   int
}

func storeGoldDiagnosticSnapshot(session *gameSession, characterID uint16, channelID int) {
	if session == nil {
		return
	}
	session.goldDiagnosticSnapshot.Store(&currentGoldDiagnosticSnapshot{
		characterID: characterID,
		channelID:   channelID,
	})
}

func (s *Service) installGoldDiagnosticSnapshot(session *gameSession) {
	if s == nil || session == nil || s.options.goldDiagnosticCharID <= 0 {
		return
	}
	storeGoldDiagnosticSnapshot(session, session.selectedCharacterID, session.channel.ID)
}

func (s *Service) clearGoldDiagnosticSnapshot(session *gameSession) {
	if session == nil {
		return
	}
	session.goldDiagnosticSnapshot.Store(nil)
}

func (s *Service) takeGoldDiagnostic(session *gameSession) (*currentGoldDiagnosticSnapshot, bool) {
	if s == nil || session == nil || s.options.goldDiagnosticCharID <= 0 {
		return nil, false
	}
	snapshot := session.goldDiagnosticSnapshot.Load()
	if snapshot == nil || snapshot.characterID == 0 ||
		s.options.goldDiagnosticCharID != int(snapshot.characterID) {
		return nil, false
	}
	for {
		n := session.goldDiagnosticCount.Load()
		if n >= currentGoldDiagnosticLimit {
			return nil, false
		}
		if session.goldDiagnosticCount.CompareAndSwap(n, n+1) {
			return snapshot, true
		}
	}
}

// Called after the socket write, while wireMu is held. Do not take dungeon.mu:
// gameplay senders can already hold it. Parse only the immutable plain body.
func (s *Service) logGoldDiagnosticPacketResult(session *gameSession, packet []byte, writeFailed bool) {
	header := s.gameUpperHeaderSize()
	if len(packet) < header || int(binary.LittleEndian.Uint32(packet[3:7])) != len(packet) {
		return
	}
	s.logGoldDiagnosticWire(session, binary.LittleEndian.Uint16(packet[1:3]), packet[0], binary.LittleEndian.Uint16(packet[11:13]), packet[header:], writeFailed)
}

func (s *Service) logGoldDiagnosticWire(session *gameSession, opcode uint16, classification byte, sequence uint16, body []byte, writeFailed bool) {
	if classification != 0 || (opcode != 38 && opcode != 29) {
		return
	}
	snapshot, ok := s.takeGoldDiagnostic(session)
	if !ok {
		return
	}
	args := []any{"conn_id", session.connID, "channel_id", snapshot.channelID,
		"char_id", snapshot.characterID, "opcode", opcode, "sequence", sequence,
		"body_len", len(body), "body_digest", currentProjectionBodyDigest(body), "write_failed", writeFailed}
	if opcode == 38 && len(body) >= 3 {
		count := int(body[2])
		end := 3 + count*currentDungeonDeathDropWireSize
		args = append(args, "target_key", binary.LittleEndian.Uint16(body), "drop_count", count, "rows_complete", end <= len(body))
		if end <= len(body) {
			rows := make([]map[string]any, 0, count)
			for i := 0; i < count; i++ {
				r := body[3+i*currentDungeonDeathDropWireSize : 3+(i+1)*currentDungeonDeathDropWireSize]
				item := binary.LittleEndian.Uint32(r[6:10])
				if item != 0 {
					continue
				}
				amount := binary.LittleEndian.Uint32(r[10:14])
				rows = append(rows, map[string]any{"row": i, "object_key": binary.LittleEndian.Uint32(r),
					"scene_slot": binary.LittleEndian.Uint16(r[4:6]), "amount": amount, "signed_amount": int32(amount),
					"owner_key": binary.LittleEndian.Uint16(r[len(r)-2:])})
			}
			args = append(args, "gold_rows", rows)
		}
	}
	s.logInfo("dnfbridge gold wire diagnostic", args...)
}

func (s *Service) logGoldDiagnosticPickup(session *gameSession, kind string, fields ...any) {
	switch kind {
	case "game-dungeon-pickup-received", "game-dungeon-pickup-blocked", "game-dungeon-pickup-accepted", "game-dungeon-pickup-response-blocked-after-commit",
		"game-dungeon-pickup-replayed", "game-dungeon-pickup-scene-slot-fallback":
	default:
		return
	}
	snapshot, ok := s.takeGoldDiagnostic(session)
	if !ok {
		return
	}
	args := []any{"kind", kind, "conn_id", session.connID, "channel_id", snapshot.channelID, "char_id", snapshot.characterID}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		switch key {
		case "body_len", "dungeon_id", "map_id", "drop_object_key", "scene_slot", "item_id", "amount",
			"gold_after", "picker_actor_object_key", "destination_slot", "response_opcode", "response_body_len":
			switch fields[i+1].(type) {
			case uint, uint8, uint16, uint32, uint64, int, int8, int16, int32, int64:
				args = append(args, key, fields[i+1])
			}
		case "error":
			args = append(args, "failed", fields[i+1] != nil)
		case "reason":
			if value, ok := fields[i+1].(string); ok {
				switch value {
				case "current_exe_op43_ordinary_request_malformed", "current_exe_op43_fixed_zero_mismatch",
					"dungeon_drop_runtime_missing", "dungeon_scene_missing", "runtime_drop_owner_rejected",
					"consumed_drop_response_missing", "runtime_drop_state_invalid", "character_gold_transaction_missing",
					"character_gold_transaction_failed_drop_remains_available", "gold_pickup_response_invariant_failed_drop_consumed_to_prevent_duplicate_grant":
					args = append(args, key, value)
				}
			}
		}
	}
	s.logInfo("dnfbridge gold pickup diagnostic", args...)
}
