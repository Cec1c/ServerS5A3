package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Regression: real definitions and current handlers, in-memory records.
// The callback sequence is a probe, not a captured native-client trace.
func TestSeekerQST002RealPVFProbe(t *testing.T) {
	source, table, resolver := loadRealStoryPVF(t)
	ctx := context.Background()
	idx, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, idx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3525, 3558, 3559, 3560} {
		d, ok := catalog.Find(id)
		if !ok {
			t.Fatalf("definition %d absent", id)
		}
		t.Logf("def %d path=%s type=%s subtype=%d int=%v main=%d noexp=%v reward=%s/%v parse=%q", id, d.Path, d.Type, d.SubType, d.IntData, d.MainQuestID, d.NoExperience, d.RewardType, d.RewardIntData, d.RewardParseError)
	}
	d, ok := table.FindDungeon(82)
	if !ok {
		t.Fatal("dungeon82 absent")
	}
	for _, m := range d.Mazes {
		if !realMazeConnectsQuest(m, 3525) && !realMazeConnectsQuest(m, 3560) {
			continue
		}
		t.Logf("maze dungeon82 index=%d connections=%v maps=%+v boss=%+v layers=%+v", m.Index, m.QuestConnection, m.MapSpecifications, m.BossSpecifications, m.LayeredSpecifications)
		stages, stageErr := resolver.StoryStages(82, m.Index)
		t.Logf("story stages=%+v err=%v", stages, stageErr)
	}
	m, ok := table.FindMap(92247)
	if !ok {
		t.Fatal("map92247 absent")
	}
	t.Logf("map92247 path=%s monsters=%+v npc=%+v ai=%+v", m.Path, m.Monsters, m.NPCs, m.AICharacters)

	repos := dnfrepomemory.NewMemoryGroup()
	account := defaultAccountPrefix + "1"
	tables, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := tables.ThresholdToNext(64)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := tables.TotalSkillPoints(65)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := tables.TotalTechniquePoints(65)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: account, Job: "2", Level: 65, Stats: map[string]int64{"grow_type": 0, "exp": int64(exp), "fatigue": 156}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3524: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "19", Points: dnfrepo.SkillPointState{SyncedLevel: 65, TotalSP: sp, RemainingSP: sp, TotalTP: tp, RemainingTP: tp}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19"}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 65, Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: account, SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3525}))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("accept parent=%d trigger=%d", accepted.QuestID, accepted.InitTrigger)
	conn := &bufferConn{}
	svc := &Service{options: options{accountID: account, gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, questCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19}}
	logState := func(label string) dnfrepo.QuestRecord {
		r, _, e := repos.Quest.Load(ctx, "19")
		if e != nil {
			t.Fatal(e)
		}
		for _, id := range []int64{3525, 3558, 3559, 3560} {
			s := r.States[id]
			t.Logf("%s q=%d status=%s progress=%d", label, id, s.Status, s.ProgressValue)
		}
		return r
	}
	logState("accepted")
	for _, id := range []uint16{3558, 3559} {
		for _, q := range []uint16{id, 3525} {
			if err := svc.handleCurrentSetQuestTrigger(session, []byte{33, 0, byte(q), byte(q >> 8), 0, 0}); err != nil {
				t.Fatal(err)
			}
		}
		logState("dialogue")
	}
	before := logState("before-clear")
	finishInput := dnfquest.FinishCommitInput{AccountID: account, CharacterID: "19", QuestID: 3525, Multiplier: 1, CommittedAt: time.Now().UTC(), Progression: tables, AllocateItem: finishBridgeAllocator(nil)}
	selected, err := selectDungeonMaze(d.Mazes, 0, before, nil)
	if err != nil || selected.Index != 2 || selected.QuestID != 3560 {
		t.Fatalf("maze=%+v err=%v", selected, err)
	}
	wrong, err := catalog.PlanClearMapCompletion(before, dnfquest.ClearMapCompletionInput{DungeonID: 82, MapID: 92251, CompletionKey: "qst002/final-boss", CompletedAt: time.Now().UTC()})
	if err != nil || len(wrong.Completions) != 0 || wrong.Record.States[3560].ProgressValue != 1 {
		t.Fatalf("final boss control=%+v err=%v", wrong, err)
	}
	t.Log("final boss map92251 does not credit target map92247")

	t.Run("middle-room-must-credit", func(t *testing.T) {
		monsters, e := newPVFDungeonMonsterCatalog(source)
		if e != nil {
			t.Fatal(e)
		}
		ai, e := newPVFDungeonAICharacterCatalog(source)
		if e != nil {
			t.Fatal(e)
		}
		svc.worldMapTable, svc.worldMapResolver, svc.dungeonMonsterTable, svc.dungeonAICharacterTable = table, resolver, monsters, ai
		svc.dungeonChoice = func(int) (int, error) { return 0, nil }
		svc.dungeonSeed = func() (uint32, error) { return 1, nil }
		rt, start, e := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 82, Difficulty: 0})
		if e != nil {
			t.Fatal(e)
		}
		session.dungeon.runtime = rt
		layout, e := worldmap.BuildDungeonLayout(resolver, 82, 2, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
		if e != nil {
			t.Fatal(e)
		}
		target := worldmap.RoomCoordinate{X: 2, Y: 1}
		for _, p := range realDungeonPathToRoom(t, layout, start.Coordinate, target)[1:] {
			clearCurrentRealPVFSmokeRoom(t, rt)
			b := make([]byte, dungeoncmd.MoveMapRequestSize)
			b[0], b[1] = byte(p.X), byte(p.Y)
			if e := svc.handleDungeonMoveMap(session, b); e != nil {
				t.Fatal(e)
			}
		}
		scene := rt.Session.Snapshot().Scene
		if scene.Map.Map.ID != 92247 || scene.Boss || scene.Cleared {
			t.Fatalf("target room map=%d boss=%v clear=%v", scene.Map.Map.ID, scene.Boss, scene.Cleared)
		}
		for _, monster := range rt.Room.Snapshot().Monsters {
			b := storyProcDeathBody(monster.ObjectKey)
			binary.LittleEndian.PutUint16(b[4:], uint16(currentSceneActorObjectKey(19)))
			if e := svc.handleDungeonMonsterDeath(session, b); e != nil {
				t.Fatal(e)
			}
			if _, ok := rt.Room.DefeatedDeathTarget(monster.ObjectKey); !ok {
				t.Fatalf("monster death rejected key=%d", monster.ObjectKey)
			}
		}
		scene = rt.Session.Snapshot().Scene
		if !scene.Cleared {
			t.Fatal("target room is not cleared")
		}
		r, _, e := repos.Quest.Load(ctx, "19")
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("authoritative room-clear map=%d boss=%v cleared=%v run=%s child3560=%s/%d parent3525=%d", scene.Map.Map.ID, scene.Boss, scene.Cleared, rt.Session.Snapshot().Run.Status, r.States[3560].Status, r.States[3560].ProgressValue, r.States[3525].ProgressValue)
		if r.States[3560].ProgressValue != 0 {
			t.Error("QST-002: all six real target-room monsters died through handler, but clear-map child still has progress1")
		}
	})
	if err := repos.Quest.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	t.Run("parent-callback-must-not-finish-unresolved-child", func(t *testing.T) {
		if e := svc.handleCurrentSetQuestTrigger(session, []byte{33, 0, byte(3525 & 255), byte(3525 >> 8), 0, 0}); e != nil {
			t.Fatal(e)
		}
		r, _, e := repos.Quest.Load(ctx, "19")
		if e != nil {
			t.Fatal(e)
		}
		_, e = owner.ApplyFinishSettlement(ctx, catalog, finishInput)
		t.Logf("parent-only callback: parent=%d child=%d finish_err=%v active_snapshot=%x", r.States[3525].ProgressValue, r.States[3560].ProgressValue, e, buildCurrentActiveQuestSnapshotBody(r, true))
		if !errors.Is(e, dnfquest.ErrFinishQuestNotPending) {
			t.Fatalf("expected blocked finish, got %v", e)
		}
		if r.States[3525].ProgressValue == 0 {
			t.Error("QST-002: parent callback advertises zero despite active child3560 at1; finish correctly rejects")
		}
	})
	if err := repos.Quest.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	t.Run("target-credit-finishes-parent", func(t *testing.T) {
		plan, err := catalog.PlanClearMapCompletion(before, dnfquest.ClearMapCompletionInput{DungeonID: 82, MapID: 92247, CompletionKey: "qst002/probe", CompletedAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := repos.Quest.Save(ctx, plan.Record); err != nil {
			t.Fatal(err)
		}
		logState("after-clear")
		result, err := owner.ApplyFinishSettlement(ctx, catalog, finishInput)
		if err != nil {
			t.Fatal(err)
		}
		body, err := buildCurrentFinishQuestSuccessBody(result)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("finish committed=%v exp=%d items=%v body=%x", result.AtomicCommitted, result.ExperienceDelta, result.Items, body)
		logState("finished")
	})
}
