package dungeon

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSettlementCharacterExperienceStopsAtProvenCap(t *testing.T) {
	tables, err := progression.Load(context.Background(), settlementTestSource())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		level     int
		exp, gain uint32
		wantExp   uint32
		wantLevel int
		wantHonor uint32
	}{
		{"at cap", 5, 900, 25, 900, 5, 25},
		{"cross cap", 4, 899, 100, 900, 5, 99},
		{"below cap", 3, 300, 25, 325, 3, 0},
		{"old excess at cap", 5, 1200, 25, 900, 5, 25},
		{"short experience ledger is not inflated", 5, 100, 25, 125, 5, 25},
		{"zero gain keeps the existing ledger", 5, 1200, 0, 1200, 5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanSettlementProgressionAtCap(tables, tc.level, tc.exp, tc.gain, dnfrepo.SkillPointState{SyncedLevel: tc.level}, 5)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Experience.NewLevel != tc.wantLevel || plan.Experience.NewExperience != tc.wantExp || plan.Experience.Gain != tc.gain {
				t.Fatalf("level=%d exp=%d raw_gain=%d; want level=%d exp=%d raw_gain=%d", plan.Experience.NewLevel, plan.Experience.NewExperience, plan.Experience.Gain, tc.wantLevel, tc.wantExp, tc.gain)
			}
			if got := maximumLevelExperienceGain(tc.level, tc.exp, tc.gain, 5, tables); got != tc.wantHonor {
				t.Fatalf("honor=%d want=%d", got, tc.wantHonor)
			}
		})
	}
}
