package fiendwar

import "testing"

func TestBossMinigameHPNeverReplacesTotalOrBoardHP(t *testing.T) {
	b, now := newFixtureBossBattle(t)
	b.PickClone = func([]int) int { return 0 }
	for _, side := range []string{"top boss", "bot boss"} {
		b.set(side+" hp", 100)
		b.set(side+" view hp", 100)
	}
	b.resetBossPattern()
	for step := 0; step < 4; step++ {
		side := 0
		if step == 3 {
			side = 1
		}
		dungeon := 4978
		if side == 1 {
			dungeon = 4953
		}
		for _, hp := range []int{80, 20, 0} {
			if err := b.BossReport(dungeon, 0, hp, now); err != nil {
				t.Fatal(err)
			}
			if b.Value(bossPatternSideName(side)+" hp") != 100 || b.Value(bossPatternSideName(side)+" view hp") != 100 {
				t.Fatal("attribute minigame damaged total/board HP", step, hp, b.Symbols)
			}
		}
		state := b.Value(bossPatternSideName(side) + " state")
		if err := b.BossReport(dungeon, 1, state, now); err != nil {
			t.Fatal(err)
		}
	}
	if !b.BossDamageWindowOpen(4978) || !b.BossDamageWindowOpen(4953) {
		t.Fatal("four games did not open shared body window")
	}
	if err := b.BossReport(4978, 0, 75, now); err != nil {
		t.Fatal(err)
	}
	if b.Value("top boss hp") != 75 || b.Value("top boss view hp") != 75 {
		t.Fatal("body damage was not visible")
	}
	if err := b.BossReport(4978, 2, 60, now); err != nil {
		t.Fatal(err)
	}
	if err := b.BossReport(4978, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	if b.Value("top boss hp") != 60 || b.Value("top boss view hp") != 60 {
		t.Fatal("finished side accepted old game heartbeat")
	}
	if err := b.BossReport(4953, 2, 80, now); err != nil {
		t.Fatal(err)
	}
	if b.Value("top boss hp") != 70 || b.Value("bot boss hp") != 70 || b.Value("top boss view hp") != 70 || b.Value("bot boss view hp") != 70 {
		t.Fatal("body window balance incorrect")
	}
	if err := b.BossReport(4953, 0, 1, now); err != nil {
		t.Fatal(err)
	}
	if b.Value("bot boss hp") != 70 || b.Value("bot boss view hp") != 70 {
		t.Fatal("next game's HP replaced retained body HP")
	}
}
