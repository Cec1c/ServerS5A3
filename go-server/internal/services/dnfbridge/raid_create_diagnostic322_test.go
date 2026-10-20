package dnfbridge

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// These are real handler calls with existing in-memory repository/session helpers.
// They never start a product service, access SQL, or replay private player input.
func TestCurrentRaidCreateDiagnostic322RejectsWithoutChangingReply(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
	}{
		{"decode", "request_decode_rejected"},
		{"name", "raid_name_rejected"},
		{"flags", "ui_flags_rejected"},
		{"no_token", "authentication_rejected"},
		{"wrong_owner", "authentication_rejected"},
		{"retired", "authentication_rejected"},
		{"channel", "channel_type_unsupported"},
		{"route", "route_mismatch"},
		{"membership", "runtime_create_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, wire := prepareCurrentRaidTest(t)
			session.connID = "game-000322"
			body := currentRaidRequestFixture(0, "private-name322", 0, 0, 0)
			var before runtimeRaidState
			if tc.name == "membership" {
				if err := s.handleCurrentCreateRaid(session, 664, body); err != nil {
					t.Fatal(err)
				}
				var found bool
				before, found = s.runtimeRaidForMember(session.selectedCharacterID)
				if !found {
					t.Fatal("normal seed creation did not create membership")
				}
				wire.write.Reset()
			}
			switch tc.name {
			case "decode":
				body = body[:2]
			case "name":
				body = currentRaidRequestFixture(0, " ", 0, 0, 0)
			case "flags":
				body[len(body)-3] = 2
			case "no_token":
				session.accountFromToken = false
			case "wrong_owner":
				session.accountID = "private-owner322"
			case "retired":
				s.unbindGameSession(session)
			case "channel":
				session.channel.Type = 11
			case "route":
				body[0] = 1
			}
			logger, path := testCharacterOptionPacketLogger(t)
			t.Cleanup(func() { _ = logger.Close() })
			s.packetLog = logger
			if err := s.handleCurrentCreateRaid(session, 664, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, wire.write.Bytes())
			if ack.Header.MsgID != 664 || ack.Header.Classification != 1 || len(rest) != 0 || !bytes.Equal(ack.Body, []byte{0, runtimeRaidFailureCode}) {
				t.Fatal("diagnostic changed the existing failure opcode/class/body or sent extra packets")
			}
			if tc.name == "membership" {
				after, ok := s.runtimeRaidForMember(session.selectedCharacterID)
				if !ok || len(s.raids) != 1 || !reflect.DeepEqual(before, after) {
					t.Fatal("rejected repeated creation changed authoritative membership")
				}
			} else if len(s.raids) != 0 {
				t.Fatal("rejection created a runtime raid")
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			var diagnostics []string
			for _, line := range strings.Split(readCharacterOptionPacketLog(t, path), "\n") {
				if strings.HasPrefix(line, "EVENT kind=game-current-raid-create-rejected ") {
					diagnostics = append(diagnostics, line)
				}
			}
			if len(diagnostics) != 1 {
				t.Fatalf("expected one fixed-enum diagnostic, got %d", len(diagnostics))
			}
			fields := make(map[string]string)
			for _, field := range strings.Fields(diagnostics[0])[1:] {
				pair := strings.SplitN(field, "=", 2)
				if len(pair) != 2 {
					t.Fatal("diagnostic has an untyped/free text field")
				}
				fields[pair[0]] = pair[1]
			}
			for key := range fields {
				switch key {
				case "kind", "conn_id", "channel_id", "op", "reason", "t":
				default:
					t.Fatalf("diagnostic exported undeclared field %q", key)
				}
			}
			if fields["conn_id"] != "game-000322" || fields["channel_id"] != "23" || fields["op"] != "664" || fields["reason"] != tc.reason {
				t.Fatal("diagnostic lacks the expected finite reason and correlation fields")
			}
			for _, secret := range []string{"private-name322", "private-owner322", "dnf:1", "character already owns raid membership"} {
				if strings.Contains(diagnostics[0], secret) {
					t.Fatal("diagnostic included an owner/name/provider value")
				}
			}
		})
	}
}

func TestCurrentRaidCreateDiagnostic322SuccessHasNoRejection(t *testing.T) {
	s, session, wire := prepareCurrentRaidTest(t)
	logger, path := testCharacterOptionPacketLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	s.packetLog = logger
	if err := s.handleCurrentCreateRaid(session, 664, currentRaidRequestFixture(0, "normal322", 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	metadata, rest := splitGameServerUpperPacket(t, wire.write.Bytes())
	ack, _ := splitGameServerUpperPacket(t, rest)
	if metadata.Header.MsgID != 591 || metadata.Header.Classification != 0 || ack.Header.MsgID != 664 || ack.Header.Classification != 1 || ack.Body[0] != 1 {
		t.Fatal("success metadata-before-ACK order changed")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readCharacterOptionPacketLog(t, path), "kind=game-current-raid-create-rejected ") {
		t.Fatal("a successful create logged a rejection")
	}
}
