package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// A local registry exercises the integration route without modifying the
// production registry. bufferConn captures bytes in memory; no socket is used.
func dispatchCurrentCeraTicket(t *testing.T, classification byte, body []byte) []byte {
	t.Helper()
	registry := mustGameplayModuleRegistry(ceraTicketGameplayModule())
	connection := &bufferConn{}
	service := &Service{options: options{
		gameUpperHeader:    gameUpperHeaderChannel13,
		gameUpperBodyCodec: gameUpperBodyCodecPlain,
	}}
	session := &gameSession{conn: connection}
	handled, err := registry.DispatchUpper(service, session,
		uint16(dnfenum.CmdPacketGenCeraticket), classification, body)
	if !handled || err != nil {
		t.Fatalf("op65 dispatch handled=%t err=%v", handled, err)
	}
	return connection.write.Bytes()
}

func TestCurrentCeraTicketEmptyRequestConsumesCompleteAck(t *testing.T) {
	for _, request := range [][]byte{nil, {}} {
		wire := dispatchCurrentCeraTicket(t, dnfproto.DefaultChannelClassification, request)
		ack, rest := splitGameServerUpperPacket(t, wire)
		if len(rest) != 0 {
			t.Fatalf("trailing wire bytes=%x", rest)
		}
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketGenCeraticket) ||
			ack.Header.Classification != dnfproto.DefaultChannelClassification {
			t.Fatalf("op65 header=%+v", ack.Header)
		}
		want := []byte{1, 0, 0, 0, 0, 0, 0, 0, 0}
		if !bytes.Equal(ack.Body, want) || len(ack.Body) != currentCeraTicketAckSize {
			t.Fatalf("op65 body=%x want=%x", ack.Body, want)
		}
		// Model the EXE's nested reader, so omitting the string-length dword
		// cannot silently regress to the incorrect five-byte ACK.
		n := int(binary.LittleEndian.Uint32(ack.Body[1:5]))
		ticketOffset := 5 + n
		if ticketOffset+4 != len(ack.Body) || binary.LittleEndian.Uint32(ack.Body[ticketOffset:]) != 0 {
			t.Fatalf("op65 nested string reader cannot consume body=%x", ack.Body)
		}
	}
}

func TestCurrentCeraTicketUnprovenBodyRejected(t *testing.T) {
	for _, body := range [][]byte{{0}, {6}, {0, 0, 0, 0}} {
		ack, rest := splitGameServerUpperPacket(t,
			dispatchCurrentCeraTicket(t, dnfproto.DefaultChannelClassification, body))
		if len(rest) != 0 || !bytes.Equal(ack.Body, []byte{0, 0}) {
			t.Fatalf("request=%x rejection=%x rest=%x", body, ack.Body, rest)
		}
	}
}

func TestCurrentCeraTicketCommandClassOnly(t *testing.T) {
	for _, classification := range []byte{0, 2} {
		if wire := dispatchCurrentCeraTicket(t, classification, nil); len(wire) != 0 {
			t.Fatalf("class=%d must not answer op65: %x", classification, wire)
		}
	}
}

func TestCurrentCeraTicketCanAttachToWebViewer(t *testing.T) {
	module := webViewerGameplayModule()
	registry := mustGameplayModuleRegistry(module)
	connection := &bufferConn{}
	service := &Service{options: options{
		gameUpperHeader:    gameUpperHeaderChannel13,
		gameUpperBodyCodec: gameUpperBodyCodecPlain,
	}}
	session := &gameSession{conn: connection}
	for _, request := range []struct {
		opcode uint16
		body   []byte
	}{
		{uint16(dnfenum.CmdPacketGenCeraticket), nil},
		{uint16(dnfenum.CmdPacketWebViewerInfo), []byte{6}},
	} {
		handled, err := registry.DispatchUpper(service, session, request.opcode,
			dnfproto.DefaultChannelClassification, request.body)
		if !handled || err != nil {
			t.Fatalf("opcode=%d handled=%t err=%v", request.opcode, handled, err)
		}
	}
	first, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	second, trailing := splitGameServerUpperPacket(t, rest)
	if first.Header.MsgID != 65 || len(first.Body) != 9 ||
		second.Header.MsgID != 773 || !bytes.Equal(second.Body, []byte{1, 6, 0, 0, 0, 0}) || len(trailing) != 0 {
		t.Fatalf("combined module replies: first=%+v second=%+v trailing=%x", first, second, trailing)
	}
}

func TestCurrentCeraTicketProductionRegistry(t *testing.T) {
	connection := &bufferConn{}
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	session := &gameSession{conn: connection}
	handled, err := currentGameplayModules.DispatchUpper(service, session, uint16(dnfenum.CmdPacketGenCeraticket), dnfproto.DefaultChannelClassification, nil)
	if !handled || err != nil {
		t.Fatalf("production op65 dispatch handled=%t err=%v", handled, err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != 65 || len(ack.Body) != 9 || len(rest) != 0 {
		t.Fatalf("production op65 reply=%+v rest=%x", ack, rest)
	}
}

func TestCurrentCeraTicketObservedLegacyRoute(t *testing.T) {
	connection := &bufferConn{}
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	session := &gameSession{conn: connection}
	// Reproduce the observed native cmd=1/type=65/body_len=0 dispatch.
	if err := service.handleGameCommand(session, 1, uint16(dnfenum.CmdPacketGenCeraticket), nil); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != 65 || len(ack.Body) != 9 || len(rest) != 0 {
		t.Fatalf("observed legacy op65 reply=%+v rest=%x", ack, rest)
	}
}
