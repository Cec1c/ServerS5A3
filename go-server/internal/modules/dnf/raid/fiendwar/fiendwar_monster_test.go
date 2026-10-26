package fiendwar

import "testing"

// 前哨基地/兵站本营的领主是**固定的**（操作员 2026-10-01 复核历史截图后的口径）：
// 战况表上每个格子画的就是那一只，服务端必须让实际出现的是同一只 —— 所以这里不抽签，
// 而是按格子位置钉死：上 → maze 0、中 → 1、下 → 2。
//
// 只有 maze 张数 > 1 的设施区域才会登记；雷达/炮塔/传送站只有一张图，不登记。
func TestFiendWarFixtureMazesAreFixedByCellPosition(t *testing.T) {
	config := &FiendWarBattleConfig{
		Raid:  &AntonConfig{PhaseMax: 1, MemberMax: 8},
		Rules: []FiendWarRule{{}},
		Areas: map[int]FiendWarArea{
			6:  {Index: 6, Dungeon: 4954},                       // 普通作战区域：由兵种定图
			15: {Index: 15, Dungeon: 4963, Kind: "radar"},       // 单图
			17: {Index: 17, Dungeon: 4965, Kind: "battery"},     // 单图
			19: {Index: 19, Dungeon: 4967, Kind: "top outpost"}, // 上哨 → maze 0
			21: {Index: 21, Dungeon: 4969, Kind: "mid outpost"}, // 中哨 → maze 1
			23: {Index: 23, Dungeon: 4971, Kind: "bot outpost"}, // 下哨 → maze 2
			27: {Index: 27, Dungeon: 4975, Kind: "top command center"},
			28: {Index: 28, Dungeon: 4976, Kind: "mid command center"},
			29: {Index: 29, Dungeon: 4977, Kind: "bot command center"},
			30: {Index: 30, Dungeon: 4978, Kind: "top boss"}, // 单图
		},
	}
	b, err := NewFiendWarBattle(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.MazeForArea(6); ok {
		t.Fatal("普通作战区域不该登记固定图：它的图由区域里那只兵种决定")
	}
	for _, id := range []int{15, 17, 30} {
		if _, ok := b.MazeForArea(id); ok {
			t.Fatalf("单图设施 %d 不该登记（恒为 0）", id)
		}
	}
	for id, want := range map[int]int{19: 0, 21: 1, 23: 2, 27: 0, 28: 1, 29: 2} {
		maze, ok := b.MazeForArea(id)
		if !ok {
			t.Fatalf("多图设施 %d 没有登记固定图", id)
		}
		if maze != want {
			t.Fatalf("设施 %d 的固定图 = %d，want %d（上/中/下 对应 0/1/2）", id, maze, want)
		}
	}

	// 固定就是把随机性去掉：建一百次战场，同一格的图必须**一模一样**。
	for range 100 {
		fresh, err := NewFiendWarBattle(config)
		if err != nil {
			t.Fatal(err)
		}
		for id, want := range map[int]int{19: 0, 21: 1, 23: 2, 27: 0, 28: 1, 29: 2} {
			if maze, _ := fresh.MazeForArea(id); maze != want {
				t.Fatalf("重建战场后设施 %d 的图变成了 %d，want %d —— 说明还在抽签", id, maze, want)
			}
		}
	}

	// 快照必须带上这张固定图：战况表是从 Clone 出来的 state 上发的。
	clone := b.Clone()
	original, _ := b.MazeForArea(19)
	cloned, ok := clone.MazeForArea(19)
	if !ok || cloned != original {
		t.Fatalf("Clone 丢了固定图：%d -> %d（ok=%t）", original, cloned, ok)
	}
	clone.FixtureMazes[19] = (original + 1) % 3
	if again, _ := b.MazeForArea(19); again != original {
		t.Fatal("Clone 改固定图影响到了原战场")
	}
}

// 表本身的自洽性：四只军团领主、两只原生怪领主，互不相同且非 0；
// 每个设施副本的 maze 张数就是它在 PVF 里的张数（1 或 3）。
func TestFiendWarMonsterTablesAreWellFormed(t *testing.T) {
	if len(fiendWarArmyBossIDs) != 4 {
		t.Fatalf("军团领主应有 4 只（[ENEMY CREATE RATE] 1..4），实测 %d", len(fiendWarArmyBossIDs))
	}
	if len(fiendWarOrcBossIDs) != 2 {
		t.Fatalf("原生怪领主应有 2 只（maze 4/5），实测 %d", len(fiendWarOrcBossIDs))
	}
	seen := map[uint32]string{}
	for i, id := range fiendWarArmyBossIDs {
		if id == 0 {
			t.Fatalf("军团领主 %d 为空", i+1)
		}
		seen[id] = "army"
	}
	for i, id := range fiendWarOrcBossIDs {
		if id == 0 {
			t.Fatalf("原生怪领主 %d 为空", i+1)
		}
		seen[id] = "orc"
	}
	if len(seen) != 6 {
		t.Fatalf("六只领主里有重复：%v", seen)
	}
	wantMazes := map[int]int{4963: 1, 4965: 1, 4967: 3, 4969: 3, 4971: 3, 4975: 3, 4976: 3, 4977: 3, 4953: 1, 4978: 1}
	if len(FiendWarFixtureBossIDs) != len(wantMazes) {
		t.Fatalf("设施副本应有 %d 个，实测 %d", len(wantMazes), len(FiendWarFixtureBossIDs))
	}
	for dungeon, want := range wantMazes {
		bosses, ok := FiendWarFixtureBossIDs[dungeon]
		if !ok {
			t.Fatalf("缺副本 %d", dungeon)
		}
		if len(bosses) != want {
			t.Fatalf("副本 %d 的 maze 张数 = %d，want %d", dungeon, len(bosses), want)
		}
		for maze, id := range bosses {
			if id == 0 {
				t.Fatalf("副本 %d maze %d 的领主为空", dungeon, maze)
			}
		}
	}
	// 三个前哨、三个兵站本营共用同一组领主（同一组 .map 的三张 maze）。
	for _, pair := range [][2]int{{4967, 4969}, {4969, 4971}, {4975, 4976}, {4976, 4977}} {
		left, right := FiendWarFixtureBossIDs[pair[0]], FiendWarFixtureBossIDs[pair[1]]
		for i := range left {
			if left[i] != right[i] {
				t.Fatalf("副本 %d/%d 的 maze %d 领主不一致：%d vs %d", pair[0], pair[1], i, left[i], right[i])
			}
		}
	}
}
