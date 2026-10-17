package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func TestSeekerQST014RealPVFAwakeningPassage(t *testing.T) {
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
	q, ok := quests.Find(989)
	if !ok || q.Name != "觉醒 - 狱血魔神 (2/4)" {
		t.Fatalf("quest989=%+v", q)
	}
	for _, last := range []bool{false, true} {
		t.Run(fmt.Sprintf("last_choice_%t", last), func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			for _, err := range []error{
				repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1", Job: "0", Level: 50, Stats: map[string]int64{"fatigue": 156, "grow_type": 3}}),
				repos.Quest.Save(ctx, repo.QuestRecord{CharacterID: "19", States: map[int64]repo.QuestState{988: {Status: "completed"}}}),
				repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			owner, err := dnfquest.NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.ApplyAccept(ctx, quests, dnfquest.CharacterEligibility{Level: 50, Job: 0, GrowType: 3}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 989}))
			if err != nil {
				t.Fatal(err)
			}
			svc := &Service{options: options{accountID: "dnf:1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai, questCatalog: quests, dungeonChoice: func(n int) (int, error) {
				if last {
					return n - 1, nil
				}
				return 0, nil
			}, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: fmt.Sprintf("qst014-%t", last), selectedCharacterID: 19}
			rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 53, Difficulty: 2})
			if err != nil {
				t.Fatal(err)
			}
			session.dungeon.runtime = rt
			// Install the lifecycle identity normally supplied by entry commit.
			// Timer progression and actor retirement below use production methods.
			rt.lifecycleToken, session.dungeon.runToken = 1, 1
			queue := newFakeCurrentDungeonDeathTimerQueue()
			svc.gameplayTimers = queue
			layout, err := worldmap.BuildDungeonLayout(resolver, 53, rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) {
				if last {
					return c.Candidates[len(c.Candidates)-1].ID, nil
				}
				return c.Candidates[0].ID, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(layout.Bosses) != 1 {
				t.Fatal("boss coordinate absent")
			}
			route := realDungeonPathToRoom(t, layout, start.Coordinate, layout.Bosses[0])
			for i, coordinate := range route {
				scene, _ := rt.Session.Scene()
				if scene.Coordinate != coordinate {
					_, planErr := svc.planCurrentDungeonMove(rt, dungeoncmd.MoveMapRequest{NextX: byte(coordinate.X), NextY: byte(coordinate.Y)})
					t.Fatalf("coordinate=%v want=%v preflight=%v", scene.Coordinate, coordinate, planErr)
				}
				if i == 0 {
					if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
						t.Fatal(err)
					}
				}
				if currentSuspiciousVillageElevatorScope(rt, scene, rt.Room.Snapshot()) {
					if err := svc.startCurrentSuspiciousVillageElevator(session, "qst014"); err != nil {
						t.Fatal(err)
					}
					if !rt.suspiciousVillageElevator.Started {
						t.Fatal("elevator did not start")
					}
					if i+1 < len(route) {
						next := route[i+1]
						if _, err := svc.planCurrentDungeonMove(rt, dungeoncmd.MoveMapRequest{NextX: byte(next.X), NextY: byte(next.Y)}); err == nil {
							t.Fatal("unfinished elevator allowed movement")
						}
					}
					ticks := 3
					if last {
						ticks = 4
					}
					for n := 0; n < ticks; n++ {
						svc.fireCurrentSuspiciousVillageElevatorTick(session, rt, rt.suspiciousVillageElevator.Generation, queue.Now())
					}
					for _, actor := range rt.Room.Snapshot().ExtendedActors {
						if actor.Kind == runtimeDungeonActorSpecialMonster && actor.Packet.Code == 56716 {
							body := storyProcDeathBody(actor.ObjectKey)
							binary.LittleEndian.PutUint16(body[4:], uint16(currentSceneActorObjectKey(19)))
							if err := svc.handleDungeonMonsterDeath(session, body); err != nil {
								t.Fatal(err)
							}
						}
					}
					if !rt.suspiciousVillageElevator.Completed {
						t.Fatalf("elevator state=%+v", rt.suspiciousVillageElevator)
					}
					t.Logf("elevator real actor retirement counter=%d state=%d", rt.suspiciousVillageElevator.Counter, rt.suspiciousVillageElevator.State)
				}
				for _, m := range rt.Room.Snapshot().Monsters {
					if m.State != runtimeDungeonMonsterAnnounced {
						continue
					}
					if !runtimeSceneContainsHostile(scene.ExpectedHostiles, m.Reference) {
						continue
					}
					body := storyProcDeathBody(m.ObjectKey)
					binary.LittleEndian.PutUint16(body[4:], uint16(currentSceneActorObjectKey(19)))
					if err := svc.handleDungeonMonsterDeath(session, body); err != nil {
						t.Fatal(err)
					}
					if _, ok := rt.Room.DefeatedDeathTarget(m.ObjectKey); !ok {
						t.Fatalf("map=%d death rejected key=%d code=%d", scene.Map.Map.ID, m.ObjectKey, m.Spawn.MonsterID)
					}
				}
				scene, _ = rt.Session.Scene()
				t.Logf("map=%d coordinate=%v cleared=%t expected=%v actors=%d", scene.Map.Map.ID, coordinate, scene.Cleared, scene.ExpectedHostiles, len(rt.Room.Snapshot().ExtendedActors))
				if !scene.Cleared {
					t.Fatalf("real ordinary deaths left room blocked map=%d remaining=%v", scene.Map.Map.ID, scene.ExpectedHostiles)
				}
				if i+1 < len(route) {
					next := route[i+1]
					body := make([]byte, dungeoncmd.MoveMapRequestSize)
					body[0], body[1] = byte(next.X), byte(next.Y)
					if err := svc.handleDungeonMoveMap(session, body); err != nil {
						t.Fatal(err)
					}
				}
			}
			record, _, err := repos.Quest.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			// Hunt-monster progress also consumes a native quest callback, which
			// this passage probe does not synthesize. Door clearance must not
			// silently finish the awakening task by itself.
			if s := record.States[989]; s.Status != "active" || s.ProgressValue != 1 {
				t.Fatalf("passage unexpectedly changed quest=%+v", s)
			}
		})
	}
}
