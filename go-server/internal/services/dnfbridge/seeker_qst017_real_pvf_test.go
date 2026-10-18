package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"reflect"
	"testing"
	"time"
)

func TestSeekerQST017RealPVFKillRewardAndHandIn(t *testing.T) {
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
	target, ok := catalog.Find(3687)
	if !ok || !reflect.DeepEqual(target.IntData, []int64{72, -1, 56158, 1}) || len(target.RewardItems) != 1 || target.RewardItems[0].ItemID != 10164766 {
		t.Fatalf("hunt definition=%+v", target)
	}
	follow, ok := catalog.Find(3688)
	if !ok || !reflect.DeepEqual(follow.IntData, []int64{10164766, 1, 1013}) {
		t.Fatalf("hand-in definition=%+v", follow)
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
	repos := qst006Repositories(t, ctx)
	char, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	char.Level = 77
	exp, err := progress.ThresholdToNext(76)
	if err != nil {
		t.Fatal(err)
	}
	char.Stats["exp"] = int64(exp)
	if err = repos.Character.Save(ctx, char); err != nil {
		t.Fatal(err)
	}
	sp, err := progress.TotalSkillPoints(77)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := progress.TotalTechniquePoints(77)
	if err != nil {
		t.Fatal(err)
	}
	if err = repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "19", Points: dnfrepo.SkillPointState{SyncedLevel: 77, TotalSP: sp, RemainingSP: sp, TotalTP: tp, RemainingTP: tp}}); err != nil {
		t.Fatal(err)
	}
	if err = repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3639: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 77, Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: qst006AccountID, SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3640})); err != nil {
		t.Fatal(err)
	}
	// The preceding 517 map-clear is supplied to its existing domain owner;
	// the 72 target death and both item settlements below use bridge handlers.
	if _, err = owner.ApplyClearMapCompletion(ctx, catalog, "19", dnfquest.ClearMapCompletionInput{DungeonID: 517, MapID: 91845, CompletionKey: "qst017/prior-room", CompletedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// A reward-bearing linked child is accepted explicitly by the client.
	if _, err = owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 77, Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: qst006AccountID, SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3687})); err != nil {
		t.Fatal(err)
	}
	if state := qst017State(t, repos); state.Status != "active" || state.ProgressValue != 1 {
		t.Fatalf("accepted child=%+v", state)
	}
	conn, svc, session := qst006Bridge(repos, catalog, progress, items)
	svc.worldMapTable, svc.worldMapResolver, svc.dungeonMonsterTable, svc.dungeonAICharacterTable = table, resolver, monsters, ai
	svc.dungeonChoice = func(int) (int, error) { return 0, nil }
	svc.dungeonSeed = func() (uint32, error) { return 1, nil }
	rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 72})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = rt
	layout, err := worldmap.BuildDungeonLayout(resolver, 72, rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	var coordinate worldmap.RoomCoordinate
	found := false
	for _, room := range rt.Session.Rooms() {
		if room.Map.Map.ID == 91857 {
			coordinate, found = room.Coordinate, true
		}
	}
	if !found {
		t.Fatalf("target map missing maze=%d rooms=%+v", rt.MazeIndex, rt.Session.Rooms())
	}
	for _, p := range realDungeonPathToRoom(t, layout, start.Coordinate, coordinate)[1:] {
		clearCurrentRealPVFSmokeRoom(t, rt)
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = byte(p.X), byte(p.Y)
		if err = svc.handleDungeonMoveMap(session, body); err != nil {
			t.Fatal(err)
		}
	}
	var key uint32
	for _, monster := range rt.Room.Snapshot().Monsters {
		if monster.Spawn.MonsterID == 56158 {
			key = monster.ObjectKey
		}
	}
	if key == 0 {
		t.Fatal("real boss56158 missing")
	}
	if err = qst017Death(svc, session, key); err != nil {
		t.Fatal(err)
	}
	state := qst017State(t, repos)
	if state.ProgressValue != 0 || state.Extra["reward_state"] != "pending" {
		t.Fatalf("real death quest=%+v", state)
	}
	count := func() int64 {
		inv, _, e := repos.Inventory.Load(ctx, "19")
		if e != nil {
			t.Fatal(e)
		}
		var n int64
		for _, slot := range inv.Slots {
			if slot.ItemID == 10164766 {
				n += slot.Count
			}
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("death reward count=%d", n)
	}
	packets := qst006CallFinish(t, svc, session, conn, 3687)
	qst006AssertFinishSuccess(t, packets, 3687)
	if n := count(); n != 1 {
		t.Fatalf("normal reward count=%d", n)
	}
	qst006CallFinish(t, svc, session, conn, 3687)
	if n := count(); n != 1 {
		t.Fatalf("repeat reward count=%d", n)
	}
	qst006CallTrigger(t, svc, session, conn, 3688, 0x10)
	qst006CallTrigger(t, svc, session, conn, 3688, 0x20)
	packets = qst006CallFinish(t, svc, session, conn, 3688)
	qst006AssertFinishSuccess(t, packets, 3688)
	if n := count(); n != 0 {
		t.Fatalf("hand-in count=%d", n)
	}
	record, _, err := repos.Quest.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if record.States[3688].Status != "completed" {
		t.Fatalf("hand-in state=%+v", record.States[3688])
	}
	t.Logf("maze=%d target=91857 monster=56158: death pending, reward once, 3688 consumes one", rt.MazeIndex)
}
