package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func realStoryQuestMazeIndex(dungeon worldmap.Dungeon, questID int64) int {
	for index := range dungeon.Mazes {
		connection := dungeon.Mazes[index].QuestConnection
		if len(connection) >= 2 && connection[0] == 0 && connection[1] == questID {
			return index
		}
	}
	return -1
}

// TestRealScriptPVFStoryNextDungeonUsesSuccessorQuestMaze pins the DOC-118 chain
// on the fixed real Script.pvf: the ElvenGard main-story step 3146
// (传说中的白化变异哥布林) is followed by 3147 (毒泉的主人), whose dungeon 6 is
// 猛毒雷鸣废墟. Both quests live in their dungeon's maze quest connection, and
// maze selection only picks that maze while the quest is active — so the
// 开始下个任务 entry has to start 3147 (through the op31 accept preflight) before it
// enters 6, otherwise it selects the dungeon's non-quest maze.
func TestRealScriptPVFStoryNextDungeonUsesSuccessorQuestMaze(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to run the real story-chain successor smoke")
	}
	ctx := context.Background()
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}

	dungeon3, ok := table.FindDungeon(3)
	if !ok {
		t.Fatal("real PVF dungeon 3 (幽暗密林) is missing")
	}
	if realStoryQuestMazeIndex(dungeon3, 3146) < 0 {
		t.Fatalf("real dungeon 3 must carry the 3146 quest maze: %+v", dungeon3.Mazes)
	}
	dungeon6, ok := table.FindDungeon(6)
	if !ok {
		t.Fatal("real PVF dungeon 6 (猛毒雷鸣废墟) is missing")
	}
	successorMaze := realStoryQuestMazeIndex(dungeon6, 3147)
	if successorMaze < 0 {
		t.Fatalf("real dungeon 6 must carry the 3147 quest maze: %+v", dungeon6.Mazes)
	}
	successorSeen := 0
	for _, successor := range catalog.Successors(3146) {
		if successor.ID == 3147 {
			successorSeen++
		}
	}
	if successorSeen != 1 {
		t.Fatalf("real PVF Successors(3146) must name 3147 exactly once, got %d", successorSeen)
	}

	// The state right after the 3146 clear: the successor quest is not active yet.
	cleared := dnfrepo.QuestRecord{CharacterID: "94", States: map[int64]dnfrepo.QuestState{
		3145: {Status: "completed"},
		3146: {Status: "completed"},
	}}
	for _, difficulty := range []byte{0, 1} {
		before, err := selectDungeonMaze(dungeon6.Mazes, difficulty, cleared, nil)
		if err != nil {
			t.Fatal(err)
		}
		if before.Index == successorMaze {
			t.Fatalf("repro precondition broken: inactive successor 3147 already selects the quest maze %d", successorMaze)
		}
	}

	// DOC-118's own character shape (神: job 8, level 50, grow type 5).
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "94", AccountID: "account-1", Job: "8", Level: 50,
		Stats: map[string]int64{"grow_type": 5, "fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, cleared); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountID: "account-1"},
		worldMapTable:      table,
		worldMapResolver:   resolver,
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{connID: "doc118-story-successor", selectedCharacterID: 94}
	if !service.acceptCurrentStorySuccessorQuest(session, 3147) {
		t.Fatal("real PVF: the eligible 3147 successor must be started before the entry")
	}
	record, found, err := repositories.Quest.Load(ctx, "94")
	if err != nil || !found {
		t.Fatalf("reload quest record found=%t err=%v", found, err)
	}
	if state := record.States[3147]; state.Status != "active" {
		t.Fatalf("successor quest state=%+v, want active", state)
	}
	for _, difficulty := range []byte{0, 1} {
		after, err := selectDungeonMaze(dungeon6.Mazes, difficulty, record, nil)
		if err != nil {
			t.Fatal(err)
		}
		if after.Index != successorMaze || after.QuestID != 3147 {
			t.Fatalf("difficulty=%d after starting 3147 the entry must select quest maze %d, got %+v",
				difficulty, successorMaze, after)
		}
	}

	// Negative control: without the prerequisite the successor must not be written.
	ineligible := dnfrepomemory.NewMemoryGroup()
	if err := ineligible.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "94", AccountID: "account-1", Job: "8", Level: 50,
		Stats: map[string]int64{"grow_type": 5, "fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ineligible.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "94", States: map[int64]dnfrepo.QuestState{
		3145: {Status: "completed"},
	}}); err != nil {
		t.Fatal(err)
	}
	ineligibleService := &Service{
		options:            options{accountID: "account-1"},
		worldMapTable:      table,
		worldMapResolver:   resolver,
		questCatalog:       catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return ineligible, true },
	}
	if ineligibleService.acceptCurrentStorySuccessorQuest(session, 3147) {
		t.Fatal("an ineligible 3147 must not be accepted")
	}
	stored, found, err := ineligible.Quest.Load(ctx, "94")
	if err != nil || !found {
		t.Fatalf("reload ineligible quest record found=%t err=%v", found, err)
	}
	if state, ok := stored.States[3147]; ok {
		t.Fatalf("an ineligible successor must not be written: state=%+v", state)
	}
}
