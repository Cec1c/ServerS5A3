package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestSeekerRealPVFNorthgateAfterLevel67Coupon(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	ctx := context.Background()
	archive, err := platformpvf.Open(path)
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
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	apcs, err := newPVFDungeonAICharacterCatalog(archive)
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
	initial := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3528: {Status: "active", ProgressValue: 1}}}
	plan, err := catalog.PlanCouponMainQuestClear(initial, dnfquest.CharacterEligibility{Level: 67, Job: 0, GrowType: 1}, 67, dnfquest.CouponClearScopeMainStorySweep, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name   string
		quests dnfrepo.QuestRecord
		maze   int
	}{
		{"active", initial, 1}, {"coupon", plan.Record, 0}, {"empty", dnfrepo.QuestRecord{CharacterID: "19"}, 0},
		{"active3529", dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3529: {Status: "active", ProgressValue: 1}}}, 2},
		{"active3530", dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3530: {Status: "active", ProgressValue: 1}}}, 3},
		{"active3533", dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3533: {Status: "active", ProgressValue: 1}}}, 4},
		{"active2957", dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{2957: {Status: "active", ProgressValue: 1}}}, 5},
	} {
		for difficulty := byte(0); difficulty < 4; difficulty++ {
			t.Run(fmt.Sprintf("%s/difficulty%d", scenario.name, difficulty), func(t *testing.T) {
				repos := dnfrepomemory.NewMemoryGroup()
				if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 67, Job: "0", Stats: map[string]int64{"fatigue": 156, "grow_type": 1}}); err != nil {
					t.Fatal(err)
				}
				if err := repos.Quest.Save(ctx, scenario.quests); err != nil {
					t.Fatal(err)
				}
				svc := &Service{options: options{accountID: "account-1"}, initialEquipmentArchive: archive, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: apcs, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				run, scene, err := svc.prepareDungeonRuntime(ctx, &gameSession{selectedCharacterID: 19}, dungeoncmd.SelectDungeonRequest{DungeonID: 88, Difficulty: difficulty})
				if err != nil {
					t.Fatalf("northgate preparation: %v", err)
				}
				wantMaze := scenario.maze
				if scenario.name == "active2957" && difficulty < 2 {
					wantMaze = 0
				}
				if run.MazeIndex != wantMaze {
					t.Fatalf("maze=%d want=%d", run.MazeIndex, wantMaze)
				}
				packets, err := buildCurrentDungeonEntryPackets(run, scene)
				if err != nil {
					t.Fatalf("northgate entry packets: %v", err)
				}
				if len(packets.StartMap) == 0 || len(packets.DungeonInfo) == 0 {
					t.Fatal("entry must emit both dungeon info and start map")
				}
				if wantMaze == 0 {
					// 真正下发的op29与服务器演员必须同源于当前DGN的22428，
					// 不能只验证准备入口成功，或仍发送旧目录的11019/15063。
					if scene.Map.Map.ID != 22428 || len(run.Room.Snapshot().Monsters) != 5 ||
						len(packets.StartMap) < 19 || binary.LittleEndian.Uint32(packets.StartMap[14:18]) != 22428 || packets.StartMap[18] != 5 {
						t.Fatalf("普通北门首房及op29不符：map=%d monsters=%d body=%x", scene.Map.Map.ID, len(run.Room.Snapshot().Monsters), packets.StartMap)
					}
				}
				if difficulty == 2 && scenario.name != "coupon" && scenario.name != "active" {
					assertNorthgateRidableRoomRoundTrip(t, svc, run, packets.StartMap, resolver)
				}
				t.Logf("maze=%d map=%d", run.MazeIndex, scene.Map.Map.ID)
			})
		}
	}
}
