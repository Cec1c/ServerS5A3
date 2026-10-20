package dnfbridge

import (
	"testing"
	"time"
)

func TestSeekerQST022RollStableWithinRunAndVariesAcrossRuns(t *testing.T) {
	f := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{dungeonID: 42, difficulty: 1, active: true})
	_, objectKey := seekerQST022AssertActorIdentity(t, f)
	scene, _ := f.runtime.Session.Scene()
	enemy := runtimeDungeonRewardEnemy{EnemyType: 2, EnemyCode: 323}
	outcomes := map[int64]bool{}
	for n := 1; n <= 100; n++ {
		f.runtime.startedAt = time.Unix(0, int64(n))
		key := currentDungeonQuestDropEventKeyFor(f.session, f.runtime, scene, objectKey, enemy)
		seed := currentDungeonQuestDropRNGSeed(key)
		first := newCurrentDungeonDropLCG(seed)
		second := newCurrentDungeonDropLCG(seed)
		a, b := currentQuestDropRoll(int(first.NextUint32()), 1, 50, 1, 0), currentQuestDropRoll(int(second.NextUint32()), 1, 50, 1, 0)
		if a != b {
			t.Fatal("same event rerolled")
		}
		outcomes[a] = true
	}
	if len(outcomes) != 2 {
		t.Fatalf("all runs share fixed outcome: %v", outcomes)
	}
}
