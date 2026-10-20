package fiendwar

import (
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestFiendWarIndependentMovementAndRecovery(t *testing.T) {
	now := time.Unix(1700000000, 0)
	b := &FiendWarBattle{
		Config: &FiendWarBattleConfig{WaitingArea: 2, DieImprisonSeconds: []int{20, 30, 50, 80, 120}, Areas: map[int]FiendWarArea{
			2: {Index: 2, Next: []int{3, 5}}, 3: {Index: 3, Next: []int{2}}, 5: {Index: 5, Next: []int{2, 8}}, 8: {Index: 8, Next: []int{5}},
		}},
		Areas: map[int]FiendWarAreaState{}, GroupAreas: map[byte]int{1: 5}, MemberAreas: map[uint16]int{1: 5, 2: 2},
	}
	if err := b.MoveMember(2, 1, 3, now); err != nil {
		t.Fatal("member used leader's area instead of own", err)
	}
	if b.MemberArea(1, 1) != 5 || b.MemberArea(2, 1) != 3 {
		t.Fatal("one movement teleported both members")
	}
	if err := b.MoveMember(2, 1, 8, now); err == nil {
		t.Fatal("accepted a nonadjacent member move")
	}
	for _, seconds := range []int{20, 30, 50, 80, 120, 120} {
		b.ImprisonMember(2, now)
		until := now.Add(time.Duration(seconds) * time.Second)
		if b.MemberArea(2, 1) != 2 || b.RecoveryUntil[2] != until {
			t.Fatal("wrong imprisonment tier", b.RecoveryUntil[2])
		}
		if err := b.MoveMember(2, 1, 3, until.Add(-time.Nanosecond)); err == nil {
			t.Fatal("escaped imprisonment early")
		}
		if err := b.MoveMember(2, 1, 3, until); err != nil {
			t.Fatal(err)
		}
		now = until
	}
	clone := b.Clone()
	clone.ImprisonMember(2, now)
	if b.ImprisonCounts[2] != 6 || b.MemberArea(2, 1) != 3 {
		t.Fatal("snapshot changed live recovery")
	}
}

func TestFiendWarAreaMemberLimit(t *testing.T) {
	b := &FiendWarBattle{
		Config: &FiendWarBattleConfig{WaitingArea: 2, Areas: map[int]FiendWarArea{
			2: {Index: 2, Next: []int{3}, MemberMax: 8},
			3: {Index: 3, Next: []int{2}, MemberMax: 4},
		}},
		Areas: map[int]FiendWarAreaState{}, GroupAreas: map[byte]int{1: 2},
		MemberAreas: map[uint16]int{1: 3, 2: 3, 3: 3, 4: 3, 5: 2},
	}
	if err := b.MoveMember(5, 1, 3, time.Now()); err == nil || b.MemberAreas[5] != 2 {
		t.Fatal("fifth member entered a four-member area")
	}
	if err := b.MoveMember(1, 1, 3, time.Now()); err != nil {
		t.Fatal("existing member counted twice", err)
	}
	if err := b.MoveMember(1, 1, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := b.MoveMember(5, 1, 3, time.Now()); err != nil {
		t.Fatal("released area slot stayed occupied", err)
	}
}

// 站在没打完的区域里也必须能继续走。
//
// 原来 Move 里有一条"fiendwar source area requires combat"：从还没清掉敌人的区域走出去
// 一律拒绝。客户端自己不认这条规则（它照走不误），于是 2026-10-01 真机出现
// "客户端的角色已经在 B、服务端 MemberAreas 还记着 A" 的分叉，直接导致
// 「同区域不一起进图」，以及之后他再按进图被同一条拒绝挡下时的**卡死**。
// 地图推进由 [AREA INFO] 的相邻关系与 BOSS 门禁把关，不需要这条自造规则。
func TestFiendWarMoveAllowsLeavingAnUnclearedArea(t *testing.T) {
	b := &FiendWarBattle{
		Config: &FiendWarBattleConfig{WaitingArea: 2, Areas: map[int]FiendWarArea{
			2:  {Index: 2, Next: []int{6}, MemberMax: 8},
			6:  {Index: 6, Dungeon: 4954, Next: []int{2, 10}, MemberMax: 4},
			10: {Index: 10, Dungeon: 4958, Next: []int{6}, MemberMax: 4},
		}},
		// 区域 6 里还站着一只没打掉的守军 —— 这正是被拒的那种输入。
		Areas:       map[int]FiendWarAreaState{6: {Enemy: FiendWarUnit{ID: 11, Type: 1}}},
		GroupAreas:  map[byte]int{1: 6},
		MemberAreas: map[uint16]int{1: 6},
	}
	if !b.AreaOpen(6) {
		t.Fatal("fixture 没造出'区域里还有敌人'这个输入")
	}
	if err := b.MoveMember(1, 1, 10, time.Now()); err != nil {
		t.Fatalf("站在没打完的区域里走不出去：%v", err)
	}
	if b.MemberArea(1, 1) != 10 {
		t.Fatalf("走位没有被记进战场：area=%d want 10", b.MemberArea(1, 1))
	}
}

func TestFiendWarMonstersSpawnAwayFromEnemyArmies(t *testing.T) {
	b := &FiendWarBattle{
		Config: &FiendWarBattleConfig{Areas: map[int]FiendWarArea{
			14: {Index: 14, Dungeon: 4962}, 16: {Index: 16, Dungeon: 4964}, 18: {Index: 18, Dungeon: 4966},
			3: {Index: 3, Dungeon: 4951}, 5: {Index: 5, Dungeon: 4953},
		}},
		Areas: map[int]FiendWarAreaState{
			14: {Enemy: FiendWarUnit{ID: 1, Type: 1}}, 16: {Enemy: FiendWarUnit{ID: 2, Type: 2}},
			18: {Enemy: FiendWarUnit{ID: 3, Type: 3}}, 3: {}, 5: {},
		},
		Symbols: map[uint32]int{}, NextUnitID: 3,
	}
	b.act(FiendWarInstruction{Op: "create monsters"}, time.Unix(1700000000, 0))
	for _, id := range []int{14, 16, 18} {
		if b.Areas[id].Monster.ID != 0 {
			t.Fatalf("spawn created an immediate enemy/monster battle in area %d", id)
		}
	}
	for _, id := range []int{3, 5} {
		if b.Areas[id].Monster.ID == 0 {
			t.Fatalf("empty area %d was not populated", id)
		}
	}
}

func TestRealScriptPVFFiendWarBattle(t *testing.T) {
	path := os.Getenv("DNF_FIENDWAR_REAL_PVF")
	if path == "" {
		t.Skip("DNF_FIENDWAR_REAL_PVF not set")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadFiendWarConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFiendWarBattle(c.Battle)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if err = b.Apply(FiendWarEvent{}, now); err != nil {
		t.Fatal(err)
	}
	if len(c.Battle.Areas) != 30 || len(c.Battle.Raid.Phases) != 1 || b.Value("fiendwar init") != 1 {
		t.Fatalf("initial battle: %+v", b)
	}
	for _, id := range []int{14, 16, 18} {
		if b.Areas[id].Enemy.ID == 0 {
			t.Fatalf("missing initial enemy %d", id)
		}
	}
	if err = b.Tick(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if b.Value("alive monster count") != 3 {
		t.Fatal("monster population", b.Value("alive monster count"))
	}
	if err = b.Move(1, 30); err == nil {
		t.Fatal("accepted boss teleport")
	}
	// Isolate the real clearing/restore triggers from unrelated moving armies.
	for _, id := range []int{27, 28, 29} {
		a := c.Battle.Areas[id]
		b.GroupAreas[1] = id
		if err = b.Enter(1, a.Dungeon, now); err != nil {
			t.Fatal(err)
		}
		if err = b.Clear(a.Dungeon, now); err != nil {
			t.Fatal(err)
		}
	}
	if !b.AreaOpen(30) || !b.AreaOpen(31) {
		t.Fatal("three center clears did not open both bosses")
	}
	if b.Timers[FiendWarTimerKey{27, "top_restore_center"}].Deadline != now.Add(720*time.Second) {
		t.Fatal("PVF center restore timer missing")
	}
	for i, id := range []int{30, 31} {
		a := c.Battle.Areas[id]
		b.GroupAreas[byte(i+1)] = id
		if err = b.Enter(byte(i+1), a.Dungeon, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = b.Clear(4978, now); err == nil {
		t.Fatal("accepted boss clear before mechanics")
	}
	for _, id := range []int{306000463, 306000460} {
		if err = b.Apply(FiendWarEvent{Kind: "on enter map", ID: id}, now); err != nil {
			t.Fatal(err)
		}
	}
	if b.Value("boss pattern state") != 1111 {
		t.Fatal("boss mechanisms not initialized")
	}
	// 四颗球是**两队共享的池子**：每一侧各拿一颗不同的球，谁打掉自己那颗，
	// 那颗球就从机制条上永久消失、该侧再从剩下的球里补一颗；
	// 四颗都清掉 → 两侧都到 9（本体破防）。
	for round := 0; round < 2; round++ {
		for _, room := range []struct {
			dungeon int
			side    string
		}{{4978, "top boss"}, {4953, "bot boss"}} {
			state := b.Value(room.side + " state")
			if state < 5 || state > 8 {
				t.Fatalf("round %d: %s has no ball to kill (state=%d)", round, room.side, state)
			}
			otherSide := "bot boss"
			if room.side == "bot boss" {
				otherSide = "top boss"
			}
			otherBefore := b.Value(otherSide + " state")
			patternBefore := b.Value("boss pattern state")
			if err = b.BossReport(room.dungeon, 1, state, now); err != nil {
				t.Fatal(err)
			}
			if got := b.Value("boss pattern state"); got != patternBefore-fiendWarCloneWeights[state-5] {
				t.Fatalf("%s kind=1 value=%d cleared the wrong ball: pattern %d → %d",
					room.side, state, patternBefore, got)
			}
			if got := b.Value(otherSide + " state"); got != otherBefore {
				t.Fatalf("%s 打掉自己那颗球，却动了对面：%s state %d → %d",
					room.side, otherSide, otherBefore, got)
			}
		}
	}
	if b.Value("boss pattern state") != 0 {
		t.Fatalf("四颗球清完后机制条 = %d，want 0", b.Value("boss pattern state"))
	}
	if b.Value("top boss state") != 9 || b.Value("bot boss state") != 9 {
		t.Fatalf("四颗球清完后两侧应当都是 9（本体破防），got top=%d bot=%d",
			b.Value("top boss state"), b.Value("bot boss state"))
	}
	for _, id := range []int{4978, 4953} {
		if err = b.BossReport(id, 0, 0, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = b.Clear(4978, now); err != nil {
		t.Fatal(err)
	}
	if b.Complete {
		t.Fatal("one boss granted victory")
	}
	if err = b.Clear(4953, now); err != nil {
		t.Fatal(err)
	}
	if !b.Complete {
		t.Fatal("both bosses did not finish raid")
	}
	clone := b.Clone()
	clone.Symbols[92] = 0
	if b.Symbols[92] != 10 {
		t.Fatal("snapshot aliases live state")
	}
	failed, _ := NewFiendWarBattle(c.Battle)
	if err = failed.Apply(FiendWarEvent{}, now); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 30 && !failed.Failed; i++ {
		if err = failed.Tick(now.Add(time.Duration(i) * 150 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if !failed.Failed {
		t.Fatal("enemy arrival did not fail defense")
	}
	t.Run("boss retreat and retry", func(t *testing.T) {
		b, _ := NewFiendWarBattle(c.Battle)
		if err := b.Apply(FiendWarEvent{}, now); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"top command center state", "mid command center state", "bot command center state"} {
			b.set(name, 0)
		}
		if err := b.Apply(FiendWarEvent{}, now); err != nil {
			t.Fatal(err)
		}
		b.GroupAreas[1], b.GroupAreas[2] = 30, 31
		for i, dungeon := range []int{4978, 4953} {
			if err := b.Enter(byte(i+1), dungeon, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.Tick(now.Add(600 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if b.Areas[30].Group != 0 || b.Areas[31].Group != 0 || len(b.Retreats) != 2 {
			t.Fatal("timeout did not release both boss rooms")
		}
		if b.Value("top boss state") != 1 || b.Value("bot boss state") != 1 {
			t.Fatal("boss rooms did not reopen")
		}
		if b.GroupAreas[1] != 30 || b.GroupAreas[2] != 31 {
			t.Fatal("retreat diverged from frozen town origins")
		}
		if err := b.Enter(1, 4978, now.Add(601*time.Second)); err != nil {
			t.Fatal(err)
		}
		if b.Value("kick out character") != 0 {
			t.Fatal("retry retained old kick request")
		}
	})
}
