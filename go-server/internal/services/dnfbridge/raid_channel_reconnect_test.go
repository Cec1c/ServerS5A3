package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestManualRaidReconnectCommitsTargetAndCompletesTownRoute(t *testing.T) {
	for _, target := range []channelcatalog.Channel{
		{ID: 23, Type: dnfenum.RaidChannelType, Group: "raid", Port: 10023},
		{ID: 241, Type: dnfenum.HiddenRaidType, Group: "luke_raid", Port: 10241},
	} {
		for _, withProbe := range []bool{true, false} {
			service, session, repositories := newChannelReconnectTest(t)
			service.options.currentClientVersion = "official-24.9"
			table, err := dnftown.Load(context.Background(), initialEquipmentMemSource{
				"town/town.lst":     "19 `anton.twn`\n32 `luke.twn`\n39 `ordinary.twn`\n",
				"town/anton.twn":    "[area]\n0 `Town/BattleShip/Gate_BattleShip.map`\n`[gate]` 474 234\n[/area]\n[area]\n2 `Town/BattleShip/BattleShip_Anton.map`\n`[dungeon gate]`\n27\n[/area]\n",
				"town/luke.twn":     "[area]\n0 `Town/Jelva_raid/Gate_Jelva.map`\n`[gate]` 474 234\n[/area]\n[area]\n2 `Town/Jelva_raid/2_Jelva_Waiting.map`\n`[dungeon gate]`\n32\n[/area]\n",
				"town/ordinary.twn": "[area]\n0 `Town/ordinary.map`\n`[normal]`\n[/area]\n",
			}, dnftown.Options{})
			if err != nil {
				t.Fatal(err)
			}
			service.townCatalog = table
			service.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) {
				return worldmap.Map{VirtualMovableArea: []int64{100, 200, 400, 100}}, true
			}
			before, _, err := repositories.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			// Use one assembled aggregate instead of the old fake Character repo
			// paired with a settlement transaction over a different memory repo.
			aggregate := dnfrepomemory.NewMemoryGroup()
			if err := aggregate.Character.Save(context.Background(), before); err != nil {
				t.Fatal(err)
			}
			repositories.Character = aggregate.Character
			repositories.CharacterSettlement = aggregate.CharacterSettlement
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
			session.channel = target
			session.residentChannel = channelcatalog.Channel{ID: 11, Type: 11, Port: 10011}
			session.connectionTownActorOwnerChannel = byte(target.ID)
			session.townActorOwnerChannel = byte(target.ID)
			if withProbe {
				probe, err := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgCharacterRoster), make([]byte, currentChannelReconnectProbeSize), 1, proto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				if err = service.handleGameUpper(session, probe); err != nil {
					t.Fatal(err)
				}
			}
			body := make([]byte, 2)
			binary.LittleEndian.PutUint16(body, 29)
			frame, err := proto.BuildChannelPacket(uint16(dnfenum.UpperMsgSelectCharacter), body, 2, proto.DefaultChannelClassification)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.handleGameUpper(session, frame); err != nil {
				t.Fatalf("raid target=%d probe=%v: %v", target.ID, withProbe, err)
			}
			if session.residentChannel.ID != target.ID {
				t.Fatalf("raid target replaced by ordinary landing: %+v", session.residentChannel)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
			assertImmediateChannelReconnectOrder(t, packets, byte(target.ID), false)
			readyCount := 0
			for _, packet := range packets {
				if packet.Header.MsgID == currentRaidChannelReadyMsgID {
					readyCount++
					if packet.Header.Classification != 0 || len(packet.Body) != 1 || packet.Body[0] != 0 {
						t.Fatal("raid reconnect emitted incompatible ready notice")
					}
				}
			}
			if readyCount != 1 {
				t.Fatalf("raid reconnect ready notice count=%d, want exactly one", readyCount)
			}
			wantTown := int64(19)
			if target.Type == dnfenum.HiddenRaidType {
				wantTown = 32
			}
			projected, townID, areaID, row, _, err := service.currentChannelReconnectTownTransition(context.Background(), session, 29, before)
			if err != nil || int64(townID) != wantTown || areaID != 0 || row.Value1 != 474 || row.Value2 != 234 || projected.Stats["town_id"] != wantTown {
				t.Fatalf("raid entrance town=%d area=%d row=%+v err=%v", townID, areaID, row, err)
			}
			if session.channelReconnect || !session.sceneBootstrapTailSent {
				t.Fatal("raid reconnect did not finish")
			}
			after, _, err := repositories.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			if after.Stats["town_id"] != wantTown || after.Stats["area_id"] != 0 || after.Stats["pos_x"] != 474 || after.Stats["pos_y"] != 234 {
				t.Fatal("raid landing was not durably committed for the subsequent door request")
			}
		}
	}
}

func TestManualRaidReconnectDoesNotChangeOrdinaryLoginPolicy(t *testing.T) {
	_, session, _ := newChannelReconnectTest(t)
	session.channel = channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType, Port: 10023}
	session.residentChannel = channelcatalog.Channel{ID: 11, Type: 11, Port: 10011}
	session.rosterRequested = true
	if markCurrentChannelReconnect(session, currentChannelReconnectProbeSize) || markCurrentChannelReconnectOnSelection(session) {
		t.Fatal("role selection mistaken for manual switch")
	}
	if session.residentChannel.ID != 11 {
		t.Fatal("ordinary login landing changed")
	}
	session.rosterRequested = false
	session.channel = channelcatalog.Channel{ID: 508, Type: dnfenum.HiddenRaidType}
	armCurrentChannelReconnect(session)
	if session.residentChannel.ID != 11 {
		t.Fatal("oversized raid owner silently truncated")
	}
}
