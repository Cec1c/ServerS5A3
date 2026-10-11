package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// Current signed DNF.exe SHA256 bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531:
// op65 is the observed recharge request, with an empty body. Evidence lives in
// current-client-contracts/cera-ticket-proof04. The sender at 0x237a070 calls
// BeginWrite(65) at 0x237a0b5 and flushes at 0x237a0ba without writing fields.
//
// COMMAND handler 0x1fbb540 checks the success flag. On success, 0x3752120
// (called at 0x1fbb58f) calls 0x3752050, which reads a u32 byte length, then
// that many string bytes when 0 < length < 64. A zero length uses the client's
// local fallback 0x4933b68 (ERROR!ERROR!). The handler then reads a u32 ticket
// at 0x1fbb59b and passes the string/ticket to 0x194cb50 without testing ticket.
// Thus the complete minimal success body is 01 | u32(0) | u32(0), nine bytes.
// No server URL is supplied. This proves packet consumption, not GUI recovery.
// On failure the handler jumps directly to 0x1fbb5c3 and reads no body fields.
const currentCeraTicketAckSize = 9

// currentCeraTicketGameplayHandler can be added to webViewerGameplayModule's
// UpperHandlers under uint16(dnfenum.CmdPacketGenCeraticket). Alternatively,
// register ceraTicketGameplayModule once. This file does not register itself.
func currentCeraTicketGameplayHandler() gameplayHandler {
	return defaultClassGameplayHandler(
		"game-cera-ticket-blocked",
		"current_exe_op65_command_class_mismatch",
		(*Service).handleCurrentCeraTicket,
	)
}

func ceraTicketGameplayModule() gameplayModuleDefinition {
	return gameplayModuleDefinition{
		Name: "cera-ticket",
		UpperHandlers: map[uint16]gameplayHandler{
			uint16(dnfenum.CmdPacketGenCeraticket): currentCeraTicketGameplayHandler(),
		},
	}
}

func buildCurrentCeraTicketAck() []byte {
	var body packetWriter
	body.writeByte(1)
	body.writeUint32(0) // Empty string: use the EXE's local fallback.
	body.writeUint32(0) // Layout-safe ticket scalar; no payment ticket is issued.
	return body.bytes()
}

func (s *Service) handleCurrentCeraTicket(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketGenCeraticket)
	if len(body) != 0 {
		s.logGameEvent(session, "game-cera-ticket-rejected",
			"msg_id", opcode,
			"body_len", len(body),
			"reason", "unproven_current_exe_request_size")
		// Zero is only the existing failure-envelope placeholder: the EXE's
		// op65 failure branch does not inspect either body or error-code arg.
		return s.sendGameUpperFailure(session, opcode, 0)
	}
	reply := buildCurrentCeraTicketAck()
	s.logGameEvent(session, "game-cera-ticket-ack",
		"msg_id", opcode,
		"body_len", len(body),
		"ack_body_len", len(reply),
		"string_byte_length", 0,
		"ticket", 0,
		"ack_shape", "current_exe_op65_success_u32_string_length_string_u32_ticket",
		"gui_status", "pending_field_verification")
	return s.sendGameUpperRawClass(session, opcode, reply, dnfproto.DefaultChannelClassification)
}
