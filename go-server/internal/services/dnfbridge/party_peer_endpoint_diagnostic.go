package dnfbridge

import (
	"encoding/binary"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// Bounded party peer endpoint diagnostics for the reported FiendWar case:
// one client can send UDP but its peer never emits a first datagram. Packet
// logging is off by default, so these events are emitted through the normal
// application logger and deliberately contain no raw addresses, accounts,
// tokens, error strings or packet bytes.
const (
	partyPeerEndpointDiagnosticKind             = "game-party-peer-endpoint-diagnostic"
	partyPeerEndpointRegistrationDiagnosticKind = "game-party-peer-endpoint-registration-diagnostic"
	partyPeerEndpointDiagnosticMaxRows          = 4
)

// partyPeerEndpointStage identifies the real op11 send point. Keeping the
// stage explicit prevents a roster repeat or dungeon-entry remotes send from
// being mislabeled as the initial formation phase one send.
type partyPeerEndpointStage string

const (
	partyPeerEndpointStageInitial             partyPeerEndpointStage = "before_roster"
	partyPeerEndpointStageAfterRosterRepeat   partyPeerEndpointStage = "after_roster_repeat"
	partyPeerEndpointStageDungeonEntryRemotes partyPeerEndpointStage = "dungeon_entry_remotes"
)

func (stage partyPeerEndpointStage) source() string {
	switch stage {
	case partyPeerEndpointStageAfterRosterRepeat:
		return "party_formation_after_roster_repeat"
	case partyPeerEndpointStageDungeonEntryRemotes:
		return "dungeon_entry_remotes"
	default:
		return "party_snapshot_before_roster"
	}
}

type partyPeerEndpointDiagnosticRow struct {
	peerID             uint16
	inner              [4]byte
	outer              [4]byte
	port               uint16
	nat                byte
	mtu                uint32
	characterAttribute byte
}

type partyPeerEndpointDiagnosticBody struct {
	declaredPeerCount int
	rows              []partyPeerEndpointDiagnosticRow
	truncated         bool
	parseOK           bool
	parseReason       string
}

// parsePartyPeerEndpointDiagnosticBody reads the already-built op11 body. It
// parses at most four 22-byte rows so a malformed or unexpectedly large body
// cannot expand the diagnostic without bound. The account field inside each
// row is skipped intentionally.
func parsePartyPeerEndpointDiagnosticBody(body []byte) partyPeerEndpointDiagnosticBody {
	result := partyPeerEndpointDiagnosticBody{}
	if len(body) == 0 {
		result.parseReason = "empty_body"
		return result
	}
	result.declaredPeerCount = int(body[0])
	rowCount := result.declaredPeerCount
	if rowCount > partyPeerEndpointDiagnosticMaxRows {
		rowCount = partyPeerEndpointDiagnosticMaxRows
		result.truncated = true
	}
	expectedBodyLen := 1 + result.declaredPeerCount*currentPartyPeerEndpointSize
	result.parseOK = len(body) == expectedBodyLen
	if !result.parseOK {
		result.parseReason = "body_length_mismatch"
	}
	for index := 0; index < rowCount; index++ {
		start := 1 + index*currentPartyPeerEndpointSize
		if start+currentPartyPeerEndpointSize > len(body) {
			result.parseOK = false
			if result.parseReason == "" {
				result.parseReason = "short_peer_row"
			}
			break
		}
		row := body[start : start+currentPartyPeerEndpointSize]
		parsed := partyPeerEndpointDiagnosticRow{
			peerID:             uint16(row[0]) | uint16(row[1])<<8,
			port:               binary.BigEndian.Uint16(row[10:12]),
			nat:                row[16],
			mtu:                uint32(row[17]) | uint32(row[18])<<8 | uint32(row[19])<<16 | uint32(row[20])<<24,
			characterAttribute: row[21],
		}
		copy(parsed.inner[:], row[2:6])
		copy(parsed.outer[:], row[6:10])
		result.rows = append(result.rows, parsed)
	}
	if result.truncated && result.parseOK {
		result.parseReason = "row_limit"
	}
	return result
}

// logPartyPeerEndpointSendDiagnostic runs after the send call. It records
// its input body and return value, not socket delivery (send filters may skip
// or replace the body and still return nil). It never asks the
// relay for Endpoint again, so observing a lease cannot change it.
func (s *Service) logPartyPeerEndpointSendDiagnostic(
	session *gameSession,
	state alignedcmd.PartyState,
	stage partyPeerEndpointStage,
	body []byte,
	sendErr error,
) {
	if s == nil || session == nil {
		return
	}
	parsed := parsePartyPeerEndpointDiagnosticBody(body)
	receiver := currentPartyPeerEndpointSnapshot(session)
	callOK := sendErr == nil
	sendResult := "returned_error"
	if callOK {
		sendResult = "returned_nil"
	}
	peerIDs := make([]string, 0, len(parsed.rows))
	peerPorts := make([]string, 0, len(parsed.rows))
	args := []any{
		"kind", partyPeerEndpointDiagnosticKind,
		"stage", string(stage),
		"source", stage.source(),
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"receiver_char_id", session.selectedCharacterID,
		"receiver_port", receiver.Port,
		"receiver_port_endian", s.partyPeerPortEndian().String(),
		"receiver_udp_reported", receiver.Port != 0,
		"receiver_mtu", receiver.MTU,
		"receiver_attr", receiver.CharacterAttribute,
		"receiver_stamped", receiver.PeerRowAccStamped,
		"party_id", state.PartyID,
		"member_count", len(runtimePartyMembers(state)),
		"peer_count", parsed.declaredPeerCount,
		"peer_rows_logged", len(parsed.rows),
		"peer_rows_truncated", parsed.truncated,
		"body_len", len(body),
		"body_digest", currentProjectionBodyDigest(body),
		"peer_row_width", currentPartyPeerEndpointSize,
		"parse_ok", parsed.parseOK,
		"parse_reason", parsed.parseReason,
		"send_call_ok", callOK,
		"send_call_result", sendResult,
		"body_scope", "send_call_input",
		"include_self", s.partyPeerEndpointIncludeSelf().String(),
	}
	for index, row := range parsed.rows {
		prefix := "peer_" + strconv.Itoa(index+1) + "_"
		args = append(args,
			prefix+"char_id", row.peerID,
			prefix+"port", row.port,
			prefix+"port_endian", "be",
			prefix+"nat", row.nat,
			prefix+"mtu", row.mtu,
			prefix+"attr", row.characterAttribute,
			prefix+"inner_class", partyPeerDiagnosticAddressClass(row.inner),
			prefix+"outer_class", partyPeerDiagnosticAddressClass(row.outer),
			prefix+"inner_equals_outer", row.inner == row.outer,
			prefix+"relation", partyPeerDiagnosticAddressRelation(receiver, row.inner, row.outer),
		)
		peerIDs = append(peerIDs, strconv.FormatUint(uint64(row.peerID), 10))
		peerPorts = append(peerPorts, strconv.FormatUint(uint64(row.port), 10))
	}
	if len(peerIDs) > 0 {
		args = append(args, "peer_ids", strings.Join(peerIDs, ","), "peer_ports", strings.Join(peerPorts, ","))
	}
	s.logInfo("dnfbridge party peer endpoint diagnostic", args...)
}

// logPartyPeerEndpointRegistrationDiagnostic is the CS op2 whitelist summary.
// It intentionally omits the machine token, inner/outer IPv4 values and the
// op11 row account stamp.
func (s *Service) logPartyPeerEndpointRegistrationDiagnostic(
	session *gameSession,
	registration currentPartyPeerEndpointRegistration,
	changed bool,
) {
	if s == nil || session == nil {
		return
	}
	s.logInfo("dnfbridge party peer endpoint registration diagnostic",
		"kind", partyPeerEndpointRegistrationDiagnosticKind,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"port", registration.Port,
		"port_endian", s.partyPeerPortEndian().String(),
		"character_attribute", registration.CharacterAttribute,
		"mtu", registration.MTU,
		"advertised_mtu", currentPartyPeerAdvertisedMTU(registration),
		"port_changed", changed,
		"acc_stamped", registration.PeerRowAccStamped,
	)
}

func partyPeerDiagnosticAddressClass(address [4]byte) string {
	switch {
	case ipv4IsZero(address):
		return "unspecified"
	case address[0] == 127:
		return "loopback"
	case address[0] == 10:
		return "private"
	case address[0] == 172 && address[1] >= 16 && address[1] <= 31:
		return "private"
	case address[0] == 192 && address[1] == 168:
		return "private"
	case address[0] == 169 && address[1] == 254:
		return "link_local"
	case address[0] == 100 && address[1] >= 64 && address[1] <= 127:
		return "shared"
	default:
		return "public"
	}
}

func partyPeerDiagnosticAddressRelation(
	receiver currentPartyPeerEndpointRegistration,
	inner [4]byte,
	outer [4]byte,
) string {
	if ipv4IsZero(inner) && ipv4IsZero(outer) {
		return "unspecified"
	}
	receiverUnreported := receiver.Port == 0 && ipv4IsZero(receiver.InnerIPv4) && ipv4IsZero(receiver.OuterIPv4)
	if receiverUnreported {
		if inner == outer {
			return "receiver_unreported_inner_equals_outer"
		}
		return "receiver_unreported"
	}
	sameInner := !ipv4IsZero(receiver.InnerIPv4) && inner == receiver.InnerIPv4
	sameOuter := !ipv4IsZero(receiver.OuterIPv4) && outer == receiver.OuterIPv4
	switch {
	case sameInner && sameOuter:
		return "same_inner_and_outer_as_receiver"
	case sameInner:
		return "same_inner_as_receiver"
	case sameOuter:
		return "same_outer_as_receiver"
	case inner == outer:
		return "inner_equals_outer_distinct_from_receiver"
	default:
		return "distinct_from_receiver"
	}
}
