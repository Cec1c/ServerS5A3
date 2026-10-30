package quest

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func huntHistoryFixture() (*Catalog, repo.QuestRecord, HuntEnemyKillInput) {
	c := &Catalog{byID: map[int64]Definition{1: {ID: 1, Type: "[hunt enemy]", IntData: []int64{-1, -1, 77110, 1, 2}}}}
	r := repo.QuestRecord{CharacterID: "history", States: map[int64]repo.QuestState{1: {Status: "active", ProgressValue: 2, Extra: map[string]string{"keep": "yes"}}}}
	for i := int64(2); i < 675; i++ {
		r.States[i] = repo.QuestState{Status: "completed", Extra: map[string]string{
			"completion_key": fmt.Sprint(i), "reward_state": "granted", "pvf_path": "quest/history.qst",
			"quest_type": "hunt enemy", "audit": "unchanged", "kept": "yes",
		}}
	}
	return c, r, HuntEnemyKillInput{EnemyCode: 77110, EnemyType: 1, CompletionKey: "kill-1", CompletedAt: time.Unix(1800000000, 0)}
}

func TestHuntHistoryInputIsolationAndStatePrecedence(t *testing.T) {
	c, record, input := huntHistoryFixture()
	// A historical States entry must still shadow a Progress entry of the same ID.
	c.byID[2] = Definition{ID: 2, Type: "[hunt enemy]", IntData: []int64{-1, -1, 77110, 1, 2}}
	record.Progress = map[int64]repo.QuestState{2: {Status: "active", ProgressValue: 2}}
	before := repo.CloneQuest(record)
	p, err := c.PlanHuntEnemyKill(record, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Completions) != 1 || p.Completions[0].QuestID != 1 || p.Record.States[1].ProgressValue != 1 {
		t.Fatalf("incorrect completion or duplicate precedence: %+v", p.Completions)
	}
	if !reflect.DeepEqual(record, before) {
		t.Fatal("planner changed its input")
	}
	for id, state := range before.States {
		if id != 1 && !reflect.DeepEqual(state, p.Record.States[id]) {
			t.Fatalf("history %d changed", id)
		}
	}
	// Plans remain independent mutable snapshots, including archived Extra maps.
	s := p.Record.States[2]
	s.Extra["audit"] = "changed"
	if record.States[2].Extra["audit"] != "unchanged" {
		t.Fatal("plan aliases input history")
	}
}

func BenchmarkHuntEnemyHistoricalTasks(b *testing.B) {
	c, record, input := huntHistoryFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.PlanHuntEnemyKill(record, input); err != nil {
			b.Fatal(err)
		}
	}
}
