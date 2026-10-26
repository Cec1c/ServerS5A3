package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// R129 checks the transfer at the wire boundary that follows op29. The old
// 218 actor is replaced with an ordinary dungeon actor before the entry tail;
// all local writes therefore have to use the selected character key, and the
// container snapshot has to be installed after op29 and before the complete
// equipment-bearing mode1. This intentionally exercises the actual opened
// hatchery owner and real PVF instead of constructing either one.
func TestReviewHatcheryEntryR129PostStartMapWireContract(t *testing.T) {
	sourceCorners := map[uint32]struct {
		coordinate worldmap.RoomCoordinate
		mapID      int64
	}{
		222: {coordinate: worldmap.RoomCoordinate{X: 3, Y: 4}, mapID: 19119},
		223: {coordinate: worldmap.RoomCoordinate{X: 0, Y: 4}, mapID: 19116},
		224: {coordinate: worldmap.RoomCoordinate{X: 0, Y: 0}, mapID: 19100},
	}
	for _, dungeonID := range reviewAntonHatcheryPolicyIDs {
		dungeonID := dungeonID
		t.Run(fmt.Sprint(dungeonID), func(t *testing.T) {
			s, session, old, target := actualAntonHatchWarpOwnerFor(t, dungeonID)
			if target != dungeonID {
				t.Fatalf("fixture opened %d, want %d", target, dungeonID)
			}
			connection, ok := session.conn.(*bufferConn)
			if !ok {
				t.Fatalf("fixture connection type %T is not a buffer connection", session.conn)
			}
			connection.write.Reset()
			sourceCorner := sourceCorners[dungeonID]
			sourceScene, sourceOK := old.Session.Scene()
			if !sourceOK || sourceScene.Coordinate != sourceCorner.coordinate || sourceScene.Map.Map.ID != sourceCorner.mapID {
				t.Fatalf("218 transfer source scene=%s/%d, want corner=%s source_map=%d",
					sourceScene.Coordinate, sourceScene.Map.Map.ID, sourceCorner.coordinate, sourceCorner.mapID)
			}
			if err := s.handleDungeonSelectUpper(session, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{
				DungeonID:  dungeonID,
				Difficulty: 2,
			})); err != nil {
				t.Fatal(err)
			}

			runtime := session.dungeon.runtime
			if runtime == nil || runtime == old || runtime.Dungeon.ID != int64(dungeonID) {
				t.Fatalf("committed runtime=%+v old=%p target=%d", runtime, old, dungeonID)
			}
			scene, ok := runtime.Session.Scene()
			if !ok {
				t.Fatal("committed hatchery runtime has no scene")
			}
			if runtime.Character.CharacterID != fmt.Sprint(session.selectedCharacterID) || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
				t.Fatalf("target actor owner=%s selected=%d", runtime.Character.CharacterID, session.selectedCharacterID)
			}

			// The hard-coded transfer corner must be the actual start room of the
			// target dungeon, not merely a corner with the same map id in 218.
			dungeon, found := s.worldMapTable.FindDungeon(int64(dungeonID))
			if !found {
				t.Fatalf("target dungeon %d missing from real PVF", dungeonID)
			}
			topology, err := worldmap.BuildDungeonLayout(s.worldMapResolver, dungeon.ID, runtime.MazeIndex, func(choice worldmap.DungeonMapChoice) (int64, error) {
				return choice.Candidates[0].ID, nil
			})
			if err != nil {
				t.Fatalf("build target layout: %v", err)
			}
			if topology.Start == nil || scene.Coordinate != *topology.Start {
				t.Fatalf("committed scene room=%s, target PVF start=%v", scene.Coordinate, topology.Start)
			}
			targetRoom, found := topology.Room(scene.Coordinate)
			if !found || targetRoom.Map == nil || targetRoom.Map.Map.ID != scene.Map.Map.ID {
				t.Fatalf("committed scene room=%s map=%d disagrees with target PVF room: %+v", scene.Coordinate, scene.Map.Map.ID, targetRoom)
			}

			packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
			infoIndex, startIndex, placementIndex, userStateIndex := -1, -1, -1, -1
			firstPostStartMode1 := -1
			for index, packet := range packets {
				if packet.Header.Classification != 0 {
					continue
				}
				switch packet.Header.MsgID {
				case currentDungeonInfoNotification:
					if infoIndex >= 0 {
						t.Fatalf("duplicate op28 at %d and %d", infoIndex, index)
					}
					infoIndex = index
					if len(packet.Body) != 36 || binary.LittleEndian.Uint32(packet.Body[:4]) != dungeonID || packet.Body[4] != 2 {
						t.Fatalf("op28 body=%x, want dungeon=%d difficulty=2 and 36 bytes", packet.Body, dungeonID)
					}
				case currentDungeonStartNotification:
					if startIndex >= 0 {
						t.Fatalf("duplicate op29 at %d and %d", startIndex, index)
					}
					startIndex = index
					if len(packet.Body) < 22 || packet.Body[0] != byte(scene.Coordinate.X) || packet.Body[1] != byte(scene.Coordinate.Y) || binary.LittleEndian.Uint32(packet.Body[14:18]) != uint32(scene.Map.Map.ID) {
						t.Fatalf("op29 body=%x, want committed room=%s map=%d", packet.Body, scene.Coordinate, scene.Map.Map.ID)
					}
					// These real start maps have only MAP-owned passive object
					// 19117; no monster/extra/ridable rows belong in op29. Check
					// all count boundaries, not just its map-id prefix.
					if len(packet.Body) != 23 || !bytes.Equal(packet.Body[18:22], []byte{0, 0, 0, 0}) {
						t.Fatalf("hatchery op29 has unexpected actor/extra/fog/ridable rows: %x", packet.Body)
					}
				case uint16(dnfenum.CmdPacketSetUDPIPPort):
					if startIndex >= 0 && firstPostStartMode1 < 0 && len(packet.Body) > currentMode1CreateCountOffset && packet.Body[0] == 1 {
						firstPostStartMode1 = index
					}
				case uint16(dnfenum.CmdPacketRequestBlacklist):
					placementIndex = index
				case uint16(dnfenum.CmdPacketNotifyUserState):
					userStateIndex = index
				}
			}
			if infoIndex < 0 || startIndex < 0 || infoIndex >= startIndex {
				t.Fatalf("op28/op29 order info=%d start=%d", infoIndex, startIndex)
			}
			if firstPostStartMode1 < 0 || placementIndex < 0 || userStateIndex < 0 || firstPostStartMode1 >= placementIndex || placementIndex >= userStateIndex {
				t.Fatalf("post-op29 order full_mode1=%d op120=%d op3=%d", firstPostStartMode1, placementIndex, userStateIndex)
			}

			localKey := currentSceneActorObjectKey(session.selectedCharacterID)
			containerTypes := loginEquipmentBootstrapListTypes()
			containerAt := 0
			for index := startIndex + 1; index < firstPostStartMode1; index++ {
				packet := packets[index]
				if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) {
					continue
				}
				if len(packet.Body) == 0 || containerAt >= len(containerTypes) || packet.Body[0] != containerTypes[containerAt] {
					t.Fatalf("post-op29 pre-mode1 container[%d] body=%x want list=%v", containerAt, packet.Body, containerTypes)
				}
				containerAt++
			}
			if containerAt != len(containerTypes) {
				t.Fatalf("post-op29 inventory containers=%d, want %d complete ordered lists before full mode1", containerAt, len(containerTypes))
			}

			for index := startIndex + 1; index < len(packets); index++ {
				packet := packets[index]
				if packet.Header.Classification != 0 {
					continue
				}
				switch packet.Header.MsgID {
				case uint16(dnfenum.CmdPacketSetUDPIPPort):
					if len(packet.Body) > currentMode1CreateCountOffset && packet.Body[0] == 1 && binary.LittleEndian.Uint16(packet.Body[0x15:0x17]) != localKey {
						t.Fatalf("post-op29 mode1 actor key=%d, want selected key %d: body=%x", binary.LittleEndian.Uint16(packet.Body[0x15:0x17]), localKey, packet.Body)
					}
				case uint16(dnfenum.CmdPacketNotifyUserState):
					if !bytes.Equal(packet.Body, []byte{1, byte(localKey), byte(localKey >> 8), currentDungeonPlayerUserState}) {
						t.Fatalf("post-op29 op3 body=%x, want selected key %d", packet.Body, localKey)
					}
				case uint16(dnfenum.CmdPacketRecoverStamina):
					if len(packet.Body) >= 12 && packet.Body[9] == currentSceneOp9ActorDisplayKind && binary.LittleEndian.Uint16(packet.Body[4:6]) != localKey {
						t.Fatalf("post-op29 op9 actor key=%d, want selected key %d: body=%x", binary.LittleEndian.Uint16(packet.Body[4:6]), localKey, packet.Body)
					}
				}
			}
			if !session.preDungeonContextPlayerStateSent || !session.postStartMapPlayerStateSent || !session.selectedItemListRefreshSent || session.selectedItemListBootstrapCharacterID != session.selectedCharacterID {
				t.Fatalf("entry gates pre=%t post=%t items=%t item_owner=%d selected=%d",
					session.preDungeonContextPlayerStateSent, session.postStartMapPlayerStateSent,
					session.selectedItemListRefreshSent, session.selectedItemListBootstrapCharacterID, session.selectedCharacterID)
			}
		})
	}
}
