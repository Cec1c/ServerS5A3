package dnfbridge

import (
	"bytes"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

// Exercise the actual legacy dispatch with existing repository/session helpers.
// Only synthetic in-memory state and temporary packet logs are used.
func TestCurrentRaidCreateDiagnostic334DispatchAuthenticationReject(t *testing.T) {
	for _, name := range []string{"valid_body", "malformed_body"} {
		t.Run(name, func(t *testing.T) {
			s, session, wire := prepareCurrentRaidTest(t)
			session.connID = "game-000334"
			session.accountFromToken = false
			body := currentRaidRequestFixture(0, "private-name334", 0, 0, 0)
			if name == "malformed_body" {
				body = body[:2]
			}
			logger, path := testCharacterOptionPacketLogger(t)
			t.Cleanup(func() { _ = logger.Close() })
			s.packetLog = logger
			if err := s.handleGameCommand(session, 1, 664, body); err != nil {
				t.Fatal(err)
			}
			reference, referenceSession, referenceWire := prepareCurrentRaidTest(t)
			referenceSession.connID = session.connID
			referenceSession.accountFromToken = false
			if err := reference.sendRaidCommandFailure(referenceSession, 664); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wire.write.Bytes(), referenceWire.write.Bytes()) {
				t.Fatal("dispatch changed any byte of the existing failure frame")
			}
			ack, rest := splitGameServerUpperPacket(t, wire.write.Bytes())
			if ack.Header.MsgID != 664 || ack.Header.Classification != 1 || len(rest) != 0 || !bytes.Equal(ack.Body, []byte{0, 3}) {
				t.Fatal("dispatch changed the failure opcode/class/body or emitted extra packets")
			}
			if len(s.raids) != 0 {
				t.Fatal("rejected dispatch created authoritative membership")
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
				t.Fatalf("expected exactly one dispatch rejection event, got %d", len(diagnostics))
			}
			fields := make(map[string]string)
			for _, field := range strings.Fields(diagnostics[0])[1:] {
				pair := strings.SplitN(field, "=", 2)
				if len(pair) != 2 {
					t.Fatal("diagnostic exported free text")
				}
				if _, duplicate := fields[pair[0]]; duplicate {
					t.Fatal("diagnostic duplicated a field")
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
			if fields["kind"] != "game-current-raid-create-rejected" || fields["conn_id"] != "game-000334" || fields["channel_id"] != "23" || fields["op"] != "664" || fields["reason"] != "dispatch_authentication_rejected" {
				t.Fatal("dispatch reason or finite correlation fields changed")
			}
			for _, private := range []string{"private-name334", "dnf:1", "Alpha", "body=", "error=", "ip=", "hash=", "token="} {
				if strings.Contains(diagnostics[0], private) {
					t.Fatal("diagnostic disclosed an undeclared value")
				}
			}
		})
	}
}

func TestCurrentRaidCreateDiagnostic334AuthenticatedDispatchSuccess(t *testing.T) {
	s, session, wire := prepareCurrentRaidTest(t)
	logger, path := testCharacterOptionPacketLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	s.packetLog = logger
	if !s.currentRaidAuthenticated(session) {
		t.Fatal("existing bound repository fixture is not authenticated")
	}
	if err := s.handleGameCommand(session, 1, 664, currentRaidRequestFixture(0, "normal334", 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	state, found := s.runtimeRaidForMember(session.selectedCharacterID)
	if !found || len(s.raids) != 1 || !state.CurrentLayout || state.CurrentUIFlags != [3]byte{} {
		t.Fatal("authenticated dispatch did not create the expected authoritative raid")
	}
	metadata, rest := splitGameServerUpperPacket(t, wire.write.Bytes())
	ack, rest := splitGameServerUpperPacket(t, rest)
	wantMetadata, err := buildCurrentRuntimeRaidMetadata(state, 0)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Header.MsgID != 591 || metadata.Header.Classification != 0 || !bytes.Equal(metadata.Body, wantMetadata) || ack.Header.MsgID != 664 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, raid.BuildCreateRaidResultBody(state.RaidKey)) {
		t.Fatal("authenticated metadata-before-ACK order or payload changed")
	}
	rest = consumeCurrentRaidProfiles(t, rest, state)
	roster, rest := splitGameServerUpperPacket(t, rest)
	wantRoster, err := raid.BuildCurrentMemberRefreshMode3Body(state.RaidKey, currentRuntimeRaidMembers(state))
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 || roster.Header.MsgID != 591 || roster.Header.Classification != 0 || !bytes.Equal(roster.Body, wantRoster) {
		t.Fatal("successful roster order/payload changed or an extra packet was sent")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readCharacterOptionPacketLog(t, path), "kind=game-current-raid-create-rejected ") {
		t.Fatal("authenticated success emitted a rejection")
	}
}

func TestCurrentRaidCreateDiagnostic334OtherOpcodeUnchanged(t *testing.T) {
	s, session, wire := prepareCurrentRaidTest(t)
	session.accountFromToken = false
	op := uint16(dnfenum.CmdPacketStartRaid)
	if op == 664 || !onlineRaidCommandKnown(op) {
		t.Fatal("countercase must be another known gated opcode")
	}
	logger, path := testCharacterOptionPacketLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	s.packetLog = logger
	if err := s.handleGameCommand(session, 1, op, nil); err != nil {
		t.Fatal(err)
	}
	reference, referenceSession, referenceWire := prepareCurrentRaidTest(t)
	referenceSession.accountFromToken = false
	referenceLogger, referencePath := testCharacterOptionPacketLogger(t)
	t.Cleanup(func() { _ = referenceLogger.Close() })
	reference.packetLog = referenceLogger
	if err := reference.sendRaidCommandFailure(referenceSession, op); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire.write.Bytes(), referenceWire.write.Bytes()) {
		t.Fatal("another opcode's failure frame changed")
	}
	ack, rest := splitGameServerUpperPacket(t, wire.write.Bytes())
	if ack.Header.MsgID != op || ack.Header.Classification != 1 || len(rest) != 0 || !bytes.Equal(ack.Body, []byte{0, 3}) || len(s.raids) != 0 {
		t.Fatal("another opcode's existing rejection behavior changed")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := referenceLogger.Close(); err != nil {
		t.Fatal(err)
	}
	events := func(text string) string {
		var lines []string
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "EVENT ") {
				line, _, _ = strings.Cut(line, " t=")
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	actual := events(readCharacterOptionPacketLog(t, path))
	expected := events(readCharacterOptionPacketLog(t, referencePath))
	if actual != expected {
		t.Fatal("another gated opcode changed the existing event sequence or fields")
	}
	if strings.Contains(actual, "kind=game-current-raid-create-rejected ") {
		t.Fatal("another opcode emitted the new rejection diagnostic")
	}
}
