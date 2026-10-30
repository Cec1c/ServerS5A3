package quest

import (
	"errors"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPreviousMainStorySweepCutoffCanonicalStateOverridesProgress(t *testing.T) {
	record := repo.QuestRecord{
		States: map[int64]repo.QuestState{
			100: {Status: "completed"},
		},
		Progress: map[int64]repo.QuestState{
			100: previousStorySweepMarkedState("completed", "89"),
		},
	}

	if cutoff := PreviousMainStorySweepCutoff(record, 90); cutoff != 0 {
		t.Fatalf("cutoff = %d, want 0 because the canonical non-authorized state overrides stale progress", cutoff)
	}
}

func TestPreviousMainStorySweepCutoffRecognizesProgressOnlyMarker(t *testing.T) {
	record := repo.QuestRecord{
		Progress: map[int64]repo.QuestState{
			100: previousStorySweepMarkedState("completed", "89"),
		},
	}

	if cutoff := PreviousMainStorySweepCutoff(record, 90); cutoff != 89 {
		t.Fatalf("cutoff = %d, want 89 from the authorized progress-only marker", cutoff)
	}
}

func TestRecoverPreviousMainStorySweepRevalidateRejectsWithoutMutation(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		successfulChecks int
		wantChecks       int
	}{
		{name: "before first check", successfulChecks: 0, wantChecks: 1},
		{name: "before write", successfulChecks: 1, wantChecks: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreviousStorySweepFixture(t, 0, 0x21, 90, repo.QuestRecord{
				States: map[int64]repo.QuestState{
					100: previousStorySweepMarkedState("completed", "89"),
				},
			})
			before := fixture.snapshot(t)
			checks := 0
			input := previousStorySweepInput(fixture.character, 89)
			input.Revalidate = func() bool {
				checks++
				return checks <= testCase.successfulChecks
			}

			if _, err := fixture.owner.RecoverPreviousMainStorySweep(fixture.ctx, fixture.catalog, input); !errors.Is(err, ErrAutoCompleteMainUnavailable) {
				t.Fatalf("error = %v, want %v", err, ErrAutoCompleteMainUnavailable)
			}
			if checks != testCase.wantChecks {
				t.Fatalf("revalidate checks = %d, want %d", checks, testCase.wantChecks)
			}
			before.requireEqual(t, fixture.snapshot(t))
		})
	}
}
