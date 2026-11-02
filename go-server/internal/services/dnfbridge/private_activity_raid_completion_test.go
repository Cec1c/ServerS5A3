package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// Enter through the production reward-flow clock after the raid reducer and
// weekly receipt fixtures have established completion. Checking the final-mode
// predicate alone previously encoded the missing Anton completion as expected.
func TestRealPVFPrivateActivityRaidCompletionAndReplay(t *testing.T) {
	for _, tc := range []struct {
		phase string
		final bool
	}{
		{"anton_phase0", false}, {"anton_phase1", true},
		{"luke_phase0", false}, {"luke_phase1", true}, {"fiendwar", true},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			f, _ := r195MovieStageFixture(t, tc.phase)
			flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
			catalog, err := f.service.privateActivityCatalog()
			if err != nil || catalog == nil {
				t.Fatalf("activity catalog required: %v", err)
			}
			if catalog.Tasks[3].Target != 1 {
				t.Fatal("fixture requires one raid completion per daily task")
			}
			at := flow.CompletedAt.Add(time.Second)
			for attempt := 0; attempt < 2; attempt++ {
				if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
					t.Fatal(errs)
				}
				for _, owner := range flow.Recipients {
					ch, found, err := f.repos.Character.Load(context.Background(), strconv.Itoa(int(owner.CharacterID)))
					if err != nil || !found {
						t.Fatalf("load owner: %v", err)
					}
					want := int64(0)
					if tc.final && owner.Clears >= flow.Rewards.MinimumClears {
						want = int64(catalog.Tasks[3].Points)
					}
					if got := ch.Stats["private_activity_v1_points"]; got != want {
						t.Fatalf("owner %d attempt %d points=%d want=%d", owner.CharacterID, attempt, got, want)
					}
					if want > 0 && (ch.Stats["private_activity_v1_progress_4"] != 1 || ch.Stats["private_activity_v1_done"] != 1<<3) {
						t.Fatalf("missing or repeated raid task: %+v", ch.Stats)
					}
				}
			}
		})
	}
}
