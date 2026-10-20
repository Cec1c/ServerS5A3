package dnfbridge

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// A login packet is logged before its token can be authenticated. Redact the
// entire payload regardless of validity, body layout, direction or hex limit.
// Raw TCP chunks may split a credential, so they cannot safely carry hex either.
func loginPacketLogMetadata(kind string, data []byte, fields []any) ([]any, bool) {
	msgID, hasMsgID := packetLogUintField(fields, "msg_id")
	classification, hasClass := packetLogUintField(fields, "classification")
	typ, hasType := packetLogUintField(fields, "type")
	sensitive := kind == "game-raw" || kind == "channel-raw"
	// A decode/error event may lack classification. A known class-zero endpoint
	// notice stays ordinary; an unclassified op1 diagnostic cannot carry a body.
	if hasMsgID && msgID == 1 && (!hasClass || classification == uint64(dnfproto.DefaultChannelClassification)) {
		sensitive = true
	}
	switch kind {
	case "game", "game-legacy", "game-fixed15", "game-transport-meta", "game-legacy-meta", "game-send-meta", "game-fixed15-send-meta":
		if hasType && typ == uint64(dnfenum.GameTypeLogin) {
			sensitive = true
		}
	}
	// Header identity is enough to redact a malformed login frame; length or
	// checksum validation remains solely the business parser's responsibility.
	if kind != "game" && len(data) >= 3 && data[0] == dnfproto.DefaultChannelClassification && binary.LittleEndian.Uint16(data[1:3]) == 1 {
		sensitive = true
		// The parsed header remains authoritative if supplied fields disagree.
		msgID, hasMsgID = 1, true
		classification, hasClass = uint64(data[0]), true
	}
	if kind == "game" {
		// Direct transport logging must inspect every record, even if its supplied
		// fields describe an ordinary neighbour. Never infer upper class/opcode
		// from a TCP outer header, which has a different layout.
		if records, err := dnfproto.ParseLatestGameTCPRecords(data); err == nil {
			for _, record := range records {
				if dnfenum.GameType(record.GameHeader.Type) == dnfenum.GameTypeLogin {
					sensitive = true
				}
			}
		}
	}
	if !sensitive {
		return nil, false
	}
	metadata := make([]any, 0, len(fields)+4)
	for i := 0; i+1 < len(fields); i += 2 {
		switch fields[i] {
		case "conn_id", "channel_id", "pkt_seq", "header_seq", "sequence", "body_len", "wire_body_len", "frame_len", "cmd", "type", "record_scoped":
			metadata = append(metadata, fields[i], fields[i+1])
		}
	}
	if hasMsgID {
		metadata = append(metadata, "msg_id", msgID)
	}
	if hasClass {
		metadata = append(metadata, "classification", classification)
	}
	return metadata, true
}

// Parser/provider errors may embed payload bytes in Error(), and a caller can
// already have converted that error to a string. Only logging receives this
// replacement: the original error, unwrap chain and control flow are unchanged.
func redactPacketDiagnosticErrors(fields []any) []any {
	var out []any
	for i := 0; i+1 < len(fields); i += 2 {
		value := fields[i+1]
		text, redact := "", false
		if diagnostic, ok := value.(businessLogValue); ok {
			if text, safe := diagnostic.text(); safe {
				if out == nil {
					out = append(make([]any, 0, len(fields)), fields[:i]...)
				}
				out = append(out, fields[i], text)
				continue
			}
			// Unsafe nil/panic formatting keeps the existing hash-only policy.
			text, redact = fmt.Sprint(diagnostic.value), true
		}
		if err, ok := value.(error); !redact && ok && err != nil {
			// fmt protects typed-nil and panicking Error methods. Its representation
			// may contain a panic body, so only length/hash are allowed in the log.
			text, redact = fmt.Sprint(err), true
		} else if !redact && fields[i] == "error" {
			text, redact = value.(string)
		}
		if redact {
			if out == nil {
				out = append(make([]any, 0, len(fields)+4), fields[:i]...)
			}
			out = append(out, fields[i], "redacted", fmt.Sprint(fields[i])+"_len", len(text), fmt.Sprint(fields[i])+"_sha256", fmt.Sprintf("%x", sha256.Sum256([]byte(text))))
		} else if out != nil {
			out = append(out, fields[i], value)
		}
	}
	if out == nil {
		return fields
	}
	return out
}

func redactedLoginPacketLogLine(direction, kind string, data []byte, metadata []any) string {
	line := fmt.Sprintf("%s kind=%s", direction, kind)
	for i := 0; i+1 < len(metadata); i += 2 {
		line += fmt.Sprintf(" %v=%v", metadata[i], metadata[i+1])
	}
	return line + fmt.Sprintf(" len=%d payload_redacted=true payload_sha256=%x", len(data), sha256.Sum256(data))
}
