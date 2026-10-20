package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 厂商对 op140 的应答形状：class1 / op140 / body = 01 + 16 个 0（17 字节）。
// 这是"公会 网络连接中…"与选人界面 Loading 卡住的对症修法（保持应答在线）。
func TestVendorGuildMemberListAnswerShape(t *testing.T) {
	service := &Service{}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, channel: channelcatalog.Channel{ID: 19, Type: 1, Name: "ch.19", Port: 10019}}
	if err := service.sendVendorGuildMemberListState(session, "unit_test"); err != nil {
		t.Fatalf("send vendor guild answer: %v", err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("trailing bytes: %x", rest)
	}
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketGuildAllMemberList) || packet.Header.Classification != 1 {
		t.Fatalf("guild answer header = class=%d msg=%d", packet.Header.Classification, packet.Header.MsgID)
	}
	if len(packet.Body) != 17 || packet.Body[0] != 0x01 {
		t.Fatalf("guild answer body = %x, want 01 then 16 zero bytes", packet.Body)
	}
	for i := 1; i < len(packet.Body); i++ {
		if packet.Body[i] != 0 {
			t.Fatalf("guild answer body[%d] = %#x, want 0", i, packet.Body[i])
		}
	}
}
