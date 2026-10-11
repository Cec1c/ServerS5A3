package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// This is test1's actual bodyless 1423, not the older generated 1425.
func TestOfficial249SeriaBottomExitUsesRealEntryTown(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		t.Run(transport, func(t *testing.T) {
			service, session, repositories := loginSeriaEntryTownFixture(t, 40, 0, 1500, 500)
			service.options.currentClientVersion = "official-24.9"
			loginIntoTheSeriaRoom(t, service, session, repositories)
			connection := session.conn.(*bufferConn)
			connection.write.Reset()
			var err error
			if transport == "legacy" {
				err = service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), 1423, nil)
			} else {
				frame, buildErr := dnfproto.BuildChannelPacket(1423, nil, 0, dnfproto.DefaultChannelClassification)
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				err = service.handleGameUpper(session, frame)
			}
			if err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
			if packet.Header.MsgID != currentSceneTransitionMsgID || len(rest) != 0 || !bytes.Equal(packet.Body, wantSeriaRoomExitBody(t, 40, 0, 523, 142, 5, 3)) {
				t.Fatalf("exit failed: header=%+v body=%x trailing=%x", packet.Header, packet.Body, rest)
			}
			stored := loadStoredCharacter(t, repositories)
			if stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 0 || stored.Stats["pos_x"] != 523 || stored.Stats["pos_y"] != 142 {
				t.Fatalf("real entry town not persisted: %+v", stored.Stats)
			}
			connection.write.Reset()
			if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), 1423, nil); err != nil {
				t.Fatal(err)
			}
			if connection.write.Len() != 0 {
				t.Fatal("exit replay teleported a character outside Seria")
			}
		})
	}
}
