package dnfbridge

import "encoding/binary"

// Record the successful wire boundary for a barrier edge or a restored edge
// marker. This is delivery evidence, not evidence of client animation or damage.
func (s *Service) traceCurrentAntonBarrierProjection(session *gameSession, b *runtimeRaidBattle, packet currentRaidProjectionPacket, fresh bool, previousCursor uint64) {
	if b == nil || b.Mode != 1 || b.Single || b.Anton == nil || packet.opcode != 583 || len(packet.body) == 0 {
		return
	}
	count := int(packet.body[0])
	if len(packet.body) != 1+count*8 {
		return
	}
	zeroIndex, breakIndex, recoverIndex, openIndex, closedIndex := -1, -1, -1, -1, -1
	for i := 0; i < count; i++ {
		offset := 1 + i*8
		id := binary.LittleEndian.Uint32(packet.body[offset : offset+4])
		value := binary.LittleEndian.Uint32(packet.body[offset+4 : offset+8])
		switch {
		case id == 110 && value == 0:
			zeroIndex = i
		case id == 126 && value == 1:
			breakIndex = i
		case id == 127 && value == 1:
			recoverIndex = i
		case id == 111 && value == 1:
			openIndex = i
		case id == 111 && value == 0:
			closedIndex = i
		}
	}
	if zeroIndex < 0 && breakIndex < 0 && recoverIndex < 0 && openIndex < 0 && closedIndex < 0 {
		return
	}
	s.logGameEvent(session, "game-raid-volcano-barrier-symbols-sent",
		"instance_id", b.InstanceID, "phase", b.Phase,
		"projection_version", b.ProgressVersion, "projection_fresh", fresh,
		"symbol_cursor_before", previousCursor, "symbol_cursor_after", packet.symbolCursor,
		"zero_write_index", zeroIndex, "break_write_index", breakIndex,
		"recover_write_index", recoverIndex,
		"native_open_write_index", openIndex, "native_closed_write_index", closedIndex,
		"barrier_current", b.Anton.NativeSymbols()[110],
		"native_open_current", b.Anton.NativeSymbols()[111])
}
