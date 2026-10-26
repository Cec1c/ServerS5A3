package quest

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestQuestCharacterExperienceStopsAtProvenCap(t *testing.T) {
	tables := finishOwnerProgression(t)
	for _, tc := range []struct {
		name       string
		level, cap int
		exp, gain  uint32
		wantExp    uint32
		wantLevel  int
	}{
		{"at cap", 2, 2, 250, 110, 100, 2},
		{"cross cap", 1, 2, 90, 110, 100, 2},
		{"below cap", 2, 4, 110, 10, 120, 2},
		{"short experience ledger is not inflated", 2, 2, 0, 10, 10, 2},
		{"zero gain keeps the existing ledger", 2, 2, 250, 0, 250, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planFinishProgression(tables, dnfrepo.CharacterRecord{Level: tc.level, Stats: map[string]int64{"exp": int64(tc.exp)}}, dnfrepo.SkillRecord{Points: dnfrepo.SkillPointState{SyncedLevel: tc.level}}, tc.gain, tc.cap)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Experience.NewLevel != tc.wantLevel || plan.Experience.NewExperience != tc.wantExp || plan.Experience.Gain != tc.gain {
				t.Fatalf("level=%d exp=%d raw_gain=%d; want level=%d exp=%d raw_gain=%d", plan.Experience.NewLevel, plan.Experience.NewExperience, plan.Experience.Gain, tc.wantLevel, tc.wantExp, tc.gain)
			}
		})
	}
}
