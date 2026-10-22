package fiendwar

import (
	"reflect"
	"testing"
	"time"
)

// One side can finish three balls and report kind2 before the other side
// finishes the fourth. Neither side may start the damage window early.
func TestR195BossSharedWindowWaitsForLastBall(t *testing.T) {
	for _, fast := range []int{0, 1} {
		t.Run(bossPatternSideName(fast), func(t *testing.T) {
			b, now := newFixtureBossBattle(t)
			b.PickClone = func([]int) int { return 0 }
			for _, name := range []string{"top boss", "bot boss"} {
				b.set(name+" hp", 100)
				b.set(name+" view hp", 100)
			}
			b.resetBossPattern()
			dungeons := [2]int{4978, 4953}
			slow := 1 - fast
			for round := 0; round < 2; round++ {
				otherHeld := b.Value(bossPatternSideName(slow) + " state")
				for ball := 0; ball < 3; ball++ {
					state := b.Value(bossPatternSideName(fast) + " state")
					if err := b.BossReport(dungeons[fast], 1, state, now); err != nil {
						t.Fatal(err)
					}
				}
				if b.Value("boss pattern state") != fiendWarCloneWeights[otherHeld-5] {
					t.Fatal("fast side consumed the slow side's remaining ball")
				}
				if state := b.Value(bossPatternSideName(fast) + " state"); state != 4 {
					t.Fatalf("round %d: fast side state=%d, want native no-ball wait 4", round, state)
				}
				if b.Value(bossPatternSideName(slow)+" state") != otherHeld {
					t.Fatal("waiting changed the slow side's mechanism")
				}
				before := b.Clone()
				if err := b.BossReport(dungeons[fast], 2, 0, now.Add(15*time.Second)); err == nil {
					t.Fatal("waiting accepted a damage-window completion")
				}
				if !reflect.DeepEqual(before.Symbols, b.Symbols) || before.BossWindowDone != b.BossWindowDone {
					t.Fatal("premature window report mutated the shared round")
				}
				hp := b.Value(bossPatternSideName(fast) + " hp")
				_ = b.BossReport(dungeons[fast], 0, 0, now.Add(16*time.Second))
				if b.Value(bossPatternSideName(fast)+" hp") != hp {
					t.Fatal("waiting HP heartbeat damaged the boss")
				}
				if err := b.Tick(now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				if b.Value(bossPatternSideName(fast)+" state") != 4 {
					t.Fatal("waiting elapsed into a solo damage window")
				}
				if err := b.BossReport(dungeons[slow], 1, otherHeld, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				if b.Value("boss pattern state") != 0 || b.Value("top boss state") != 9 || b.Value("bot boss state") != 9 {
					t.Fatal("last ball did not open both damage windows together")
				}
				if err := b.BossReport(dungeons[fast], 2, 30, now.Add(75*time.Second)); err != nil {
					t.Fatal(err)
				}
				if b.Value("boss pattern state") != 0 {
					t.Fatal("single window tail restarted the shared pool")
				}
				before = b.Clone()
				if err := b.BossReport(dungeons[fast], 2, 0, now.Add(75*time.Second)); err == nil {
					t.Fatal("duplicate tail overwrote the completed side's HP")
				}
				if !reflect.DeepEqual(before.Symbols, b.Symbols) || before.BossWindowDone != b.BossWindowDone {
					t.Fatal("duplicate tail changed a completed window")
				}
				if err := b.BossReport(dungeons[slow], 2, 50, now.Add(75*time.Second)); err != nil {
					t.Fatal(err)
				}
				if b.Value("boss pattern state") != 1111 || b.BossWindowDone != [2]bool{} {
					t.Fatal("failed window did not restart all four balls")
				}
				if b.Value("top boss state") == b.Value("bot boss state") || b.Complete {
					t.Fatal("reset assigned the same ball or granted a clear")
				}
				before = b.Clone()
				if err := b.BossReport(dungeons[slow], 2, 0, now.Add(76*time.Second)); err == nil {
					t.Fatal("old window report was accepted in the next mechanism round")
				}
				if !reflect.DeepEqual(before.Symbols, b.Symbols) {
					t.Fatal("stale tail changed the next round")
				}
				now = now.Add(2 * time.Minute)
			}
		})
	}
}

func TestR195BossDeathCannotBypassSharedWindow(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state, pool int
	}{{"entry", 2, 0}, {"waiting", 4, 1}, {"premature_groggy", 9, 1}, {"next_round", 5, 1111}} {
		t.Run(tc.name, func(t *testing.T) {
			b, now := newFixtureBossBattle(t)
			b.set("top boss state", tc.state)
			b.set("boss pattern state", tc.pool)
			before := b.Clone()
			if b.BossDamageWindowOpen(4978) {
				t.Fatal("unfinished mechanisms opened a damage window")
			}
			if err := b.ClearClientBossRoom(4978, now); err == nil {
				t.Fatal("client death bypassed unfinished mechanisms")
			}
			if err := b.Clear(4978, now); err == nil {
				t.Fatal("direct clear bypassed unfinished mechanisms")
			}
			if !reflect.DeepEqual(before.Symbols, b.Symbols) || !reflect.DeepEqual(before.Areas, b.Areas) ||
				!reflect.DeepEqual(before.DungeonStates, b.DungeonStates) || before.BossWindowDone != b.BossWindowDone || b.Complete {
				t.Fatal("rejected death changed battle state")
			}
		})
	}
}
