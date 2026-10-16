package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场缺陷是"锻造窗口永远等不到回包、关不掉"：所以连**请求体都解不出来**的
// 畸形 438 也必须回一条 class1 失败体（0 + u8 error），让客户端走完
// 0x2483b60 那条"复位锻造 UI"的回调。这条用例不需要运行时 PVF。
func TestHandleAlignedGameCommandRepliesToMalformedForgingFrame(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	service := &Service{
		options: options{
			accountID:          "dnf:forging",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			// 明确指向不存在的路径：这条用例不依赖任何运行时 PVF。
			pvfPath: "Z:/dnf-forging-test-does-not-exist/Script.pvf",
		},
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		},
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		channel:             channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038},
		selectedCharacterID: 77,
	}
	// 现场样例 00 0900 a84f0506 7900 02000000 ced0 00 的截断版：
	// 名字长度声称 2 字节，体里却没有名字与 confirmation。
	body, err := hex.DecodeString("000900a84f0506790002000000")
	if err != nil {
		t.Fatal(err)
	}
	handled, err := service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketUpgradeItemSeparate),
		body,
	)
	if err != nil {
		t.Fatalf("handleAlignedGameCommand error = %v", err)
	}
	if !handled {
		t.Fatal("malformed forging frame must still be handled with a failure ack")
	}
	packet, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(trailing) != 0 {
		t.Fatalf("trailing bytes = %d, want only the forging failure ack", len(trailing))
	}
	if packet.Header.Classification != dnfproto.DefaultChannelClassification || packet.Header.MsgID != uint16(dnfenum.CmdPacketUpgradeItemSeparate) {
		t.Fatalf("header = %+v, want class1/438", packet.Header)
	}
	if !bytes.Equal(packet.Body, []byte{0, currentForgingFailureInvalidTarget}) {
		t.Fatalf("body = %x, want failure body 0 + %d", packet.Body, currentForgingFailureInvalidTarget)
	}
}
