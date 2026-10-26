package dnfbridge

import (
	"testing"
)

func TestOfficialChannelRoomLocationPrecedesSceneActivation(t *testing.T) {
	s, p, _ := newChannelReconnectTest(t)
	s.options.currentClientVersion = "official-24.9"
	p.channelReconnect = true
	s.bindGameSessionCharacter(p, 29)
	if err := s.sendCurrentChannelReconnectTownEntry(p); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
	area, scene, commit := -1, -1, -1
	for i, packet := range packets {
		if packet.Header.MsgID == currentTownUserAreaNotificationMsgID {
			area = i
			if len(packet.Body) != 10 || packet.Body[2] != 39 || packet.Body[3] != 2 {
				t.Fatal("换线首次位置不是赛丽亚房间", packet.Body)
			}
		}
		if packet.Header.MsgID == currentTownActorSceneSnapshotMsgID {
			scene = i
		}
		if packetLooksLikeInitialTownTransition(packet) {
			commit = i
		}
	}
	if area < 0 || scene < 0 || commit < 0 || area >= scene || scene >= commit {
		t.Fatalf("房间位置必须在场景激活前提交，完整初始化后保留最终op24：位置=%d 场景=%d 提交=%d", area, scene, commit)
	}
}
