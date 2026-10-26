package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestAncient121ReconnectCommitsExplicitTargetAndRebuildsWithoutAssetMutation(t *testing.T) {
	service, session, repositories := newChannelReconnectTest(t)
	session.channel = channelcatalog.Channel{ID: 121, ServerID: 1, Type: 22, Group: "ancient", Name: "ch.121", NoticeName: "ch.121", Port: 10121}
	session.residentChannel = channelcatalog.Channel{ID: 10, ServerID: 1, Type: 11, Group: "normal", Port: 10010}
	session.connectionTownActorOwnerChannel = 121
	session.townActorOwnerChannel = 121
	before, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load before: %t %v", found, err)
	}
	equipmentBefore, _, err := repositories.Equipment.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if !markCurrentChannelReconnect(session, currentChannelReconnectProbeSize) {
		t.Fatal("op2 did not arm")
	}
	if session.residentChannel.ID != 121 {
		t.Fatalf("explicit target still landing %d", session.residentChannel.ID)
	}
	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, 29)
	frame, err := dnfproto.BuildChannelPacket(uint16(dnfenum.UpperMsgSelectCharacter), request, 2, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.handleGameUpper(session, frame); err != nil {
		t.Fatalf("121 target reconnect closed: %v", err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
	assertImmediateChannelReconnectOrder(t, packets, 121)
	if session.channelReconnect || !session.sceneBootstrapTailSent {
		t.Fatal("target route unfinished")
	}
	after, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || after.Stats["town_id"] != 39 || after.Stats["area_id"] != 2 ||
		after.Stats["pos_x"] != 612 || after.Stats["pos_y"] != 244 ||
		after.Stats[currentTownPrevVillageTownStat] != before.Stats["town_id"] ||
		after.Stats[currentTownPrevVillageAreaStat] != before.Stats["area_id"] {
		t.Fatalf("远古频道换线未进入房间或丢失原城镇来源：%v %v", after.Stats, err)
	}
	equipmentAfter, _, err := repositories.Equipment.Load(context.Background(), "29")
	if err != nil || !reflect.DeepEqual(equipmentBefore, equipmentAfter) {
		t.Fatalf("equipment changed: %v", err)
	}
}

func TestExplicitSpecialReconnectKeepsBootstrapLandingUntilActualProbeAndNeverTruncates(t *testing.T) {
	for _, id := range []int{121, 251, 252, 508} {
		t.Run(string(rune(id)), func(t *testing.T) {
			session := &gameSession{channel: channelcatalog.Channel{ID: id, Type: 22}, residentChannel: channelcatalog.Channel{ID: 10}}
			session.rosterRequested = true
			if markCurrentChannelReconnect(session, currentChannelReconnectProbeSize) || session.residentChannel.ID != 10 {
				t.Fatal("ordinary roster changed special landing")
			}
			session.rosterRequested = false
			if !markCurrentChannelReconnect(session, currentChannelReconnectProbeSize) {
				t.Fatal("explicit probe not armed")
			}
			want := id
			if id > 255 {
				want = 10
			}
			if session.residentChannel.ID != want {
				t.Fatalf("resident %d want %d", session.residentChannel.ID, want)
			}
		})
	}
}
