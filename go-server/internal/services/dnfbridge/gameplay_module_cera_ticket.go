package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// Current signed DNF.exe SHA256 bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531:
// op65 is the observed recharge request, with an empty body. Evidence lives in
// current-client-contracts/cera-ticket-proof04 (P56 复核见 p56-cashshop-close-532).
// The sender at 0x237a070 calls BeginWrite(65) at 0x237a0b5 and flushes at
// 0x237a0ba without writing fields.
//
// COMMAND handler 0x1fbb540 checks the success flag. On success, 0x3752120
// (called at 0x1fbb58f, cap 0x40 pushed at 0x1fbb58c) calls 0x3752050, which
// reads a u32 byte length, then that many string bytes when 0 < length < cap.
// A zero length uses the client's local fallback 0x4933b68 (ERROR!ERROR!).
// The handler then reads a u32 ticket at 0x1fbb59b and passes the string/ticket
// to 0x194cb50 without testing ticket. Thus the complete minimal success body is
// 01 | u32(0) | u32(0), nine bytes. No server URL is supplied by this fallback.
// This proves packet consumption, not GUI recovery.
// On failure the handler jumps directly to 0x1fbb5c3 and reads no body fields.
//
// P56 (2026-10-13): the URL slot is real packet data, not a fixed local
// constant — only length==0 falls back. So the same 9-byte layout with a
// non-zero, ASCII, length<0x40 URL is the user-authorised "blank page"
// (about:blank) variant. The old zero-length reply left the embedded
// Awesomium view pointed at ERROR!ERROR!, a page that can never load.
const currentCeraTicketAckSize = 9

// currentCeraTicketStringCap mirrors the cap pushed at 0x1fbb58c.
const currentCeraTicketStringCap = 0x40

// currentCeraTicketURL is the page address carried in the op65 success body.
// Default blank page (pure ASCII, 11 bytes < cap); set to "" to fall back to
// the client's local constant 0x4933b68.
var currentCeraTicketURL = "about:blank"

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

// currentCeraTicketAckURL applies the shared cap/ASCII filter (see
// gameplay_module_web_viewer.go) to the configured recharge page address.
func currentCeraTicketAckURL() string {
	return currentWebTicketURLWithinCap(currentCeraTicketURL, currentCeraTicketStringCap)
}

func buildCurrentCeraTicketAck(url string) []byte {
	var body packetWriter
	body.writeByte(1)
	body.writeUint32(uint32(len(url))) // 0 => the EXE's local fallback.
	if len(url) > 0 {
		body.writeBytes([]byte(url))
	}
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
	url := currentCeraTicketAckURL()
	reply := buildCurrentCeraTicketAck(url)
	s.logGameEvent(session, "game-cera-ticket-ack",
		"msg_id", opcode,
		"body_len", len(body),
		"ack_body_len", len(reply),
		"string_byte_length", len(url),
		"page_url", url,
		"ticket", 0,
		"ack_shape", "current_exe_op65_success_u32_string_length_string_u32_ticket",
		"gui_status", "pending_field_verification")
	return s.sendGameUpperRawClass(session, opcode, reply, dnfproto.DefaultChannelClassification)
}
