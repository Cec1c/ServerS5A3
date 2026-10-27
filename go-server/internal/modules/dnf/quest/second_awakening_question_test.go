package quest

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// A future PVF must not let the awakening marker turn a player choice into an
// automatic completion. A completed, explicitly answered choice remains valid.
func TestSecondAwakeningCouponQuestionGate(t *testing.T) {
	for _, mode := range []string{"unanswered", "answered_unfinished", "damaged_completed", "answered_completed"} {
		t.Run(mode, func(t *testing.T) {
			g, _, in := secondAwakeningCouponFixture(t)
			source := professionOverrideTestSource()
			source["n_quest/awaken/second_1.qst"] += "\n[type]\n`[question]`\n[question info]\n`choose` `choose` `one` `two`\n"
			catalog := loadProfessionOverrideTestCatalog(t, source)
			q, _, _ := g.Quest.Load(context.Background(), "77")
			s := repo.QuestState{Status: "active", Extra: map[string]string{}}
			if mode == "answered_completed" || mode == "damaged_completed" {
				s.Status = "completed"
			}
			if mode == "answered_completed" || mode == "answered_unfinished" {
				s.Extra[questionAnswerExtraKey] = "0"
			}
			q.States[6900] = s
			if err := g.Quest.Save(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			before := secondAwakeningSnapshot(t, g)
			o, _ := NewOwner(g)
			r, err := o.UseClearCoupon(context.Background(), catalog, in)
			if mode == "answered_completed" {
				if err != nil || !r.Awakened || r.Remaining != 1 {
					t.Fatalf("valid completed choice rejected: %v", err)
				}
			} else if err == nil || !reflect.DeepEqual(before, secondAwakeningSnapshot(t, g)) {
				t.Fatal("awakening coupon bypassed an unfinished/damaged choice or mutated rejected state")
			}
		})
	}
}
