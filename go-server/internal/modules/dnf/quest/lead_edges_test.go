package quest

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestLeadQuestionDecodeMissingAnswer(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("missing answer caused panic: %v", r)
		}
	}()
	r, e := DecodeSetTriggerRequest([]byte{33, 0, 0x1c, 0x0f, 0})
	if e != nil {
		t.Fatal(e)
	}
	if r.HasAnswer {
		t.Fatalf("absent answer reported present: %+v", r)
	}
}

func TestLeadQuestionConcurrentChoicesConfirmOnlyOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	repos := dnfrepomemory.NewMemoryGroup()
	if e := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acc", Level: 90, Job: "2"}); e != nil {
		t.Fatal(e)
	}
	if e := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3868: {Status: "active", ProgressValue: 1},
	}}); e != nil {
		t.Fatal(e)
	}
	owner, e := NewOwner(repos)
	if e != nil {
		t.Fatal(e)
	}
	catalog := questionOwnerTestCatalog(t)
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, answer := range []byte{1, 2} {
		go func(a byte) {
			<-start
			_, e := owner.ApplyQuestionAnswer(ctx, catalog, questionOwnerCommand(19, 3868, 0, a, true))
			results <- e
		}(answer)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if errors.Is(e, ErrQuestionAnswerConflict) {
			conflict++
		} else {
			t.Errorf("answer error=%v", e)
		}
	}
	row, _, _ := repos.Quest.Load(ctx, "19")
	state := row.States[3868]
	if success != 1 || conflict != 1 || state.ProgressValue != 0 ||
		(state.Extra[questionAnswerExtraKey] != "1" && state.Extra[questionAnswerExtraKey] != "2") {
		t.Fatalf("concurrent choices: success=%d conflict=%d state=%+v, want one confirmed choice", success, conflict, state)
	}
}
