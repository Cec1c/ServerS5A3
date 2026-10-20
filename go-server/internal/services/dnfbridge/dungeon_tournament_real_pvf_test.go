package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档回归：`Script.pvf` 的 120 黄龙 / 121 青龙必须能被厂商口径的定义投影接受，
// 并且真的产出 16 队 4 轮 + 4 条 blocking 的 PathActors。
//
// 运行方式（必须显式给路径，否则 skip）：
//
//	$env:DNF_WORLDMAP_REAL_PVF_SMOKE='H:\...\Script.pvf'
//	go test -count=1 -run RealScriptPVFTournament -v ./internal/services/dnfbridge
func TestRealScriptPVFTournamentYellowAndBlueDragonProject(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to run the real tournament smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		dungeonID int64
		wantEntry int64
		wantName  string
		// 真档 `[tournament enemies]` 的 Strength 分布（权重，不是坐标）。
		wantStrength map[int64]int
	}{
		{
			dungeonID: 120, wantEntry: 4726, wantName: "黄龙大会",
			wantStrength: map[int64]int{900: 4, 600: 4, 300: 7},
		},
		{
			dungeonID: 121, wantEntry: 4727, wantName: "青龙大会",
			wantStrength: map[int64]int{900: 4, 600: 6, 300: 5},
		},
	}
	for _, test := range tests {
		t.Run(test.wantName, func(t *testing.T) {
			dungeon, ok := table.FindDungeon(test.dungeonID)
			if !ok {
				t.Fatalf("real PVF dungeon %d is missing", test.dungeonID)
			}
			if dungeon.Metadata.Name != test.wantName {
				t.Fatalf("dungeon %d name=%q want=%q", test.dungeonID, dungeon.Metadata.Name, test.wantName)
			}
			// `[tournament dungeon] 1` 是这 5 张图**唯一**声明过的锦标赛标识；
			// `ignore default dungeon clear` 在真档里**没有**出现在这 5 个 DGN
			// （见交付报告 PVF 取证）。
			if !dungeon.Metadata.Flags["tournament dungeon"] {
				t.Fatalf("dungeon %d lost the `[tournament dungeon]` flag", test.dungeonID)
			}
			if dungeon.Metadata.Flags["ignore default dungeon clear"] {
				t.Fatalf("dungeon %d unexpectedly declares `ignore default dungeon clear`", test.dungeonID)
			}
			items := dungeon.Metadata.RequiredItems
			if len(items) != 1 || items[0].ItemID != test.wantEntry ||
				items[0].Count != 1 || !items[0].ConsumeOnEntry {
				t.Fatalf("dungeon %d `[required item]` = %+v want {%d 1 true}", test.dungeonID, items, test.wantEntry)
			}
			if len(dungeon.Metadata.TournamentResultCards) != 6 {
				t.Fatalf("dungeon %d result cards = %+v", test.dungeonID, dungeon.Metadata.TournamentResultCards)
			}
			if len(dungeon.Metadata.TournamentClearExperiences) != 5 {
				t.Fatalf("dungeon %d clear reward exp = %+v", test.dungeonID, dungeon.Metadata.TournamentClearExperiences)
			}
			if len(dungeon.Metadata.TournamentRewardItemRates) == 0 {
				t.Fatalf("dungeon %d reward item rate is empty", test.dungeonID)
			}
			if dungeon.Metadata.TournamentResultCardMalformed ||
				dungeon.Metadata.TournamentRewardItemRateMalformed ||
				dungeon.Metadata.TournamentRewardExpMalformed {
				t.Fatalf("dungeon %d reward sections reported malformed", test.dungeonID)
			}

			topology, err := worldmap.BuildDungeonLayout(resolver, test.dungeonID, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
				return choice.Candidates[0].ID, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if topology.Start == nil {
				t.Fatal("tournament maze has no start room")
			}
			room, ok := topology.Room(*topology.Start)
			if !ok || room.Map == nil {
				t.Fatalf("start room %v unresolved", topology.Start)
			}
			mapValue := room.Map.Map
			if mapValue.TournamentDefinitionMalformed {
				t.Fatalf("real tournament map reported malformed: %+v", mapValue.Diagnostics)
			}
			if len(mapValue.TournamentEnemyCandidates) != tournamentNpcTeamCount {
				t.Fatalf("candidates = %d want %d", len(mapValue.TournamentEnemyCandidates), tournamentNpcTeamCount)
			}
			if len(mapValue.TournamentStartAreas) != tournamentRequiredStartAreas {
				t.Fatalf("start areas = %+v", mapValue.TournamentStartAreas)
			}
			for _, candidate := range mapValue.TournamentEnemyCandidates {
				if candidate.IsApc {
					t.Fatalf("dungeon %d `[monster]` batch must not carry APC candidates: %+v", test.dungeonID, candidate)
				}
				if candidate.PartyCount != 1 {
					t.Fatalf("candidate party count = %d want 1", candidate.PartyCount)
				}
			}
			// 权重只允许 {300, 600, 900}，且分布逐图不同（已按真档 dump 逐条核对）。
			counts := map[int64]int{}
			for _, candidate := range mapValue.TournamentEnemyCandidates {
				counts[candidate.Strength]++
			}
			if len(counts) != len(test.wantStrength) {
				t.Fatalf("dungeon %d strength values = %+v want %+v", test.dungeonID, counts, test.wantStrength)
			}
			for strength, want := range test.wantStrength {
				if counts[strength] != want {
					t.Fatalf("dungeon %d strength %d count=%d want=%d (all=%+v)",
						test.dungeonID, strength, counts[strength], want, counts)
				}
			}

			definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, nil)
			if !ok {
				t.Fatalf("real dungeon %d rejected by the vendor definition projection: %s", test.dungeonID, reason)
			}
			runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(uint32(test.dungeonID)))
			if !ok {
				t.Fatalf("real dungeon %d runtime rejected: %s", test.dungeonID, reason)
			}
			snapshot := runtime.snapshot()
			if len(snapshot.PathActors) != tournamentRoundCount*definition.PartyLimit {
				t.Fatalf("path actors = %d", len(snapshot.PathActors))
			}
			for index, actor := range snapshot.PathActors {
				if !actor.IsBlocking {
					t.Fatalf("path actor %d not blocking", index)
				}
			}
			if snapshot.PathActors[tournamentFinalRound-1].Type != 3 {
				t.Fatalf("final round monster actor type = %d want 3", snapshot.PathActors[tournamentFinalRound-1].Type)
			}
		})
	}
}

// 黑龙 122 走 `[apc]` 分支：本批不实现，但**定义层必须能把它判出来**，
// 且 42 个 APC code 全部要能在 `AICharacter/AICharacter.lst` 里解出等级
// （厂商 TournamentDungeonDefinition.cs:416-428 的硬门槛）。
func TestRealScriptPVFTournamentBlackDragonApcCodesResolve(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to run the real black dragon APC smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(122)
	if !ok {
		t.Fatal("real PVF dungeon 122 is missing")
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, 122, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
		return choice.Candidates[0].ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	room, ok := topology.Room(*topology.Start)
	if !ok || room.Map == nil {
		t.Fatal("black dragon start room unresolved")
	}
	mapValue := room.Map.Map
	if len(mapValue.TournamentEnemyCandidates) != 42 {
		t.Fatalf("black dragon candidates = %d want 42", len(mapValue.TournamentEnemyCandidates))
	}
	for _, candidate := range mapValue.TournamentEnemyCandidates {
		if !candidate.IsApc {
			t.Fatalf("black dragon candidate is not `[apc]`: %+v", candidate)
		}
	}
	catalog, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	resolver194 := func(code int64) (byte, bool) {
		definition, found, findErr := catalog.Find(code)
		if findErr != nil || !found {
			return 0, false
		}
		return definition.Level, true
	}
	definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, resolver194)
	if !ok {
		t.Fatalf("black dragon rejected by the vendor definition projection: %s", reason)
	}
	if len(definition.Candidates) != 42 {
		t.Fatalf("projected apc candidates = %d want 42", len(definition.Candidates))
	}
	for _, candidate := range definition.Candidates {
		if candidate.Kind != tournamentActorAICharacter || candidate.ActorType != 5 {
			t.Fatalf("apc candidate projection = %+v", candidate)
		}
	}
}
