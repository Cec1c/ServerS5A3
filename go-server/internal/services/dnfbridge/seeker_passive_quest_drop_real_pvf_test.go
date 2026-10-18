package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestSeekerPassiveRealPVFAcceptCollectAndFinish(t *testing.T) {
	source, table, resolver := loadRealStoryPVF(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
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
	for _, mode := range []string{"legacy", "register"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(mapOwnedPassiveObjectModeEnvName, mode)
			for _, qid := range []int64{3735, 3954, 3955} {
				t.Run(fmt.Sprint(qid), func(t *testing.T) {
					def, ok := catalog.Find(qid)
					if !ok || def.Type != "[seeking]" || len(def.IntData) != 2 {
						t.Fatalf("definition=%+v", def)
					}
					itemID, required := def.IntData[0], def.IntData[1]
					codes := map[int64]bool{}
					var dungeonID int64
					for _, r := range def.EnemyRewardItems {
						if r.EnemyType != 3 || r.ItemID != itemID || r.Count != 1 || r.DropRate != 100 || r.MaxStack != required {
							t.Fatalf("rule=%+v", r)
						}
						codes[r.EnemyCode] = true
						dungeonID = r.DungeonID
					}
					repos := qst006Repositories(t, ctx)
					character, _, err := repos.Character.Load(ctx, "19")
					if err != nil {
						t.Fatal(err)
					}
					character.Level = def.LevelMin
					character.Stats["exp"] = 0
					if err = repos.Character.Save(ctx, character); err != nil {
						t.Fatal(err)
					}
					sp, err := progress.TotalSkillPoints(def.LevelMin)
					if err != nil {
						t.Fatal(err)
					}
					tp, err := progress.TotalTechniquePoints(def.LevelMin)
					if err != nil {
						t.Fatal(err)
					}
					if err = repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "19", Points: dnfrepo.SkillPointState{SyncedLevel: def.LevelMin, TotalSP: sp, RemainingSP: sp, TotalTP: tp, RemainingTP: tp}}); err != nil {
						t.Fatal(err)
					}
					states := map[int64]dnfrepo.QuestState{}
					for _, id := range def.PreRequiredGroups[0] {
						states[id] = dnfrepo.QuestState{Status: "completed"}
					}
					if err = repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: states}); err != nil {
						t.Fatal(err)
					}
					owner, err := dnfquest.NewOwner(repos)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: def.LevelMin, Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: qst006AccountID, SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: uint16(qid)})); err != nil {
						t.Fatal(err)
					}
					conn, svc, session := qst006Bridge(repos, catalog, progress, items)
					svc.worldMapTable, svc.worldMapResolver, svc.dungeonMonsterTable, svc.dungeonAICharacterTable = table, resolver, monsters, ai
					svc.pvfItemCatalog = items
					svc.dungeonChoice = func(int) (int, error) { return 0, nil }
					svc.dungeonSeed = func() (uint32, error) { return 1, nil }
					count := func() int64 {
						inventory, _, e := repos.Inventory.Load(ctx, "19")
						if e != nil {
							t.Fatal(e)
						}
						return currentQuestDropHeldCount(inventory, itemID)
					}
					if count() != 0 {
						t.Fatal("preexisting task material")
					}
					seq := uint16(100)
					var ordinary, special int
					for run := 0; run < 4 && count() < required; run++ {
						rt, _, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: uint32(dungeonID), Difficulty: 1})
						if err != nil {
							t.Fatal(err)
						}
						session.dungeon.runtime = rt
						layout, err := worldmap.BuildDungeonLayout(resolver, dungeonID, rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
						if err != nil {
							t.Fatal(err)
						}
						beforeRun := count()
						for _, target := range rt.Session.Rooms() {
							present := false
							for _, o := range target.Map.Map.PassiveObjects {
								if codes[o.ObjectID] {
									present = true
								}
							}
							for _, o := range target.Map.Map.SpecialPassiveObjects {
								if codes[o.ObjectID] {
									present = true
								}
							}
							if !present {
								continue
							}
							current, ok := rt.Session.Scene()
							if !ok {
								t.Fatal("missing scene")
							}
							// Intermediate clears are fixtures; map selection, movements, reports,
							// inventory transactions, trigger and finish use the production owners.
							for _, next := range realDungeonPathToRoom(t, layout, current.Coordinate, target.Coordinate)[1:] {
								clearCurrentRealPVFSmokeRoom(t, rt)
								move := make([]byte, dungeoncmd.MoveMapRequestSize)
								move[0], move[1] = byte(next.X), byte(next.Y)
								if err = svc.handleDungeonMoveMap(session, move); err != nil {
									t.Fatal(err)
								}
							}
							scene, ok := rt.Session.Scene()
							if !ok {
								t.Fatal("missing target scene")
							}
							if mode == "register" {
								rt.registerCurrentDungeonMapOwnedPassiveObjects(scene)
							}
							var all []int64
							for _, o := range scene.PassiveObjects {
								if codes[o.ObjectID] {
									all = append(all, o.ObjectID)
									ordinary++
								}
							}
							for _, o := range scene.SpecialPassiveObjects {
								if codes[o.ObjectID] {
									all = append(all, o.ObjectID)
									special++
								}
							}
							for _, code := range all {
								if count() >= required {
									break
								}
								before := count()
								room := rt.Room.Snapshot()
								frame, e := dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketDieMonster), seekerPassiveDropNativeBody(code), seq, dnfproto.DefaultChannelClassification)
								if e != nil {
									t.Fatal(e)
								}
								conn.write.Reset()
								if e = svc.handleGameUpper(session, frame); e != nil {
									t.Fatal(e)
								}
								if n := count(); n != before+1 {
									t.Fatalf("mode=%s quest=%d room=%s code=%d held=%d want=%d", mode, qid, scene.Coordinate.String(), code, n, before+1)
								}
								if !reflect.DeepEqual(room, rt.Room.Snapshot()) {
									t.Fatal("passive quest item changed room actors/clear state")
								}
								if e = svc.handleGameUpper(session, frame); e != nil {
									t.Fatal(e)
								}
								if count() != before+1 {
									t.Fatal("replayed report regranted")
								}
								seq++
							}
							if count() >= required {
								break
							}
						}
						if count() == beforeRun {
							t.Fatal("no task object in selected real layout")
						}
					}
					if count() != required {
						t.Fatalf("collection=%d want=%d", count(), required)
					}
					qst006CallTrigger(t, svc, session, conn, uint16(qid), 0)
					packets := qst006CallFinish(t, svc, session, conn, uint16(qid))
					qst006AssertFinishSuccess(t, packets, uint16(qid))
					expected := int64(0)
					if qid == 3735 {
						expected = 50
					}
					if count() != expected {
						t.Fatalf("after hand-in count=%d want=%d", count(), expected)
					}
					qst006CallFinish(t, svc, session, conn, uint16(qid))
					if count() != expected {
						t.Fatal("repeated finish changes inventory")
					}
					record, _, err := repos.Quest.Load(ctx, "19")
					if err != nil {
						t.Fatal(err)
					}
					if record.States[qid].Status != "completed" {
						t.Fatalf("state=%+v", record.States[qid])
					}
					t.Logf("quest=%d collected=%d ordinary_sources=%d special_sources=%d; normal accept, formal op39, callback and finish passed", qid, required, ordinary, special)
				})
			}
		})
	}
}
