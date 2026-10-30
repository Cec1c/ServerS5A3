package progression

import (
	"context"
	"maps"
	"testing"
)

func TestCommitReturnsOwnedAuthoritativeStatsOnApplyAndReplay(t *testing.T) {
	ctx := context.Background()
	repos := seedProgressionCommitRepositories(t, ctx)
	plan := validProgressionCommitPlan()
	plan.NextCharacterStats = map[string]int64{"honor_expert_level": 2}
	for _, replay := range []bool{false, true} {
		result, err := Commit(ctx, repos.CharacterProgression, plan)
		if err != nil {
			t.Fatal(err)
		}
		stored, _, err := repos.Character.Load(ctx, plan.CharacterID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Idempotent != replay || !maps.Equal(result.CharacterStats, stored.Stats) {
			t.Fatalf("replay=%t snapshot=%+v stored=%+v", replay, result, stored.Stats)
		}
		result.CharacterStats["exp"] = -99
		result.CharacterStats["honor_expert_level"] = -99
		reloaded, _, _ := repos.Character.Load(ctx, plan.CharacterID)
		if !maps.Equal(reloaded.Stats, stored.Stats) {
			t.Fatal("caller mutation escaped returned snapshot")
		}
	}
}
