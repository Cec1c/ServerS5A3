package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFN3PublicMasterSeriaPrivacy(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the current read-only PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{townCatalog: table}
	room, found := s.townArea(98, 0)
	if !found {
		t.Fatal("real PVF town98/0 absent")
	}
	t.Logf("PVF room=%+v", room)
	// Town references omit the native (R)/(F) resource prefix.
	for _, name := range []string{"map/gcontents/180621_fiendwar/town/(r)gate_fiend.map", "map/gcontents/180621_fiendwar/town/(f)gate_fiend.map"} {
		raw, err := archive.ReadText(name)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("resource=%s textSHA256=%x\n%s", name, sha256.Sum256([]byte(raw)), raw)
		if !strings.Contains(strings.ToLower(raw), "seria") {
			t.Fatalf("resource does not establish Seria template: %s", name)
		}
	}
	t.Run("map_identity", func(t *testing.T) {
		for _, path := range []string{room.MapPath, "map/" + room.MapPath, strings.ReplaceAll(strings.ToUpper(room.MapPath), "/", "\\")} {
			if got := townAreaInstanceForMapPath(path, 29); got != 29 {
				t.Errorf("real FiendWar room %s instance=%d, want29", path, got)
			}
		}
		for _, areaID := range []int64{1, 2} {
			area, ok := s.townArea(98, areaID)
			if !ok {
				t.Fatal("public PVF area missing", areaID)
			}
			if got := townAreaInstanceForMapPath(area.MapPath, 29); got != 0 {
				t.Errorf("non-Seria area98/%d marked private", areaID)
			}
			t.Logf("public/control area98/%d map=%s", areaID, area.MapPath)
		}
	})
	for _, sameParty := range []bool{false, true} {
		t.Run(map[bool]string{false: "strangers", true: "same_party"}[sameParty], func(t *testing.T) {
			s, p, repos := newTownMoveTest(t)
			s.townCatalog = table
			s.options.currentClientVersion, s.options.gameUpperBodyCodec = "official-24.9", gameUpperBodyCodecPlain
			s.onlinePlayers = newOnlinePlayerManager()
			channel := channelcatalog.Channel{ServerID: 1, ID: 244, Type: 42, Group: "fiendwar"}
			q := &gameSession{conn: &bufferConn{}}
			peers := []*gameSession{p, q}
			for i, peer := range peers {
				id := uint16(29 + i)
				peer.accountID = "dnf:" + strconv.Itoa(i+1)
				peer.selectedCharacterID, peer.townSceneReadyCharacterID = id, id
				peer.channel, peer.residentChannel = channel, channel
				peer.connectionTownActorOwnerChannel, peer.townActorOwnerChannel = 244, 244
				peer.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: id, TownID: 98, AreaID: 1, PositionX: 474, PositionY: 234, PositionValid: true}
				s.bindGameSessionCharacter(peer, id)
				// In-memory actors only; satisfy the actual PVF level/quest gates.
				c := dnfrepo.CharacterRecord{CharacterID: strconv.Itoa(int(id)), AccountID: peer.accountID, Name: "N3-fixture", Job: "0", Level: 90, Stats: map[string]int64{"town_id": 98, "area_id": 1, "pos_x": 474, "pos_y": 234, "direction": 5, "area_state": 3}}
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: c.CharacterID, States: map[int64]dnfrepo.QuestState{8641: {Status: "completed"}}}); err != nil {
					t.Fatal(err)
				}
			}
			state := alignedcmd.PartyState{PartyID: 1, UserID: 29, UserState: 1, IsLeader: true, MaxMembers: 4, Members: []alignedcmd.PartyMemberState{{UserID: 29, UserState: 1}, {UserID: 30, UserState: 1}}}
			if sameParty {
				for _, peer := range peers {
					storeRuntimePartyState(peer, state)
				}
			}
			move := func(peer *gameSession, area byte) []byte {
				t.Helper()
				peer.conn.(*bufferConn).write.Reset()
				packet, err := dnfproto.BuildChannelPacket(36, buildTownMoveRequest(98, area, 474, 234, 5), 1, 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.handleGameUpper(peer, packet); err != nil {
					t.Fatal(err)
				}
				info, ok := s.onlinePlayers.PlayerForCharacter(peer.selectedCharacterID)
				if !ok || info.AreaID != area {
					t.Fatalf("op36 failed before presence: %+v found=%t", info, ok)
				}
				return append([]byte(nil), peer.conn.(*bufferConn).write.Bytes()...)
			}
			move(p, 0)
			p.conn.(*bufferConn).write.Reset()
			data := move(q, 0)
			if s.onlinePlayers.PeerInSameArea(29, 30) {
				t.Error("different owners merged into same private-room scene")
			}
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Error("second entrant projected actor into first owner's room")
			}
			want, _ := buildCurrentSceneTransitionBody(98, 0, []currentSceneTransitionRow{{ObjectOrResourceKey: 30, Value1: 474, Value2: 234, Value3: 5, Value4: 3}})
			transitions := 0
			for _, packet := range splitAllCurrentGameServerUpperPackets(t, data) {
				if packet.Header.MsgID == currentSceneTransitionMsgID {
					transitions++
					if !bytes.Equal(packet.Body, want) {
						t.Errorf("op24 includes another owner's actor: %x", packet.Body)
					}
				}
				if packet.Header.MsgID == 2 {
					t.Error("private-room arrival rebuilt foreign party actor via mode0/mode1")
				}
			}
			if transitions != 1 {
				t.Errorf("op24 count=%d", transitions)
			}
			for _, peer := range peers {
				info, _ := s.onlinePlayers.PlayerForCharacter(peer.selectedCharacterID)
				if info.Instance != uint32(peer.selectedCharacterID) {
					t.Errorf("owner%d instance=%d", peer.selectedCharacterID, info.Instance)
				}
			}
			p.conn.(*bufferConn).write.Reset()
			q.conn.(*bufferConn).write.Reset()
			packet, err := dnfproto.BuildChannelPacket(35, []byte{0xde, 1, 0xea, 0, 5, 0, 0}, 2, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameUpper(q, packet); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Error("private-room position relayed to another owner")
			}
			var chat packetWriter
			chat.writeByte(3)
			chat.writeUint16(30)
			chat.writeUint32(0)
			chat.writeRawDstr([]byte("N3"))
			if err := s.handleCurrentPeerChat(q, 17, chat.bytes()); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Error("private-room area chat relayed to another owner")
			}
			// Leaving both private rooms restores public co-presence, even in type42.
			move(p, 1)
			data = move(q, 1)
			if !s.onlinePlayers.PeerInSameArea(29, 30) {
				t.Fatal("public town area lost co-presence")
			}
			for _, packet := range splitAllCurrentGameServerUpperPackets(t, data) {
				if packet.Header.MsgID == currentSceneTransitionMsgID && (len(packet.Body) < 4 || binary.LittleEndian.Uint16(packet.Body[2:4]) != 2) {
					t.Errorf("public area roster must retain two actors: %x", packet.Body)
				}
			}
			p.conn.(*bufferConn).write.Reset()
			q.conn.(*bufferConn).write.Reset()
			if err := s.handleTownSetUserPosition(q, []byte{0xde, 1, 0xea, 0, 5, 0, 0}); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() == 0 {
				t.Error("public town movement silenced")
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentPeerChat(q, 17, chat.bytes()); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() == 0 {
				t.Error("public town chat silenced")
			}
			if sameParty {
				for _, peer := range peers {
					if got := runtimePartyStateSnapshot(peer); got.PartyID != state.PartyID || len(runtimePartyMembers(got)) != 2 {
						t.Error("private-room move changed party membership")
					}
				}
			}
			// Establish both private scenes through the public Upper/op36 owner.
			// No FiendWar-specific login/configuration helper is required.
			for _, peer := range peers {
				move(peer, 0)
				if err := s.handleTownSetUserPosition(peer, []byte{0xda, 1, 0xea, 0, 5, 0, 0}); err != nil {
					t.Fatal(err)
				}
			}

			if sameParty {
				t.Run("return_party_fallback", func(t *testing.T) {
					for _, peer := range peers {
						peer.conn.(*bufferConn).write.Reset()
						if !currentPartyTownReturnSceneReady(peer) {
							t.Fatal("return fixture not ready")
						}
					}
					if err := s.rebuildRuntimePartyTownCoPresenceAfterReturn(p, "N3_private_return"); err != nil {
						t.Fatal(err)
					}
					for _, peer := range peers {
						hasRoster := false
						for _, packet := range splitAllCurrentGameServerUpperPackets(t, peer.conn.(*bufferConn).write.Bytes()) {
							if packet.Header.MsgID == 2 || packet.Header.MsgID == currentTownUserAreaNotificationMsgID {
								t.Errorf("return fallback projected other private owner: op%d", packet.Header.MsgID)
							}
							if packet.Header.MsgID == 9 {
								hasRoster = true
							}
						}
						if !hasRoster {
							t.Error("private-room return lost party roster")
						}
					}
				})
			}
			t.Run("level_and_quest_gates_preserved", func(t *testing.T) {
				move(q, 1)
				c, ok, err := repos.Character.Load(context.Background(), "30")
				if !ok || err != nil {
					t.Fatal(err)
				}
				for _, missing := range []string{"level", "quest"} {
					c.Level = 90
					quests := dnfrepo.QuestRecord{CharacterID: "30", States: map[int64]dnfrepo.QuestState{8641: {Status: "completed"}}}
					if missing == "level" {
						c.Level = 89
					} else {
						quests.States = map[int64]dnfrepo.QuestState{}
					}
					if err := repos.Character.Save(context.Background(), c); err != nil {
						t.Fatal(err)
					}
					if err := repos.Quest.Save(context.Background(), quests); err != nil {
						t.Fatal(err)
					}
					packet, err := dnfproto.BuildChannelPacket(36, buildTownMoveRequest(98, 0, 474, 234, 5), 3, 1)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.handleGameUpper(q, packet); err != nil {
						t.Fatal(err)
					}
					info, ok := s.onlinePlayers.PlayerForCharacter(30)
					if !ok || info.AreaID != 1 {
						t.Errorf("missing %s bypassed room admission", missing)
					}
				}
			})
		})
	}
}
