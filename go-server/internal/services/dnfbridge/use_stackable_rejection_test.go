package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func buildRejectedUseStackableBody(slot int16, listType byte, instance int32, itemCode int32) []byte {
	body := make([]byte, 15)
	binary.LittleEndian.PutUint16(body[0:2], uint16(slot))
	body[2] = listType
	binary.LittleEndian.PutUint32(body[3:7], uint32(instance))
	binary.LittleEndian.PutUint32(body[7:11], uint32(itemCode))
	return body
}

// 操作者报的"使用无效会导致卡操作"就是这条路径修好的：op44 被拒绝时必须回一个
// 当前客户端认得的失败体，否则客户端一直等结果、之后任何操作都点不动。
// 这条用例把**失败体的字节**钉死：{0x00, errorCode=13, listType, u32 instance, u32 itemCode}，
// 并且必须走 class1 成功通道（客户端在那里读失败首字节）。
func TestAnswerRejectedUseStackableSendsExactFailureAck(t *testing.T) {
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "use-stackable-rejection", selectedCharacterID: 42}

	const (
		slot     int16 = 65
		listType byte  = 0
		instance int32 = 0x11223344
		itemCode int32 = 490701730 // 春节欢庆爆竹
	)
	if err := service.answerRejectedUseStackable(session, buildRejectedUseStackableBody(slot, listType, instance, itemCode), "unit test"); err != nil {
		t.Fatalf("answer rejected use-stackable: %v", err)
	}
	// 线上就是 server16 头 + plaintext 正文（DNFBRIDGE_GAME_UPPER_HEADER=server16），
	// 所以这里用 16 字节头的解析器，和发送侧配置保持一致。
	packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketUseStackable) ||
		packet.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("failure ack header = class %d msg %d, want class1 op44",
			packet.Header.Classification, packet.Header.MsgID)
	}
	want := []byte{0, currentUseStackableRejectionCode, listType}
	var number [4]byte
	binary.LittleEndian.PutUint32(number[:], uint32(instance))
	want = append(want, number[:]...)
	binary.LittleEndian.PutUint32(number[:], uint32(itemCode))
	want = append(want, number[:]...)
	if !bytes.Equal(packet.Body, want) {
		t.Fatalf("failure ack body = % X, want % X", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("failure ack left %d trailing bytes", len(rest))
	}
}

// 请求体连解析都失败时没有可回填的 listType/instance/itemCode，这条路径保持静默
// （不回一个字段全错的包），但也不能 panic。
func TestAnswerRejectedUseStackableStaysSilentWithoutParseableBody(t *testing.T) {
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "use-stackable-rejection-unparsable", selectedCharacterID: 42}

	if err := service.answerRejectedUseStackable(session, []byte{1, 2, 3}, "unit test unparsable"); err != nil {
		t.Fatalf("unparsable rejection must not fail: %v", err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("unparsable rejection wrote %d bytes", conn.write.Len())
	}
}
