package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestFourToSingleDungeonDepartureClearsAllRetiredClientRecords(t *testing.T) {
	for _, survivorID := range []uint16{99, 100, 101, 102} {
		t.Run(fmt.Sprint(survivorID), func(t *testing.T) {
			sessions := map[uint16]*gameSession{}
			s, leader, member, _, _ := partyDungeonDeathFixture(t, func(s *Service, l, m *gameSession) {
				if _, ok := s.bootstrapManagedRuntimePartyForSession(l); !ok {
					t.Fatal("bootstrap")
				}
				sessions[99], sessions[100] = l, m
				repos, _ := s.repositoryGroup()
				template, _, _ := repos.Character.Load(context.Background(), "100")
				snapshot, _ := s.runtimePartyManagerForService().SnapshotByUser(99, 0)
				for _, id := range []uint16{101, 102} {
					c := dnfrepo.CloneCharacter(template)
					c.CharacterID = fmt.Sprint(id)
					c.Name = fmt.Sprint(id)
					if err := repos.Character.Save(context.Background(), c); err != nil {
						t.Fatal(err)
					}
					if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: c.CharacterID, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
						t.Fatal(err)
					}
					peer := &gameSession{conn: &bufferConn{}, channel: l.channel, residentChannel: l.residentChannel}
					s.bindGameSessionCharacter(peer, id)
					identity, _ := s.boundGameSessionCharacterSnapshot(peer)
					row := snapshot.Members[1]
					row.UserID = id
					row.SessionGeneration = identity.generation
					row.State.UserID = id
					result := s.runtimePartyManagerForService().Join(snapshot.ID, party.RuntimePartyMember{UserID: id, SessionGeneration: identity.generation, State: row.State})
					if !result.OK {
						t.Fatal(result.Reason)
					}
					s.publishRuntimePartySnapshot(result.Party)
					sessions[id] = peer
				}
			})
			_ = member
			s.options.currentClientVersion = "official-24.9"
			instance := leader.dungeon.runtime.partyInstance
			for _, id := range []uint16{101, 102} {
				peer := sessions[id]
				runtime, _, err := s.prepareDungeonRuntime(context.Background(), peer, dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1})
				if err != nil {
					t.Fatal(err)
				}
				scene, ok := runtime.Session.Scene()
				if !ok {
					t.Fatal("scene")
				}
				if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
					t.Fatal(err)
				}
				peer.dungeon.runtime = runtime
				if got := s.adoptPartyDungeonInstance(peer, runtime, scene); got != instance {
					t.Fatal("separate instance")
				}
			}
			repos, _ := s.repositoryGroup()
			for id, peer := range sessions {
				peer.channel.ID = 11
				peer.townActorOwnerChannel = 0
				c, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(id))
				for key, value := range map[string]int64{"town_id": 7, "area_id": 3, "pos_x": 474, "pos_y": 234, "direction": 5, "area_state": 3} {
					c.Stats[key] = value
				}
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				peer.dungeon.runtime.Character = c
				enableTownMoveSkillProjection(t, s, repos, c.CharacterID)
				bindDungeonSelectorOriginForTestAt(t, s, peer, 7, 3, 474, 234)
				if err := freezeCurrentDungeonTownReturnOrigin(peer, peer.dungeon.runtime); err != nil {
					t.Fatal(err)
				}
			}
			// Leave the leader until last whenever it is not the surviving player.
			order := []uint16{}
			for _, id := range []uint16{100, 101, 102, 99} {
				if id != survivorID {
					order = append(order, id)
				}
			}
			survivor := sessions[survivorID]
			retained := survivor.dungeon.runtime
			for step, id := range order {
				for _, peer := range sessions {
					peer.conn.(*bufferConn).write.Reset()
				}
				leaving := sessions[id]
				if err := s.handleDungeonGiveupGame(leaving, nil); err != nil {
					t.Fatal(err)
				}
				if err := s.commitPendingDungeonReturnForSceneRequest(leaving, "four_to_single_confirmed"); err != nil {
					t.Fatal(err)
				}
				if _, found := s.runtimePartyManagerForService().SnapshotByUser(id, 0); found {
					t.Fatal("leaver retained")
				}
				frames := splitUpperFrames(t, survivor.conn.(*bufferConn).write.Bytes())
				clears := map[uint16]map[byte]bool{}
				clearedHP := false
				restored := false
				for _, frame := range frames {
					if frame.msgID == 9 && len(frame.body) == 10 && frame.body[9] == 3 {
						if restored {
							t.Fatal("clear after restored roster")
						}
						key := binary.LittleEndian.Uint16(frame.body[4:6])
						if clears[key] == nil {
							clears[key] = map[byte]bool{}
						}
						clears[key][frame.body[7]] = true
					}
					if frame.msgID == 153 && len(frame.body) == 1 && frame.body[0] == 0 {
						clearedHP = true
					}
					if frame.msgID == 9 && len(frame.body) > 10 && frame.body[9] == 0 {
						restored = true
					}
				}
				if step < 2 {
					if len(clears) != 0 || clearedHP {
						t.Fatal("cleared party with surviving remote players")
					}
				} else {
					for _, old := range []uint16{99, 100, 101, 102} {
						if !clears[old][0] || !clears[old][11] {
							t.Fatalf("retired record %d not cleared in both namespaces: %+v", old, clears)
						}
					}
					if !clearedHP || !restored {
						t.Fatal("singleton HUD reset or retained roster absent")
					}
					after, found := s.runtimePartyManagerForService().SnapshotByUser(survivorID, 0)
					if !found || len(after.Members) != 1 || after.Members[0].UserID != survivorID || after.Members[0].Slot != byte(survivorID-99) {
						t.Fatalf("singleton identity/slot changed: %+v", after)
					}
					assertDungeonSurvivorRetainsParty(t, survivor, id)
				}
				if survivor.dungeon.runtime != retained || retained.partyInstance != instance {
					t.Fatal("surviving runtime replaced")
				}
			}
		})
	}
}
