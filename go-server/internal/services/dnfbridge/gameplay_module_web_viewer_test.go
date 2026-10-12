package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// currentWebViewerTicketTestService 只需要能发包的最小 Service：op773 的回包不读
// 仓储、不读 PVF，因此不接 repository provider。
func currentWebViewerTicketTestService() *Service {
	return &Service{options: options{
		gameUpperHeader:    gameUpperHeaderChannel13,
		gameUpperBodyCodec: gameUpperBodyCodecPlain,
	}}
}

// dispatchCurrentWebViewerTicket 走真实注册表分发一次 op773，并把唯一的回包解出来。
// 三条断言都是形状判据（走没走注册表、是不是 class1/op773、有没有尾随 wire 字节），
// 不涉及 GUI 结论。
func dispatchCurrentWebViewerTicket(
	t *testing.T,
	classification byte,
	body []byte,
) dnfproto.ChannelPacket {
	t.Helper()
	connection := &bufferConn{}
	service := currentWebViewerTicketTestService()
	session := &gameSession{conn: connection}
	handled, err := currentGameplayModules.DispatchUpper(
		service,
		session,
		uint16(dnfenum.CmdPacketWebViewerInfo),
		classification,
		body,
	)
	if err != nil || !handled {
		t.Fatalf("op773 dispatch handled=%t err=%v body=%x", handled, err, body)
	}
	if connection.write.Len() == 0 {
		t.Fatalf("op773 produced no response body=%x", body)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("op773 ACK left trailing wire bytes=%x", rest)
	}
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketWebViewerInfo) ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("op773 ACK header=%+v", ack.Header)
	}
	return ack
}

// webViewerAckRead 是 0x1FD5360 成功分支读取顺序的模型：
// success:u8 -> u8 type -> u32 stringByteLength -> length 字节 -> u32 code。
// Complete 为 false 表示客户端还要读（0x37e82e0/0x3752050 会跨过包边界继续取流字节）。
type webViewerAckRead struct {
	TicketType byte
	URL        string
	Code       uint32
	Consumed   int
	Complete   bool
}

func readCurrentWebViewerTicketAck(body []byte) webViewerAckRead {
	var read webViewerAckRead
	enough := func(n int) bool { return len(body) >= read.Consumed+n }
	if !enough(1) {
		return read
	}
	read.Consumed = 1 // success flag
	if !enough(1) {
		return read
	}
	read.TicketType = body[read.Consumed]
	read.Consumed++
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
	read.Code = binary.LittleEndian.Uint32(body[read.Consumed:])
	read.Consumed += 4
	read.Complete = true
	return read
}

// TestCurrentWebViewerTicketSuccessBodyIsFullyConsumed 钉住已证成功形状：请求体恰
// 1 字节且票据类型命中已证区间 4..6 时，成功体必须是
// success | u8 type | u32 stringByteLength | url | u32 code，
// 且客户端按该顺序恰好读完整包、不多不少。
func TestCurrentWebViewerTicketSuccessBodyIsFullyConsumed(t *testing.T) {
	restore := currentWebViewerTicketURL
	t.Cleanup(func() { currentWebViewerTicketURL = restore })
	currentWebViewerTicketURL = "about:blank"

	for _, ticketType := range []byte{
		currentWebViewerTicketTypeMin,
		currentWebViewerTicketTypeMin + 1,
		currentWebViewerTicketTypeMax,
	} {
		t.Run("proven ticket type", func(t *testing.T) {
			ack := dispatchCurrentWebViewerTicket(
				t,
				dnfproto.DefaultChannelClassification,
				[]byte{ticketType},
			)
			read := readCurrentWebViewerTicketAck(ack.Body)
			if !read.Complete {
				t.Fatalf("op773 ACK body=%x not fully consumable (consumed=%d)", ack.Body, read.Consumed)
			}
			if read.Consumed != len(ack.Body) {
				t.Fatalf("op773 ACK body=%x read=%d body_len=%d (client would cross the packet boundary)",
					ack.Body, read.Consumed, len(ack.Body))
			}
			if read.TicketType != ticketType || read.URL != "about:blank" || read.Code != 0 {
				t.Fatalf("op773 ACK decoded type=%d url=%q code=%d body=%x",
					read.TicketType, read.URL, read.Code, ack.Body)
			}
			if len(ack.Body) != 2+4+len(read.URL)+4 {
				t.Fatalf("op773 ACK size=%d body=%x", len(ack.Body), ack.Body)
			}
		})
	}
}

// TestCurrentWebViewerTicketAckCarriesNestedStringLength 直接钉字节布局：旧实现
// （6 字节：成功位 + u8 + u32）漏掉嵌套字符串长度字段，客户端会多吃 4 字节，
// 因此这里逐个字段断言长度前缀与 url 载荷。
func TestCurrentWebViewerTicketAckCarriesNestedStringLength(t *testing.T) {
	restore := currentWebViewerTicketURL
	t.Cleanup(func() { currentWebViewerTicketURL = restore })
	currentWebViewerTicketURL = "about:blank"

	ack := dispatchCurrentWebViewerTicket(t, dnfproto.DefaultChannelClassification, []byte{6})
	if ack.Body[0] != 0x01 || ack.Body[1] != 6 {
		t.Fatalf("op773 ACK prefix=%x", ack.Body[:2])
	}
	if got := int(binary.LittleEndian.Uint32(ack.Body[2:6])); got != len("about:blank") {
		t.Fatalf("op773 ACK nested stringByteLength=%d body=%x", got, ack.Body)
	}
	if !bytes.Equal(ack.Body[6:6+len("about:blank")], []byte("about:blank")) {
		t.Fatalf("op773 ACK url payload=%x", ack.Body[6:6+len("about:blank")])
	}
	tail := ack.Body[6+len("about:blank"):]
	if !bytes.Equal(tail, []byte{0, 0, 0, 0}) {
		t.Fatalf("op773 ACK tail code=%x body=%x", tail, ack.Body)
	}
	if len(ack.Body) != currentWebViewerTicketAckMinSize+len("about:blank") {
		t.Fatalf("op773 ACK len=%d want=%d", len(ack.Body), currentWebViewerTicketAckMinSize+len("about:blank"))
	}
}

// TestCurrentWebViewerTicketLegacySixByteAckWouldStealNextPacket 钉住 2026-10-13
// P56 根因：6 字节旧形状（01 type u32code）在客户端读满 4+4 前就会越界，把紧随
// 其后的一包头部 4 字节吃成自己的字段。这条用例把"旧形状必须是 RED"固定下来，
// 防止以后有人把长度前缀再删回去。
func TestCurrentWebViewerTicketLegacySixByteAckWouldStealNextPacket(t *testing.T) {
	legacy := []byte{0x01, 0x06, 0x00, 0x00, 0x00, 0x00}
	next := []byte{0x01, 0x2a, 0x05, 0x00, 0x00, 0x00}
	stream := append(append([]byte{}, legacy...), next...)

	read := readCurrentWebViewerTicketAck(stream)
	if !read.Complete {
		t.Fatalf("legacy stream unexpectedly incomplete consumed=%d", read.Consumed)
	}
	if read.Consumed <= len(legacy) {
		t.Fatalf("legacy 6-byte ACK did not over-read: consumed=%d", read.Consumed)
	}
	// The 6-byte shape leaves the client one u32 short, so it consumes the next
	// packet's first four header bytes as its own code field.
	if read.URL != "" || read.Code != binary.LittleEndian.Uint32(next[:4]) {
		t.Fatalf("legacy stream decoded url=%q code=%d want_code=%d",
			read.URL, read.Code, binary.LittleEndian.Uint32(next[:4]))
	}
	stolen := read.Consumed - len(legacy)
	if stolen != 4 {
		t.Fatalf("legacy over-read=%d bytes, want 4", stolen)
	}
	if !bytes.Equal(stream[len(legacy):len(legacy)+stolen], next[:stolen]) {
		t.Fatalf("legacy over-read did not consume the next packet header")
	}

	// 修复后的成功体在同一个流里必须恰好停在包边界上。
	restore := currentWebViewerTicketURL
	t.Cleanup(func() { currentWebViewerTicketURL = restore })
	currentWebViewerTicketURL = "about:blank"
	fixed := buildCurrentWebViewerAck(6, currentWebViewerTicketAckURL())
	fixedStream := append(append([]byte{}, fixed...), next...)
	fixedRead := readCurrentWebViewerTicketAck(fixedStream)
	if !fixedRead.Complete || fixedRead.Consumed != len(fixed) {
		t.Fatalf("fixed ACK consumed=%d complete=%t len=%d", fixedRead.Consumed, fixedRead.Complete, len(fixed))
	}
}

// TestCurrentWebViewerTicketOverlongURLCannotEscapeProvenCap 钉住 cap 边界：
// 0x3752120 只在 0 < length < cap 时读 payload，超长地址必须回落空串（length 0），
// 而不是发出客户端读不了的 length。
func TestCurrentWebViewerTicketOverlongURLCannotEscapeProvenCap(t *testing.T) {
	restore := currentWebViewerTicketURL
	t.Cleanup(func() { currentWebViewerTicketURL = restore })

	cases := []string{
		"http://127.0.0.1/" + "0123456789" + "0123456789",
		"about:blank\u0000trailing",
		"中文页面",
	}
	for _, url := range cases {
		currentWebViewerTicketURL = url
		ack := dispatchCurrentWebViewerTicket(t, dnfproto.DefaultChannelClassification, []byte{4})
		read := readCurrentWebViewerTicketAck(ack.Body)
		if !read.Complete || read.Consumed != len(ack.Body) {
			t.Fatalf("url=%q ack=%x not fully consumable", url, ack.Body)
		}
		if read.URL != "" {
			t.Fatalf("url=%q must fall back to length 0, ack=%x", url, ack.Body)
		}
		if len(ack.Body) != currentWebViewerTicketAckMinSize {
			t.Fatalf("url=%q ack len=%d body=%x", url, len(ack.Body), ack.Body)
		}
	}
}

// TestCurrentWebViewerTicketUnprovenRequestsAreRejected 钉住"未证就不冒充成功"：
// 未证长度（空体、尾随字节）与未证类型（0/3/7/9）一律回显式失败体，
// 但每一条都必须有回包（旧行为是整条落进 unhandled，一个字节都不回）。
//
// 失败体固定为 00 00：成功位 0 + 0 码占位。op773 的失败分支不读包体任何字段
// （0x1fd5405 直接返回），所以这里没有可对应的客户端文案。
func TestCurrentWebViewerTicketUnprovenRequestsAreRejected(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "nil body", body: nil},
		{name: "empty body", body: []byte{}},
		{name: "type zero", body: []byte{0}},
		{name: "type below proven range", body: []byte{currentWebViewerTicketTypeMin - 1}},
		{name: "type above proven range", body: []byte{currentWebViewerTicketTypeMax + 1}},
		{name: "type four plus trailing byte", body: []byte{4, 0x00}},
		{name: "type six plus unproven trailing bytes", body: []byte{6, 0xaa, 0xbb}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ack := dispatchCurrentWebViewerTicket(
				t,
				dnfproto.DefaultChannelClassification,
				tc.body,
			)
			if !bytes.Equal(ack.Body, []byte{0x00, 0x00}) {
				t.Fatalf("op773 rejected body=%x want=0000 (request=%x)", ack.Body, tc.body)
			}
		})
	}
}

// TestCurrentWebViewerTicketRequiresCommandClass 钉住传输分类边界：只有 class1
// （COMMAND）通道被取证过，其它分类既不猜回包形状也不发包。
func TestCurrentWebViewerTicketRequiresCommandClass(t *testing.T) {
	connection := &bufferConn{}
	service := currentWebViewerTicketTestService()
	session := &gameSession{conn: connection}
	handled, err := currentGameplayModules.DispatchUpper(
		service,
		session,
		uint16(dnfenum.CmdPacketWebViewerInfo),
		dnfproto.DefaultChannelClassification+1,
		[]byte{6},
	)
	if err != nil || !handled {
		t.Fatalf("op773 class mismatch dispatch handled=%t err=%v", handled, err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("op773 class mismatch must not answer: %x", connection.write.Bytes())
	}
}
