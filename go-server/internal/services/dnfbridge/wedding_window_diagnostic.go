package dnfbridge

import "encoding/binary"

// Only the two result routes can open the native congratulations dialog;
// 1004 can update an already open one. Record each actual socket write so a
// duplicate result and a later body refresh can be distinguished without
// exposing names/account IDs or requiring full packet logging.
func (s *Service) logCurrentWeddingWindowWire(session *gameSession, opcode uint16, class byte, sequence uint16, body []byte, sent bool) {
	if session == nil || !((class == 0 && (opcode == 998 || opcode == 1004)) || (class == 1 && opcode == 1033)) {
		return
	}
	identity, _ := s.boundGameSessionCharacterSnapshot(session)
	fields := []any{"event", "game-wedding-window-wire", "char_id", identity.character, "generation", identity.generation,
		"opcode", opcode, "classification", class, "sequence", sequence, "body_len", len(body), "sent", sent}
	if opcode == 1004 {
		if len(body) >= 4 {
			fields = append(fields, "wedding_key", binary.LittleEndian.Uint32(body))
		}
	} else {
		if class == 1 && len(body) > 0 {
			body = body[1:]
		}
		if len(body) == 12 {
			fields = append(fields, "result", int32(binary.LittleEndian.Uint32(body)), "op", body[8], "kind", body[9], "duration", body[10], "answer", body[11])
		}
	}
	s.logInfo("dnfbridge wedding window wire diagnostic", fields...)
}
