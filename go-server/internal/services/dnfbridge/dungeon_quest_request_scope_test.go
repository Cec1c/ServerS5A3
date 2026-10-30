package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type countingKillQuestPlanning struct {
	dnfrepo.QuestRepository
	reads        int
	readsAtWrite []int
}

func (r *countingKillQuestPlanning) LoadQuestPlanning(ctx context.Context, id string) (dnfrepo.QuestRecord, bool, error) {
	r.reads++
	return r.QuestRepository.Load(ctx, id)
}

func (r *countingKillQuestPlanning) SaveQuestChanges(ctx context.Context, before, after dnfrepo.QuestRecord, fields ...dnfrepo.QuestField) error {
	r.readsAtWrite = append(r.readsAtWrite, r.reads)
	return dnfrepo.SaveQuestChanges(ctx, r.QuestRepository, before, after, fields...)
}

func TestDungeonKillSharesQuestPlanningOnlyWithinOneRequest(t *testing.T) {
	for _, padded := range []bool{false, true} {
		t.Run(fmt.Sprint("padded=", padded), func(t *testing.T) {
			s, session, repos, first := qst017Fixture(t, 3)
			if padded {
				session.dungeon.runtime.Character.CharacterID = " " + session.dungeon.runtime.Character.CharacterID + " "
			}
			counter := &countingKillQuestPlanning{QuestRepository: repos.Quest}
			repos.Quest = counter
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			monsters := session.dungeon.runtime.Room.Snapshot().Monsters
			var second uint32
			for _, monster := range monsters {
				if monster.ObjectKey != first {
					second = monster.ObjectKey
					break
				}
			}
			if second == 0 {
				t.Fatal("fixture requires two separate kills")
			}
			for index, key := range []uint32{first, second} {
				if err := qst017Death(s, session, key); err != nil {
					t.Fatal(err)
				}
				// Enemy planning and drop matching make no quest write in this
				// fixture. Monster planning must reuse their snapshot, then commit.
				// The final active-list response reads fresh state after invalidation.
				if len(counter.readsAtWrite) != index+1 || counter.readsAtWrite[index] != index*2+1 {
					t.Fatalf("planning before writes=%v", counter.readsAtWrite)
				}
				if counter.reads != (index+1)*2 {
					t.Fatalf("kill %d planning reads=%d want=%d", index, counter.reads, (index+1)*2)
				}
				if got := qst017State(t, repos).ProgressValue; got != int64(2-index) {
					t.Fatalf("kill %d progress=%d", index, got)
				}
			}
		})
	}
}
