package quest

import (
	"strconv"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The caller supplies resources, never a player save. Offline audits may reuse
// an already-parsed catalog; the ordinary integration entrypoint reads its PVF.
func TestRealPVFStorySweepChoiceBoundaries(t *testing.T) {
	verifyStorySweepRealChoices(t, loadRealPVFQuestCatalog(t))
}

func verifyStorySweepRealChoices(t *testing.T, catalog *Catalog) {
	t.Helper()
	for _, tc := range []struct {
		id       int64
		job      int
		branches []int64
	}{
		{3868, 0, []int64{3870, 3871, 3873}}, {3869, 11, []int64{3872, 3874}},
	} {
		d, ok := catalog.Find(tc.id)
		if !ok || normalizeQuestTag(d.Type) != "question" || d.QuestionOptionCount != len(tc.branches) {
			t.Fatalf("real choice definition changed: %d", tc.id)
		}
		for answer := -1; answer < len(tc.branches); answer++ {
			r := repo.QuestRecord{CharacterID: "resource-fixture", States: map[int64]repo.QuestState{}}
			for _, group := range d.PreRequiredGroups {
				for _, id := range group {
					r.States[id] = repo.QuestState{Status: "completed"}
				}
			}
			if answer >= 0 {
				r.States[tc.id] = repo.QuestState{Status: "completed", Extra: map[string]string{questionAnswerExtraKey: strconv.Itoa(answer), questionOptionCountExtraKey: strconv.Itoa(d.QuestionOptionCount), questionAnswerMetadataKey: "1"}}
			}
			for _, ticket := range []bool{true, false} {
				p := catalog.planStorySweep(r, CharacterEligibility{Level: 89, Job: tc.job}, 89, CouponClearScopeMainStorySweep, ticket, time.Unix(1800000000, 0))
				if answer < 0 && isCompletedQuestStatus(p.Record.States[tc.id].Status) {
					t.Fatal("unanswered real question cleared", tc.id, ticket)
				}
				for a, id := range tc.branches {
					if isCompletedQuestStatus(p.Record.States[id].Status) != (answer == a) {
						t.Fatal("real branch mismatch", tc.id, answer, id, ticket)
					}
				}
			}
		}
	}
}
