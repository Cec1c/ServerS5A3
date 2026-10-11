package dnfbridge

import (
	"bytes"
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

// TestCurrentWebViewerTicketSuccessTypes 钉住已证成功形状：请求体恰 1 字节且票据类型
// 命中已证区间 4..6（0x378c370 的 sub esi,4 + 两次 dec/je）时，回 6 字节成功体
// （成功位 + type 回显 + code=0）。下行不携带 URL：页面地址是客户端本地常量 0x4933b68。
func TestCurrentWebViewerTicketSuccessTypes(t *testing.T) {
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
			want := []byte{0x01, ticketType, 0, 0, 0, 0}
			if !bytes.Equal(ack.Body, want) {
				t.Fatalf("op773 ACK body=%x want=%x (type=%d)", ack.Body, want, ticketType)
			}
			if len(ack.Body) != currentWebViewerTicketAckSize {
				t.Fatalf("op773 ACK body len=%d want=%d", len(ack.Body), currentWebViewerTicketAckSize)
			}
			for _, fragment := range [][]byte{[]byte("http"), []byte("://"), []byte("ERROR")} {
				if bytes.Contains(ack.Body, fragment) {
					t.Fatalf("op773 ACK carries URL fragment %q: %x", fragment, ack.Body)
				}
			}
		})
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
