package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 第 17 批任务 C：简单原段 127「遗忘之地无法进入」。
//
// 服务端这条线的**有界**执行路径是：客户端选图 → 进图准备（prepareDungeonRuntime，在
// 任何 ACK / 选择器包之前）→ BuildDungeonLayout 逐房间要一张地图。本文件只钉这条
// 准备链在真档 PVF 上读到什么、走到哪一步、以及它现在为什么停住。
//
// 真档出处（本文件读到的原始数据）：
//
//	dungeon/dungeon.lst              -> 4000 `Fun_test\Fun_test2.dgn`
//	dungeon/Fun_test/Fun_test2.dgn   -> [name] `遗忘之地`, [minimum required level] 25,
//	                                    [limit party count] 1, [no fatigue],
//	                                    [enter without fatigue] 1, [dungeon type] `lost land`,
//	                                    [size] 10 10, [start map] 9 6, [boss map] 9 0,
//	                                    [map specification] 两段（都落在同一个 maze）
//	worldmap/worldmap.lst            -> 101 `golden_road.wdm`（[name] `遗忘之地`, [dungeon] 4000 …）
//	map/map.lst 归属                  -> 11247/11248/11249 map/Lost_Land/LastTemple/LastTemple{,2,3}.map
//
// Before the pool classification fix, the boss at (9,0) failed during preparation.
// MAP documents 11204/11208/11237 declare [boss] and dungeon 4000 even though
// their filenames lack a boss marker. This test checks their existing pool contract,
// not a unique authored binding, actual client entry, or full lost-land rules.

const round17ForgottenDungeonID int64 = 4000

func round17ForgottenService(t *testing.T) (*Service, dnfrepo.Group) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify 遗忘之地 (dungeon 4000) against the published PVF")
	}
	service, repos, _ := newDungeonDailyCountTestService(t, path)
	return service, repos
}

func round17ForgottenDungeon(t *testing.T, service *Service) worldmap.Dungeon {
	t.Helper()
	dungeon, ok := service.worldMapTable.FindDungeon(round17ForgottenDungeonID)
	if !ok {
		t.Fatalf("real PVF dungeon %d (遗忘之地) is not indexed by dungeon/dungeon.lst", round17ForgottenDungeonID)
	}
	return dungeon
}

// TestRound17RealPVFForgottenLandCatalogPolicyAndAreaFacts 钉真档输入面：副本目录项、
// 进图资格的三条策略输入（等级 / 队伍上限 / 疲劳）、费用与次数面，以及它在世界地图上的归属。
// 这些值直接决定 prepareDungeonRuntime 的准入判据，改动真档或改动解析都会红。
func TestRound17RealPVFForgottenLandCatalogPolicyAndAreaFacts(t *testing.T) {
	service, _ := round17ForgottenService(t)
	dungeon := round17ForgottenDungeon(t, service)

	if dungeon.Path != "dungeon/Fun_test/Fun_test2.dgn" {
		t.Fatalf("dungeon %d path=%q, want the published `dungeon/Fun_test/Fun_test2.dgn`", round17ForgottenDungeonID, dungeon.Path)
	}
	if !dungeon.Metadata.MinimumRequiredLevel.Set || dungeon.Metadata.MinimumRequiredLevel.Value != 25 {
		t.Fatalf("minimum required level=%d(set=%t), want the published 25",
			dungeon.Metadata.MinimumRequiredLevel.Value, dungeon.Metadata.MinimumRequiredLevel.Set)
	}
	if !dungeon.Metadata.LimitPartyCount.Set || dungeon.Metadata.LimitPartyCount.Value != 1 {
		t.Fatalf("limit party count=%d(set=%t), want the published solo-only 1",
			dungeon.Metadata.LimitPartyCount.Value, dungeon.Metadata.LimitPartyCount.Set)
	}
	if !dungeon.Metadata.NoFatigue || !dungeon.Metadata.Flags["enter without fatigue"] {
		t.Fatalf("no fatigue=%t enter without fatigue=%t, want the published fatigue-free entry",
			dungeon.Metadata.NoFatigue, dungeon.Metadata.Flags["enter without fatigue"])
	}
	if got := strings.ToLower(strings.TrimSpace(dungeon.Metadata.DungeonType)); got != "lost land" {
		t.Fatalf("dungeon type=%q, want the published `lost land` family", dungeon.Metadata.DungeonType)
	}
	if len(dungeon.Mazes) != 1 {
		t.Fatalf("mazes=%d, want the single published maze of this `lost land` document", len(dungeon.Mazes))
	}
	maze := dungeon.Mazes[0]
	if maze.Start == nil || maze.Start.X != 9 || maze.Start.Y != 6 {
		t.Fatalf("start map=%v, want the published 9 6", maze.Start)
	}
	if maze.Boss == nil || maze.Boss.X != 9 || maze.Boss.Y != 0 {
		t.Fatalf("boss map=%v, want the published 9 0", maze.Boss)
	}

	// 费用面：真档这个 DGN 没有 `[required item]`，这条线因此不该扣任何入场件。
	if items := currentTournamentEntryItems(dungeon); len(items) != 0 {
		t.Fatalf("dungeon %d declares entry items %+v although the published document has no [required item]",
			round17ForgottenDungeonID, items)
	}
	// 次数面：遗忘之地既不在异次元裂缝共享池里，也没有被循环副本表声明。
	if service.currentDimensionGateDailyEntryEnabled(dungeon) {
		t.Fatalf("dungeon %d must not be gated by the dimension-gate daily pool", round17ForgottenDungeonID)
	}
	if _, declared, err := service.currentDungeonCycleDailyCountForDungeon(round17ForgottenDungeonID); err != nil || declared {
		t.Fatalf("dungeon %d cycle declaration=%t err=%v, want no declared daily cycle counter",
			round17ForgottenDungeonID, declared, err)
	}

	// 归属：这支副本在世界地图上属于区域 101 worldmap/golden_road.wdm（名字也是“遗忘之地”）。
	area, found := service.worldMapTable.FindAreaByDungeon(round17ForgottenDungeonID)
	if !found {
		t.Fatalf("dungeon %d is not owned by any real worldmap document", round17ForgottenDungeonID)
	}
	if area.ID != 101 || !strings.EqualFold(area.Path, "worldmap/golden_road.wdm") {
		t.Fatalf("area=%d path=%q, want the published 101 worldmap/golden_road.wdm", area.ID, area.Path)
	}
	owned := false
	for _, entry := range area.Dungeons {
		if entry.DungeonID == round17ForgottenDungeonID {
			owned = true
		}
	}
	if !owned {
		t.Fatalf("area %d (%s) does not list dungeon %d", area.ID, area.Path, round17ForgottenDungeonID)
	}

	// 资格面走**真实的** validateDungeonEntry：90 级单人 + 0 疲劳必须放行（真档 `[no fatigue]`），
	// 24 级（低于真档 25）必须按既有 ErrEntryLevelTooLow 拒绝。
	session := &gameSession{selectedCharacterID: 17}
	level90 := dnfrepo.CharacterRecord{CharacterID: "17", Level: 90, Stats: map[string]int64{"fatigue": 0}}
	if err := validateDungeonEntry(level90, dungeon, session); err != nil {
		t.Fatalf("level 90 solo entry rejected against the published fatigue-free policy: %v", err)
	}
	level24 := level90
	level24.Level = 24
	if err := validateDungeonEntry(level24, dungeon, session); !errors.Is(err, dnfdungeon.ErrEntryLevelTooLow) {
		t.Fatalf("level 24 entry err=%v, want the existing ErrEntryLevelTooLow gate", err)
	}
}

// The coordinate remains unresolved until layout selection uses the declared boss pool.
// This covers preparation only, not native lost-land gameplay or entry commit.
func TestRealPVFForgottenLandPreEntryUsesDeclaredBossPool(t *testing.T) {
	service, repos := round17ForgottenService(t)
	ctx := context.Background()
	_, resolver, err := service.dungeonWorldMap()
	if err != nil {
		t.Fatal(err)
	}

	// 一、世界地图拓扑面：除了首领坐标，其余房间都要有真档地图。
	topology, err := worldmap.BuildDungeonTopology(resolver, round17ForgottenDungeonID, 0)
	if err != nil {
		t.Fatalf("build topology for dungeon %d maze 0: %v", round17ForgottenDungeonID, err)
	}
	unresolved := topology.UnresolvedRooms()
	unresolvedCoordinates := map[string]bool{}
	for _, room := range unresolved {
		unresolvedCoordinates[room.Coordinate.String()] = true
	}
	if len(unresolved) != 2 || !unresolvedCoordinates["7:0"] || !unresolvedCoordinates["9:0"] {
		t.Fatalf("unresolved rooms=%+v, want exactly the published multi-candidate (7,0) room and the (9,0) [boss map] room", unresolved)
	}
	t.Logf("maze 0 rooms=%d resolvable=%d unresolved=%v",
		len(topology.Rooms()), len(topology.Rooms())-len(unresolved), unresolved)

	// The three normal LastTemple candidates remain explicitly bound to (7,0).
	candidates, err := resolver.ResolveCandidates(worldmap.ResolveRequest{
		DungeonID: round17ForgottenDungeonID, MazeIndex: 0, X: 7, Y: 0,
	})
	if err != nil {
		t.Fatalf("resolve candidates for the real (7,0) cluster: %v", err)
	}
	seen := map[int64]string{}
	for _, candidate := range candidates {
		seen[candidate.Map.ID] = candidate.Map.Path
	}
	for _, id := range []int64{11247, 11248, 11249} {
		if path, ok := seen[id]; !ok || !strings.Contains(strings.ToLower(path), "lost_land/lasttemple/") {
			t.Fatalf("real candidate %d missing from the (7,0) cluster: %+v", id, seen)
		}
	}
	// Direct coordinate resolution remains empty; the declared type pool supplies layout candidates.
	bossCandidates, bossErr := resolver.ResolveCandidates(worldmap.ResolveRequest{
		DungeonID: round17ForgottenDungeonID, MazeIndex: 0, X: 9, Y: 0,
	})
	if !errors.Is(bossErr, worldmap.ErrMapUnresolved) || len(bossCandidates) != 0 {
		t.Fatalf("(9,0) candidates=%v err=%v, want the observed zero-candidate boss room", bossCandidates, bossErr)
	}

	pool, err := resolver.PoolCandidates(worldmap.MapPoolRequest{DungeonID: 4000, MazeIndex: 0, FileType: worldmap.MapFileBoss})
	if err != nil {
		t.Fatal(err)
	}
	wantBoss := map[int64]bool{11204: true, 11208: true, 11237: true}
	if len(pool) != len(wantBoss) {
		t.Fatalf("boss pool=%v", pool)
	}
	for _, candidate := range pool {
		if !wantBoss[candidate.ID] || candidate.Type != "[boss]" {
			t.Fatalf("non-authoritative boss pool member: %+v", candidate)
		}
	}
	// Exercise each permitted pool choice without hardcoding the runtime's random result.
	for wanted := range wantBoss {
		layout, layoutErr := worldmap.BuildDungeonLayout(resolver, 4000, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
			if choice.Coordinate == (worldmap.RoomCoordinate{X: 9, Y: 0}) {
				return wanted, nil
			}
			return choice.Candidates[0].ID, nil
		})
		if layoutErr != nil {
			t.Fatalf("boss %d layout: %v", wanted, layoutErr)
		}
		room, ok := layout.Room(worldmap.RoomCoordinate{X: 9, Y: 0})
		if !ok || room.Map == nil || room.Map.Map.ID != wanted || room.Map.Source != worldmap.ResolutionTypePool || len(layout.UnresolvedRooms()) != 0 {
			t.Fatalf("boss %d layout room=%+v", wanted, room)
		}
		bound, ok := layout.Room(worldmap.RoomCoordinate{X: 5, Y: 7})
		if !ok || bound.Map == nil || bound.Map.Map.ID != 11204 {
			t.Fatalf("explicit 5,7 changed: %+v", bound)
		}
	}

	session := seedDungeonDailyCountCharacter(t, repos, "17", 17, "forgotten-account")
	bindDungeonSelectorOriginForTestAt(t, service, session, 107, 101, 250, 224)
	before, found, err := repos.Character.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load seeded character: found=%t err=%v", found, err)
	}
	runtime, scene, err := service.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{
		DungeonID: uint32(round17ForgottenDungeonID), Difficulty: 0,
	})
	if err != nil {
		t.Fatalf("real preparation still blocked: %v", err)
	}
	if runtime == nil || runtime.MazeIndex != 0 || scene.Map.Map.ID != 11200 {
		t.Fatalf("unexpected prepared runtime=%+v scene=%+v", runtime, scene)
	}

	// Preparation has not committed the active runtime or charged entry costs.
	// 既有的疲劳记账键（fatigue_display_update / fatigue_limit / fatigue_service_day_id /
	// fatigue_used）会在 prepareDungeonRuntime 的疲劳刷新里被写一次，这是改动前就有的行为，
	// 不是本项要钉的“入场消耗”；这里只钉“疲劳值本身没被扣、也没有落任何次数计数”。
	if sessionHasActiveDungeonRuntime(session) {
		t.Fatal("pre-entry preparation still committed an active dungeon runtime")
	}
	after, found, err := repos.Character.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("reload character: found=%t err=%v", found, err)
	}
	if before.Stats["fatigue"] != after.Stats["fatigue"] {
		t.Fatalf("pre-entry preparation charged fatigue: %d -> %d", before.Stats["fatigue"], after.Stats["fatigue"])
	}
	for _, key := range []string{
		"dimension_gate_daily_entry_day",
		"dimension_gate_daily_entry_used",
		"dimension_gate_daily_entry_extra_granted",
	} {
		if _, ok := after.Stats[key]; ok {
			t.Fatalf("pre-entry preparation wrote the daily counter %q; preparation must precede count commit", key)
		}
	}
	added := make([]string, 0, len(after.Stats))
	for key := range after.Stats {
		if _, ok := before.Stats[key]; !ok {
			added = append(added, key)
		}
	}
	if !reflect.DeepEqual(before.Stats, after.Stats) {
		t.Logf("pre-entry preparation stats delta=%v (only the pre-existing fatigue refresh is tolerated)", added)
	}
	if before.Level != after.Level {
		t.Fatalf("pre-entry preparation changed the character level: %d -> %d", before.Level, after.Level)
	}
}
