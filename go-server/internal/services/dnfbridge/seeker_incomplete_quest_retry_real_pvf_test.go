package dnfbridge

import (
	"context"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSeekerQST022RealPVFIncompleteSuccessorEligibility(t *testing.T) {
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
	dungeon, ok := table.FindDungeon(42)
	if !ok {
		t.Fatal("dungeon42 missing")
	}
	mazeIndex := -1
	for i, maze := range dungeon.Mazes {
		if len(maze.QuestConnection) >= 2 && maze.QuestConnection[0] == 0 && maze.QuestConnection[1] == 2949 {
			mazeIndex = i
		}
	}
	if mazeIndex < 0 {
		t.Fatal("2949 quest maze missing")
	}
	repos := qst006Repositories(t, ctx)
	_, svc, session := qst006Bridge(repos, catalog, nil, nil)
	svc.worldMapTable, svc.worldMapResolver = table, resolver
	rt := &runtimeDungeonState{Dungeon: dungeon, MazeIndex: mazeIndex}
	nextDungeonID, nextQuestID, found := svc.resolveNextStoryQuestDungeon(rt)
	if !found || nextDungeonID == 42 {
		t.Fatalf("successor dungeon=%d quest=%d found=%t", nextDungeonID, nextQuestID, found)
	}
	for _, state := range []dnfrepo.QuestState{
		{Status: "active", ProgressValue: 1},
		{Status: "active", ProgressValue: 0},
	} {
		if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{2948: {Status: "completed"}, 2949: state}}); err != nil {
			t.Fatal(err)
		}
		outcome, err := svc.acceptCurrentStorySuccessorQuest(session, nextQuestID)
		if outcome != storySuccessorAcceptIneligible || !isStorySuccessorPreflightRefusal(err) {
			t.Fatalf("incomplete current quest outcome=%v err=%v", outcome, err)
		}
	}
	t.Logf("real2949 maze=%d can propose dungeon%d/quest%d, but active current quest rejects successor acceptance; formal op72 regression covers same-dungeon fallback", mazeIndex, nextDungeonID, nextQuestID)
}
