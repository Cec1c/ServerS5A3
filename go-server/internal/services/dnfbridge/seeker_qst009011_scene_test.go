package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// This is a scene/quest projection probe. Intermediate rooms are cleared by the
// existing smoke helper to reach the destination; it does not execute ACT/CMT
// or claim native combat/animation acceptance.
func TestSeekerQST009011RealPVFSceneContracts(t *testing.T) {
	source, table, resolver := loadRealStoryPVF(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ quest, prior, dungeon, target, level int64 }{
		{3188, 3187, 15, 57962, 23}, {3189, 3188, 17, 57970, 22}, {3190, 3189, 15, 57979, 23},
		{3212, 3211, 26, 53522, 30}, {3213, 3212, 26, 53529, 30}, {3214, 3213, 26, 53536, 30}, {3215, 3214, 26, 53543, 30},
		{3917, 2984, 148, 400225, 53},
	} {
		t.Run(fmt.Sprint(tc.quest), func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			for _, err := range []error{
				repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1", Job: "2", Level: int(tc.level), Stats: map[string]int64{"fatigue": 156}}),
				repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}),
				repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{tc.prior: {Status: "completed"}}}),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			owner, err := dnfquest.NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ApplyAccept(ctx, quests, dnfquest.CharacterEligibility{Level: int(tc.level), Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: uint16(tc.quest)})); err != nil {
				t.Fatal(err)
			}
			if tc.quest == 3917 {
				character, _, err := repos.Character.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				for k, v := range map[string]int64{"town_id": 7, "area_id": 3, "pos_x": 474, "pos_y": 234, "direction": 5, "area_state": 3} {
					character.Stats[k] = v
				}
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				if _, _, err := repos.DungeonPermission.UpsertMax(ctx, "19", 148, 4); err != nil {
					t.Fatal(err)
				}
			}
			svc := &Service{options: options{accountID: "dnf:1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai, questCatalog: quests, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: fmt.Sprintf("qst009011-%d", tc.quest), selectedCharacterID: 19}
			difficulty := byte(0)
			if tc.quest == 3917 {
				difficulty = 2
				bindDungeonSelectorOriginForTestAt(t, svc, session, 7, 3, 474, 234)
			}
			rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: uint32(tc.dungeon), Difficulty: difficulty})
			if err != nil {
				t.Fatal(err)
			}
			if tc.quest == 3917 {
				if err := freezeCurrentDungeonTownReturnOrigin(session, rt); err != nil {
					t.Fatal(err)
				}
			}
			session.dungeon.runtime = rt
			if !realMazeConnectsQuest(rt.Dungeon.Mazes[rt.MazeIndex], tc.quest) {
				t.Fatalf("wrong maze=%d", rt.MazeIndex)
			}
			layout, err := worldmap.BuildDungeonLayout(resolver, tc.dungeon, rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
			if err != nil {
				t.Fatal(err)
			}
			var target worldmap.RoomCoordinate
			found := false
			for _, room := range layout.Rooms() {
				if room.Map != nil && room.Map.Map.ID == tc.target {
					target = room.Coordinate
					found = true
				}
			}
			if !found {
				t.Fatalf("quest target map=%d absent", tc.target)
			}
			for _, coordinate := range realDungeonPathToRoom(t, layout, start.Coordinate, target)[1:] {
				clearCurrentRealPVFSmokeRoom(t, rt)
				b := make([]byte, dungeoncmd.MoveMapRequestSize)
				b[0], b[1] = byte(coordinate.X), byte(coordinate.Y)
				if err := svc.handleDungeonMoveMap(session, b); err != nil {
					t.Fatal(err)
				}
				scene, _ := rt.Session.Scene()
				if scene.Coordinate != coordinate {
					t.Fatalf("move to %+v remained %+v", coordinate, scene.Coordinate)
				}
			}
			scene, _ := rt.Session.Scene()
			if scene.Map.Map.ID != tc.target {
				t.Fatalf("map=%d want=%d", scene.Map.Map.ID, tc.target)
			}
			actors, err := currentDungeonStartMapActors(rt, scene)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[uint32]int{}
			for _, actor := range actors {
				counts[actor.Code]++
			}
			record, _, err := repos.Quest.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if state := record.States[tc.quest]; state.Status != "active" || state.ProgressValue != 1 {
				t.Fatalf("quest completed before target combat: %+v", state)
			}
			if tc.quest == 3190 && counts[63525] != 3 {
				t.Fatalf("Seghart phases=%d want3", counts[63525])
			}
			if tc.quest == 3214 && counts[55424] != 1 {
				t.Fatalf("Renny hostile APC=%d want1", counts[55424])
			}
			if tc.quest == 3215 && counts[107004987] != 1 {
				t.Fatalf("Lotus boss=%d want1", counts[107004987])
			}
			if tc.quest == 3917 {
				if max := currentDungeonMaxClearState(rt.Dungeon); max != 3 {
					t.Fatalf("real dungeon148 maximum=%d want3", max)
				}
				conn.write.Reset()
				if err := svc.sendCurrentDungeonResourceState(session, rt, "qst011_story_entry"); err != nil {
					t.Fatal(err)
				}
				assertAuthorityEntryState(t, conn.write.Bytes(), 148, 3)
				permissionBefore, _, err := repos.DungeonPermission.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				before := dnfrepo.CloneQuest(record)
				for _, kind := range []byte{0, 1, 2} {
					for _, state := range []byte{1, 0, 0} {
						conn.write.Reset()
						if err := svc.handleCurrentDungeonStoryPause(session, []byte{state, kind}); err != nil {
							t.Fatal(err)
						}
						packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
						want := []byte{0, 0, state, kind}
						binary.LittleEndian.PutUint16(want, currentSceneActorObjectKey(19))
						if packet.Header.MsgID != 170 || !reflect.DeepEqual(packet.Body, want) || len(rest) != 0 {
							t.Fatalf("pause packet=%+v body=%x tail=%x", packet.Header, packet.Body, rest)
						}
					}
				}
				after, _, err := repos.Quest.Load(ctx, "19")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("pause changed quest: err=%v record=%+v", err, after)
				}
				conn.write.Reset()
				if err := svc.handleDungeonGiveupGame(session, nil); err != nil {
					t.Fatal(err)
				}
				if !rt.townReturnPending || !rt.townReturnOp24Sent {
					t.Fatal("active story exit did not send pending town transition")
				}
				transition, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if transition.Header.MsgID != currentSceneTransitionMsgID || len(rest) != 0 {
					t.Fatalf("exit packet=%+v rest=%x", transition.Header, rest)
				}
				permissionAfter, _, err := repos.DungeonPermission.Load(ctx, "19")
				if err != nil || !reflect.DeepEqual(permissionBefore, permissionAfter) {
					t.Fatalf("story exit changed difficulty: %v %+v", err, permissionAfter)
				}
				for _, projectionSession := range []*gameSession{session, {conn: &bufferConn{}, selectedCharacterID: 19}} {
					entries, err := svc.currentDungeonPermissionSnapshotEntries(ctx, projectionSession)
					want := []currentDungeonResourceStateEntry{{DungeonID: 148, State: 3}}
					if err != nil || !reflect.DeepEqual(entries, want) {
						t.Fatalf("post exit/reconnect difficulty=%+v err=%v", entries, err)
					}
				}
				t.Log("actual dungeon148 entry=3 (real PVF cap); pause replies=170; exit=24; persisted=4 and reconnect projection=3")
			}
			t.Logf("quest=%d maze=%d start=%d/%v target=%d/%v active=%d actors=%v", tc.quest, rt.MazeIndex, start.Map.Map.ID, start.Coordinate, scene.Map.Map.ID, scene.Coordinate, record.States[tc.quest].ProgressValue, counts)
		})
	}
}
