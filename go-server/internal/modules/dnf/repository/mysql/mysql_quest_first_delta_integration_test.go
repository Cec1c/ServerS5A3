//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func runFirstQuestDeltaRealDB(t *testing.T, ctx context.Context, db *sql.DB, schema string, g repository.Group) {
	t.Helper()
	for _, sameQuest := range []bool{true, false} {
		for round := 0; round < 8; round++ {
			id := fmt.Sprintf("fresh-%t-%d", sameQuest, round)
			before := repository.QuestRecord{CharacterID: id}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				qid := int64(1)
				if !sameQuest {
					qid += int64(i)
				}
				after := repository.QuestRecord{CharacterID: id, States: map[int64]repository.QuestState{qid: {Status: "active", ProgressValue: 1, Extra: map[string]string{"receipt": "first"}}}}
				go func() {
					<-start
					results <- repository.SaveQuestChanges(ctx, g.Quest, before, after, repository.QuestFieldStates)
				}()
			}
			close(start)
			success, conflicts := 0, 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					success++
				} else if errors.Is(err, repository.ErrQuestVersionConflict) {
					conflicts++
				} else {
					t.Fatalf("concurrent first write: %v", err)
				}
			}
			want := 2
			if sameQuest {
				want = 1
			}
			if success != want || conflicts != 2-want {
				t.Fatalf("success=%d conflict=%d want=%d", success, conflicts, want)
			}
			loaded, found, err := g.Quest.Load(ctx, id)
			if err != nil || !found || len(loaded.States) != want {
				t.Fatalf("lost/duplicated first quests: %+v %v", loaded, err)
			}
			for _, state := range loaded.States {
				if state.Extra["receipt"] != "first" {
					t.Fatal("lost first quest metadata")
				}
			}
		}
	}
	for _, partial := range []bool{false, true} {
		before := repository.QuestRecord{CharacterID: fmt.Sprintf("missing-stale-%t", partial), PlanningPartial: partial}
		if !partial {
			before.States = map[int64]repository.QuestState{1: {Status: "active"}}
		}
		after := repository.CloneQuest(before)
		after.States = map[int64]repository.QuestState{1: {Status: "completed"}}
		if err := repository.SaveQuestChanges(ctx, g.Quest, before, after, repository.QuestFieldStates); !errors.Is(err, repository.ErrQuestVersionConflict) {
			t.Fatalf("stale/partial absent parent revived: %v", err)
		}
		if _, found, err := g.Quest.Load(ctx, before.CharacterID); err != nil || found {
			t.Fatalf("stale parent persisted: %t %v", found, err)
		}
	}
	trigger := "CREATE TRIGGER `" + schema + "`.first_quest_fail BEFORE INSERT ON `" + schema + "`.`dnf_" + mysqlQuestExtraTable + "` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='first quest rollback'"
	if _, err := db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TRIGGER `" + schema + "`.first_quest_fail")
	before := repository.QuestRecord{CharacterID: "first-rollback"}
	after := repository.QuestRecord{CharacterID: before.CharacterID, States: map[int64]repository.QuestState{1: {Status: "active", Extra: map[string]string{"receipt": "must rollback"}}}}
	if err := repository.SaveQuestChanges(ctx, g.Quest, before, after, repository.QuestFieldStates); err == nil {
		t.Fatal("injected write failure ignored")
	}
	if _, found, err := g.Quest.Load(ctx, before.CharacterID); err != nil || found {
		t.Fatalf("first parent survived failed child write: %t %v", found, err)
	}
	for _, table := range []string{mysqlQuestStatesTable, mysqlQuestExtraTable} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+schema+"`.`dnf_"+table+"` WHERE character_id=?", before.CharacterID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial child writes: %d %v", n, err)
		}
	}
}
