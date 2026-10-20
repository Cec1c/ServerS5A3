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

// readCurrentCeraTicketAck models the EXE 0x1fbb540 read order:
// success:u8 -> u32 stringByteLength -> length bytes -> u32 ticket.
type ceraTicketAckRead struct {
	URL      string
	Ticket   uint32
	Consumed int
	Complete bool
}

func readCurrentCeraTicketAck(body []byte) ceraTicketAckRead {
	var read ceraTicketAckRead
	enough := func(n int) bool { return len(body) >= read.Consumed+n }
	if !enough(1) {
		return read
	}
	read.Consumed = 1
	if !enough(4) {
		return read
	}
	length := int(binary.LittleEndian.Uint32(body[read.Consumed:]))
	read.Consumed += 4
	if length > 0 {
		if !enough(length) {
			return read
		}
		read.URL = string(body[read.Consumed : read.Consumed+length])
		read.Consumed += length
	}
	if !enough(4) {
		return read
	}
	read.Ticket = binary.LittleEndian.Uint32(body[read.Consumed:])
	read.Consumed += 4
	read.Complete = true
	return read
}

func TestCurrentCeraTicketEmptyRequestConsumesCompleteAck(t *testing.T) {
	restore := currentCeraTicketURL
	t.Cleanup(func() { currentCeraTicketURL = restore })
	currentCeraTicketURL = "about:blank"

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
		read := readCurrentCeraTicketAck(ack.Body)
		if !read.Complete || read.Consumed != len(ack.Body) {
			t.Fatalf("op65 body=%x not fully consumable (consumed=%d)", ack.Body, read.Consumed)
		}
		if read.URL != "about:blank" || read.Ticket != 0 {
			t.Fatalf("op65 decoded url=%q ticket=%d body=%x", read.URL, read.Ticket, ack.Body)
		}
		// Model the EXE's nested reader, so omitting the string-length dword
		// cannot silently regress to the incorrect five-byte ACK.
		n := int(binary.LittleEndian.Uint32(ack.Body[1:5]))
		ticketOffset := 5 + n
		if ticketOffset+4 != len(ack.Body) || binary.LittleEndian.Uint32(ack.Body[ticketOffset:]) != 0 {
			t.Fatalf("op65 nested string reader cannot consume body=%x", ack.Body)
		}
		if len(ack.Body) != currentCeraTicketAckSize+len("about:blank") {
			t.Fatalf("op65 body len=%d want=%d", len(ack.Body), currentCeraTicketAckSize+len("about:blank"))
		}
	}
}

// TestCurrentCeraTicketZeroLengthStillUsesLocalFallback pins the documented
// fallback contract: an empty URL must keep the packet legal (length 0) so the
// client uses its own local constant instead of reading past the packet.
func TestCurrentCeraTicketZeroLengthStillUsesLocalFallback(t *testing.T) {
	restore := currentCeraTicketURL
	t.Cleanup(func() { currentCeraTicketURL = restore })
	currentCeraTicketURL = ""

	ack, rest := splitGameServerUpperPacket(t, dispatchCurrentCeraTicket(t, dnfproto.DefaultChannelClassification, nil))
	if len(rest) != 0 {
		t.Fatalf("trailing wire bytes=%x", rest)
	}
	want := []byte{1, 0, 0, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(ack.Body, want) || len(ack.Body) != currentCeraTicketAckSize {
		t.Fatalf("op65 fallback body=%x want=%x", ack.Body, want)
	}
}

// TestCurrentCeraTicketOverlongURLFallsBack pins the proven cap (0x40): the
// nested reader only consumes payload when 0 < length < cap, so a longer
// configured URL must degrade to length 0 rather than ship an unreadable body.
func TestCurrentCeraTicketOverlongURLFallsBack(t *testing.T) {
	restore := currentCeraTicketURL
	t.Cleanup(func() { currentCeraTicketURL = restore })

	currentCeraTicketURL = "http://127.0.0.1/" +
		"0123456789" + "0123456789" + "0123456789" +
		"0123456789" + "0123456789" + "0123456789"
	ack, _ := splitGameServerUpperPacket(t, dispatchCurrentCeraTicket(t, dnfproto.DefaultChannelClassification, nil))
	if len(ack.Body) != currentCeraTicketAckSize || binary.LittleEndian.Uint32(ack.Body[1:5]) != 0 {
		t.Fatalf("overlong op65 url body=%x", ack.Body)
	}

	currentCeraTicketURL = "中文页面"
	ack, _ = splitGameServerUpperPacket(t, dispatchCurrentCeraTicket(t, dnfproto.DefaultChannelClassification, nil))
	if len(ack.Body) != currentCeraTicketAckSize || binary.LittleEndian.Uint32(ack.Body[1:5]) != 0 {
		t.Fatalf("non-ascii op65 url body=%x", ack.Body)
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
	restore := currentWebViewerTicketURL
	t.Cleanup(func() { currentWebViewerTicketURL = restore })
	currentWebViewerTicketURL = "about:blank"

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
	if first.Header.MsgID != 65 || second.Header.MsgID != 773 || len(trailing) != 0 {
		t.Fatalf("combined module replies: first=%+v second=%+v trailing=%x", first, second, trailing)
	}
	// Both ACKs must be exactly consumable by their own proven reader; the old
	// op773 six-byte shape is pinned as RED in gameplay_module_web_viewer_test.go.
	if read := readCurrentCeraTicketAck(first.Body); !read.Complete || read.Consumed != len(first.Body) {
		t.Fatalf("combined op65 body=%x consumed=%d", first.Body, read.Consumed)
	}
	if read := readCurrentWebViewerTicketAck(second.Body); !read.Complete || read.Consumed != len(second.Body) {
		t.Fatalf("combined op773 body=%x consumed=%d", second.Body, read.Consumed)
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
	if ack.Header.MsgID != 65 || len(rest) != 0 {
		t.Fatalf("production op65 reply=%+v rest=%x", ack, rest)
	}
	if read := readCurrentCeraTicketAck(ack.Body); !read.Complete || read.Consumed != len(ack.Body) {
		t.Fatalf("production op65 body=%x consumed=%d", ack.Body, read.Consumed)
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
	if ack.Header.MsgID != 65 || len(rest) != 0 {
		t.Fatalf("observed legacy op65 reply=%+v rest=%x", ack, rest)
	}
	if read := readCurrentCeraTicketAck(ack.Body); !read.Complete || read.Consumed != len(ack.Body) {
		t.Fatalf("observed legacy op65 body=%x consumed=%d", ack.Body, read.Consumed)
	}
	// The legacy route must not answer from the local-processor fallback with an
	// empty URL: it uses the same configured blank page as the upper route.
	if read := readCurrentCeraTicketAck(ack.Body); read.URL != currentCeraTicketAckURL() {
		t.Fatalf("legacy op65 url=%q want=%q", read.URL, currentCeraTicketAckURL())
	}
}
