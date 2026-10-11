package dnfbridge

import (
	"context"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档 PVF 用例（`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）：`etc/hellparty.etc` 的解析与
// 波次装配必须在**真档**上成立。
//
// 为什么必须有这一条：合成 PVF 用的是解析器"以为"的语法，而深渊波次的真源是
// 一张我们**从来没有读过**的 etc（`_dist/hell-room-two-pillars-diagnosis.md` §2.1b）。
// 期望值全部来自真档逐行 dump（`_dist/_hellparty_etc_dump.txt`）与
// `dungeon_hell_room_test.go` 已钉住的 map 60083 事实：
//
//	[group index] 125 `A` 1050 0     126 `A` 1051 0     127 `A` 1052 0     128 `A` 1053 0
//	[group index] 211 `A` 65745 0 65746 0 65747 0
//	[group index] 212 `A` 65749 0 65750 0 65751 0
//	…213/214/215 见 dump
//	[difficulty] 4 `A` 8 5 100 100 0 `B` 6 4 0 0 0 `C` 4 3 0 0 0 `D` 0 3 0 0 1
//
// 运行：`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -run RealPVFHellPartyWave ./internal/services/dnfbridge`
func TestRealPVFHellPartyWaveTableAndMapGroupsResolve(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	catalog, err := newPVFDungeonHellPartyCatalog(archive)
	if err != nil {
		t.Fatalf("parse real etc/hellparty.etc: %v", err)
	}
	if catalog.Count() < 100 {
		t.Fatalf("real etc/hellparty.etc group count=%d, want the full table", catalog.Count())
	}

	// CosmoFiend 深渊怪物组（order 1）与 APC 小队组（order 2）。
	wantGroups := map[int64][]int64{
		125: {1050}, 126: {1051}, 127: {1052}, 128: {1053},
		211: {65745, 65746, 65747},
		212: {65749, 65750, 65751},
		213: {65745, 65746, 65749, 65750},
		214: {65745, 65747, 65749, 65751},
		215: {65746, 65747, 65750, 65751},
	}
	for groupID, wantCodes := range wantGroups {
		if !catalog.HasEntries(groupID, 1) {
			t.Fatalf("real group %d has no difficulty-A entries", groupID)
		}
		entries := catalog.Entries(groupID, 1)
		if len(entries) != len(wantCodes) {
			t.Fatalf("real group %d entries=%+v want codes=%v", groupID, entries, wantCodes)
		}
		for index, wantCode := range wantCodes {
			if entries[index].Code != wantCode || entries[index].EntityType != 0 {
				t.Fatalf("real group %d entry[%d]=%+v want code=%d entity_type=0", groupID, index, entries[index], wantCode)
			}
		}
	}
	// 121-124 是同一批怪的 `B` 档（`HellModeToGroupKey`：difficulty 2 ⇒ 'B'）。
	if !catalog.HasEntries(121, 2) || catalog.HasEntries(121, 1) {
		t.Fatalf("real group 121 A/B = %t/%t want false/true", catalog.HasEntries(121, 1), catalog.HasEntries(121, 2))
	}
	ruleA, ok := catalog.DifficultyRule(1)
	if !ok || ruleA.Key != 'A' || ruleA.RewardRollCount != 8 || ruleA.Unknown1 != 5 ||
		ruleA.RatioProbability != 100 || ruleA.Probability != 100 || ruleA.Unknown4 != 0 {
		t.Fatalf("real difficulty A rule=%+v ok=%t", ruleA, ok)
	}
	ruleB, ok := catalog.DifficultyRule(2)
	if !ok || ruleB.Key != 'B' || ruleB.RewardRollCount != 6 || ruleB.Unknown1 != 4 || ruleB.Probability != 0 {
		t.Fatalf("real difficulty B rule=%+v ok=%t", ruleB, ok)
	}

	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}
	dungeon, ok := table.FindDungeon(310)
	if !ok {
		t.Fatal("real dungeon 310 not indexed")
	}
	if !dungeon.Metadata.BasisLevel.Set || dungeon.Metadata.BasisLevel.Value <= 0 || dungeon.Metadata.BasisLevel.Value > 0xff {
		t.Fatalf("real dungeon 310 [basis level]=%+v is unusable for wave monster levels", dungeon.Metadata.BasisLevel)
	}
	hellMap, ok := table.FindMap(60083)
	if !ok {
		t.Fatal("real map 60083 not indexed")
	}
	orders := map[int64]int{}
	for _, object := range hellMap.SpecialPassiveObjects {
		for _, entry := range object.HellParty {
			orders[entry.Order]++
		}
	}
	if orders[1] != 8 || orders[2] != 5 {
		t.Fatalf("real map 60083 [hellparty] orders=%v want order1=8 order2=5", orders)
	}

	// 确定性抽组：`dungeonChoice` 恒返回 0 ⇒ order1 落 125（1050）、order2 落 211（3 只 APC）。
	service := &Service{dungeonChoice: func(int) (int, error) { return 0, nil }}
	waves, err := service.resolveDungeonHellPartyWaves(
		table, catalog, nil, 60083, dungeon.Metadata.BasisLevel, 1)
	if err != nil {
		t.Fatalf("resolve real hell party waves: %v", err)
	}
	if len(waves) != 2 {
		t.Fatalf("real waves=%+v", waves)
	}
	if waves[0].Order != 1 || waves[0].GroupID != 125 || len(waves[0].Monsters) != 1 ||
		waves[0].Monsters[0].Code != 1050 || waves[0].Monsters[0].Type != 0 ||
		int64(waves[0].Monsters[0].Level) != dungeon.Metadata.BasisLevel.Value {
		t.Fatalf("real wave 1=%+v basis=%+v", waves[0], dungeon.Metadata.BasisLevel)
	}
	if waves[1].Order != 2 || waves[1].GroupID != 211 || len(waves[1].Monsters) != 3 {
		t.Fatalf("real wave 2=%+v", waves[1])
	}
	for index, wantCode := range []int64{65745, 65746, 65747} {
		if waves[1].Monsters[index].Code != wantCode || waves[1].Monsters[index].Type != 0 {
			t.Fatalf("real wave 2 monster[%d]=%+v want code=%d", index, waves[1].Monsters[index], wantCode)
		}
	}
	// NOTI 679 的体：4 只怪 ⇒ 4 + 4×8 = 36 字节（现场验收同值）。
	body := buildCurrentHellPartyMonsterInfoBody(waves)
	if len(body) != 36 {
		t.Fatalf("real 679 body len=%d want=36 body=%x", len(body), body)
	}
	entries := buildCurrentHellPartyMonsterInfoEntries(waves)
	if len(entries) != 4 || entries[0].Code != 1050 || entries[1].Code != 65745 {
		t.Fatalf("real 679 entries=%+v", entries)
	}
	// 每个波次怪都必须能在真档 monster.lst 里查到（隐藏模板行发射前的那道校验）。
	monsterCatalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("load real monster catalog: %v", err)
	}
	for _, wave := range waves {
		for _, monster := range wave.Monsters {
			if _, found, err := monsterCatalog.Find(monster.Code); err != nil || !found {
				t.Fatalf("real wave monster code=%d not in monster.lst: found=%t error=%v", monster.Code, found, err)
			}
		}
	}
}
