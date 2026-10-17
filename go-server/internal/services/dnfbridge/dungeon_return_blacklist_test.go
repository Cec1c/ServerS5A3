package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestTownReturnBlacklistAcknowledgesWithoutReplayingOldDungeonActor(t *testing.T) {
	for _, phase := range []string{"pending_op24", "confirmed_actor_suffix", "completed_return"} {
		t.Run(phase, func(t *testing.T) {
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 99,
				postStartMapPlayerStateSent: true, sceneBootstrapTailSent: true}
			switch phase {
			case "pending_op24":
				session.dungeon.runtime = &runtimeDungeonState{townReturnPending: true, townReturnOp24Sent: true}
			case "confirmed_actor_suffix":
				session.confirmedDungeonReturnStatePending = true
			case "completed_return":
				session.returnTownFinishLoadingAckOnly = true
			}
			service := &Service{}
			if err := service.sendRequestBlacklistState(session, "test_op120_during_return", nil); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != uint16(dnfenum.CmdPacketRequestBlacklist) ||
				packet.Header.Classification != dnfproto.DefaultChannelClassification ||
				!bytes.Equal(packet.Body, currentRequestBlacklistResponseBody) || len(rest) != 0 {
				t.Fatalf("return blacklist must only ACK: header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
			}
			if session.runtimeAfterBlacklistSent || session.selectedUserInfoMode3Sent {
				t.Fatal("return consumed old dungeon actor seed")
			}
		})
	}
}

func TestCancelledTownReturnStillAllowsDungeonBlacklistSeed(t *testing.T) {
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99,
		postStartMapPlayerStateSent: true, sceneBootstrapTailSent: true,
		selectedUserInfoMode3Sent: true,
		dungeon:                   dungeonSessionState{runtime: &runtimeDungeonState{}},
	}
	if err := (&Service{}).sendRequestBlacklistState(session, "test_cancelled_return", nil); err != nil {
		t.Fatal(err)
	}
	if !session.runtimeAfterBlacklistSent {
		t.Fatal("cancelled return incorrectly blocks valid dungeon runtime seed")
	}
}
