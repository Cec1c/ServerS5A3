package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestSeekerQST022RealPVFAcceptDeathAndHandIn(t *testing.T) {
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
	definition, ok := catalog.Find(2949)
	if !ok || definition.Type != "[seeking]" || !reflect.DeepEqual(definition.IntData, []int64{10164883, 1}) {
		t.Fatalf("quest identity=%+v", definition)
	}
	if len(definition.EnemyRewardItems) != 2 {
		t.Fatalf("enemy rules=%+v", definition.EnemyRewardItems)
	}
	for i, entry := range definition.EnemyRewardItems {
		if entry.EnemyType != 2 || entry.EnemyCode != 323 || entry.DungeonID != 42 || entry.Difficulty != int64(i+1) || entry.ItemID != 10164883 || entry.Count != 1 || entry.MaxStack != 1 || entry.DropRate != 100 {
			t.Fatalf("enemy rule=%+v", entry)
		}
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
	for _, difficulty := range []byte{1, 2} {
		t.Run(string(rune('0'+difficulty)), func(t *testing.T) {
			repos := qst006Repositories(t, ctx)
			character, _, err := repos.Character.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			character.Level = 45
			character.Stats["exp"] = 0
			if err = repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			sp, err := progress.TotalSkillPoints(45)
			if err != nil {
				t.Fatal(err)
			}
			tp, err := progress.TotalTechniquePoints(45)
			if err != nil {
				t.Fatal(err)
			}
			if err = repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "19", Points: dnfrepo.SkillPointState{SyncedLevel: 45, TotalSP: sp, RemainingSP: sp, TotalTP: tp, RemainingTP: tp}}); err != nil {
				t.Fatal(err)
			}
			if err = repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{2948: {Status: "completed"}}}); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfquest.NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 45, Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: qst006AccountID, SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 2949})); err != nil {
				t.Fatal(err)
			}
			conn, svc, session := qst006Bridge(repos, catalog, progress, items)
			svc.worldMapTable, svc.worldMapResolver, svc.dungeonMonsterTable, svc.dungeonAICharacterTable = table, resolver, monsters, ai
			svc.pvfItemCatalog = items
			svc.dungeonChoice = func(int) (int, error) { return 0, nil }
			svc.dungeonSeed = func() (uint32, error) { return 1, nil }
			rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 42, Difficulty: difficulty})
			if err != nil {
				t.Fatal(err)
			}
			session.dungeon.runtime = rt
			layout, err := worldmap.BuildDungeonLayout(resolver, 42, rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
			if err != nil {
				t.Fatal(err)
			}
			var coordinate worldmap.RoomCoordinate
			found := false
			for _, room := range rt.Session.Rooms() {
				for _, actor := range room.Map.Map.AICharacters {
					if actor.Code == 323 {
						coordinate, found = room.Coordinate, true
					}
				}
			}
			if !found {
				t.Fatalf("real APC323 room missing: maze=%d", rt.MazeIndex)
			}
			// Intermediate room clearing is supplied by the existing smoke helper;
			// target APC death, inventory refresh and quest hand-in use bridge handlers.
			for _, p := range realDungeonPathToRoom(t, layout, start.Coordinate, coordinate)[1:] {
				clearCurrentRealPVFSmokeRoom(t, rt)
				body := make([]byte, dungeoncmd.MoveMapRequestSize)
				body[0], body[1] = byte(p.X), byte(p.Y)
				if err = svc.handleDungeonMoveMap(session, body); err != nil {
					t.Fatal(err)
				}
			}
			var objectKey uint32
			for _, actor := range rt.Room.Snapshot().ExtendedActors {
				if actor.AICharacter != nil && actor.AICharacter.Code == 323 {
					objectKey = actor.ObjectKey
				}
			}
			if objectKey == 0 {
				t.Fatal("APC323 runtime object missing")
			}
			count := func(itemID int64) int64 {
				inventory, _, e := repos.Inventory.Load(ctx, "19")
				if e != nil {
					t.Fatal(e)
				}
				var n int64
				for _, stack := range inventory.Slots {
					if stack.ItemID == itemID {
						n += stack.Count
					}
				}
				return n
			}
			if count(10164883) != 0 {
				t.Fatal("quest item present before death")
			}
			body := seekerQST022DeathBody(objectKey)
			binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(19))
			if err = svc.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if n := count(10164883); n != 1 {
				t.Fatalf("death item count=%d", n)
			}
			if err = svc.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if n := count(10164883); n != 1 {
				t.Fatalf("repeat item count=%d", n)
			}
			// The client reports the now-satisfied item condition via its normal op33.
			qst006CallTrigger(t, svc, session, conn, 2949, 0)
			packets := qst006CallFinish(t, svc, session, conn, 2949)
			qst006AssertFinishSuccess(t, packets, 2949)
			if n := count(10164883); n != 0 {
				t.Fatalf("hand-in item count=%d", n)
			}
			if n := count(10149512); n != 4 {
				t.Fatalf("reward count=%d", n)
			}
			qst006CallFinish(t, svc, session, conn, 2949)
			if n := count(10149512); n != 4 {
				t.Fatalf("repeat reward count=%d", n)
			}
			record, _, err := repos.Quest.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if record.States[2949].Status != "completed" {
				t.Fatalf("quest=%+v", record.States[2949])
			}
			t.Logf("difficulty=%d maze=%d: accepted, APC323 death gives one, callback and hand-in consume one, reward once", difficulty, rt.MazeIndex)
		})
	}
}
