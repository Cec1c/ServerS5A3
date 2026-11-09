package memory

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

type weddingQuestCommitFailure struct {
	r.QuestRepository
	fired      bool
	panicWrite bool
}

func (q *weddingQuestCommitFailure) Save(ctx context.Context, record r.QuestRecord) error {
	if e := q.QuestRepository.Save(ctx, record); e != nil {
		return e
	}
	if record.CharacterID == "2" && !q.fired {
		q.fired = true
		if q.panicWrite {
			panic("wedding quest commit panic")
		}
		return errors.New("wedding quest commit failure")
	}
	return nil
}

func TestMemoryWeddingQuestsRollbackCharactersAndQuestsAfterMutation(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panics], func(t *testing.T) {
			g := NewMemoryGroup()
			ctx := context.Background()
			beforeChars := map[string]r.CharacterRecord{}
			beforeQuests := map[string]r.QuestRecord{}
			for _, id := range []string{"1", "2"} {
				c := r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Stats: map[string]int64{"gold": 900000, "wedding_v1_phase": 1}}
				if e := r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
					t.Fatal(e)
				}
				q := r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "active", ProgressValue: 5}}}
				if e := g.Quest.Save(ctx, q); e != nil {
					t.Fatal(e)
				}
				beforeChars[id], _, _ = g.Character.Load(ctx, id)
				beforeQuests[id], _, _ = g.Quest.Load(ctx, id)
			}
			u := g.CharacterTrade.(*memoryCharacterTradeUnitOfWork)
			u.quests = &weddingQuestCommitFailure{QuestRepository: g.Quest, panicWrite: panics}
			var err error
			recovered := false
			func() {
				defer func() {
					if recover() != nil {
						recovered = true
					}
				}()
				err = u.WithinWeddingQuests(ctx, []string{"2", "1"}, func(chars r.CharacterRepository, quests r.QuestRepository) error {
					for _, id := range []string{"1", "2"} {
						c, _, _ := chars.Load(ctx, id)
						c.Stats["gold"] -= 500000
						c.Stats["wedding_v1_phase"] = 0
						if e := r.SaveCharacterFields(ctx, chars, c, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
							return e
						}
						q, _, _ := quests.Load(ctx, id)
						delete(q.States, 20737)
						if e := r.SaveQuestFields(ctx, quests, q, r.QuestFieldStates); e != nil {
							return e
						}
					}
					return nil
				})
			}()
			if panics != recovered || !panics && err == nil {
				t.Fatalf("failure missing panic=%t recovered=%t err=%v", panics, recovered, err)
			}
			for _, id := range []string{"1", "2"} {
				c, _, _ := g.Character.Load(ctx, id)
				q, _, _ := g.Quest.Load(ctx, id)
				if !reflect.DeepEqual(c, beforeChars[id]) || !reflect.DeepEqual(q, beforeQuests[id]) {
					t.Fatalf("partial commit survived for %s", id)
				}
			}
		})
	}
}

func TestMemoryWeddingQuestsRejectOutOfPairAndInvalidScope(t *testing.T) {
	g := NewMemoryGroup()
	u := g.CharacterTrade.(r.WeddingQuestUnitOfWork)
	if e := u.WithinWeddingQuests(context.Background(), []string{"1", "1"}, func(r.CharacterRepository, r.QuestRepository) error { return nil }); e == nil {
		t.Fatal("same-character scope accepted")
	}
	if e := u.WithinWeddingQuests(context.Background(), []string{"1", "2"}, func(_ r.CharacterRepository, q r.QuestRepository) error {
		if _, _, e := q.Load(context.Background(), "3"); e == nil {
			t.Fatal("read outside pair")
		}
		if e := q.Save(context.Background(), r.QuestRecord{CharacterID: "3"}); e == nil {
			t.Fatal("write outside pair")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
