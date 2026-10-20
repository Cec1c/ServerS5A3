package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 这个测试锁住用户报告的现象本身：客户端点了 op99「合成」后必须收到回包。
// op99 以前只落到 avatartitle 的只读预检分支（固定 ResponseAllowed=false），
// 服务端一个字节都不回，客户端就停在等待状态。
func TestAvatarCompoundOpcodeIsRoutedAndAlwaysAnswered(t *testing.T) {
	body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)

	t.Run("legacy transport", func(t *testing.T) {
		withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
		repositories, catalog := mustCurrentAvatarCompoundTestAssets(t, currentAvatarCompoundTestSlots())
		connection := &bufferConn{}
		service := currentAvatarCompoundTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		if err := service.handleGameCommand(
			session,
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketCompoundAvatar),
			body,
		); err != nil {
			t.Fatal(err)
		}
		if connection.write.Len() == 0 {
			t.Fatal("op99 produced no response: the client would hang")
		}
		ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketCompoundAvatar) ||
			ack.Header.Classification != dnfproto.DefaultChannelClassification {
			t.Fatalf("op99 ACK header=%+v", ack.Header)
		}
	})

	t.Run("upper transport", func(t *testing.T) {
		withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
		repositories, catalog := mustCurrentAvatarCompoundTestAssets(t, currentAvatarCompoundTestSlots())
		connection := &bufferConn{}
		service := currentAvatarCompoundTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		handled, err := currentGameplayModules.DispatchUpper(
			service,
			session,
			uint16(dnfenum.CmdPacketCompoundAvatar),
			dnfproto.DefaultChannelClassification,
			body,
		)
		if err != nil || !handled {
			t.Fatalf("upper dispatch handled=%t err=%v", handled, err)
		}
		if connection.write.Len() == 0 {
			t.Fatal("op99 produced no response: the client would hang")
		}
		ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketCompoundAvatar) {
			t.Fatalf("op99 ACK header=%+v", ack.Header)
		}
	})

	// 非 22 字节的包体同样必须得到显式失败包，而不是静默丢弃。
	t.Run("malformed body still answered", func(t *testing.T) {
		repositories, catalog := mustCurrentAvatarCompoundTestAssets(t, currentAvatarCompoundTestSlots())
		connection := &bufferConn{}
		service := currentAvatarCompoundTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		if err := service.handleGameCommand(
			session,
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketCompoundAvatar),
			[]byte{1, 2, 3, 4},
		); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketCompoundAvatar) ||
			!bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) {
			t.Fatalf("malformed op99 ACK=%x header=%+v", ack.Body, ack.Header)
		}
	})
}
