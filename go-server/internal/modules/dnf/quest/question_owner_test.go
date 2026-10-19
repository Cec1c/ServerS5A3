package quest

import (
	"context"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestQuestionCatalogParsesOptionsAndAnswerPrerequisites(t *testing.T) {
	catalog := questionOwnerTestCatalog(t)
	if got, ok := catalog.QuestionOptionCount(3868); !ok || got != 3 {
		t.Fatalf("quest 3868 options=%d question=%t want 3/true", got, ok)
	}
	if got, ok := catalog.QuestionOptionCount(3869); !ok || got != 2 {
		t.Fatalf("quest 3869 options=%d question=%t want 2/true", got, ok)
	}

	character := CharacterEligibility{Level: 90, Job: 2}
	for _, test := range []struct {
		question int64
		answer   string
		want     int32
	}{
		{3868, "0", 3870},
		{3868, "1", 3871},
		{3868, "2", 3873},
		{3869, "0", 3872},
		{3869, "1", 3874},
	} {
		record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			3867: {Status: "completed"},
			test.question: {
				Status: "completed",
				Extra: map[string]string{
					questionAnswerExtraKey: test.answer,
				},
			},
		}}
		list := catalog.QuestList(character, record).IDs
		if !questionContainsID(list, test.want) {
			t.Fatalf("question=%d answer=%s list=%v, want branch %d", test.question, test.answer, list, test.want)
		}
		if _, err := catalog.PlanAccept(character, record, int64(test.want)); err != nil {
			t.Fatalf("question=%d answer=%s PlanAccept branch=%d error=%v", test.question, test.answer, test.want, err)
		}
		for _, blocked := range []int32{3870, 3871, 3872, 3873, 3874} {
			if blocked != test.want && questionContainsID(list, blocked) {
				t.Fatalf("question=%d answer=%s list=%v exposed wrong branch %d", test.question, test.answer, list, blocked)
			}
		}
	}

	withoutAnswer := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3867: {Status: "completed"},
		3868: {Status: "completed"},
	}}
	if got := catalog.QuestList(character, withoutAnswer).IDs; questionContainsAny(got, 3870, 3871, 3873) {
		t.Fatalf("answerless completed question exposed a branch: %v", got)
	}
	wrongAnswer := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3867: {Status: "completed"},
		3868: {Status: "completed", Extra: map[string]string{questionAnswerExtraKey: "2"}},
	}}
	if got := catalog.QuestList(character, wrongAnswer).IDs; questionContainsID(got, 3870) || questionContainsID(got, 3871) {
		t.Fatalf("wrong persisted answer exposed a branch: %v", got)
	}
	activeAnswered := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3867: {Status: "completed"},
		3868: {Status: "active", Extra: map[string]string{questionAnswerExtraKey: "1"}},
	}}
	if got := catalog.QuestList(character, activeAnswered).IDs; questionContainsID(got, 3871) {
		t.Fatalf("active answered question exposed its successor before completion: %v", got)
	}
	collision := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3867: {Status: "completed"},
		3868: {Status: "completed", Extra: map[string]string{questionAnswerExtraKey: "1"}},
		3872: {Status: "completed"},
	}}
	if got := catalog.QuestList(character, collision).IDs; questionContainsID(got, 3871) {
		t.Fatalf("completed collision quest exposed branch 3871: %v", got)
	}
}

func TestDecodeQuestionAnswerAndCommandPreserveRawByteAndPresence(t *testing.T) {
	for _, test := range []struct {
		body    []byte
		answer  byte
		present bool
	}{
		{[]byte{0x21, 0x00, 0x1c, 0x0f, 0x00, 0x00}, 0, true},
		{[]byte{0x21, 0x00, 0x1c, 0x0f, 0x00, 0x02}, 2, true},
		{[]byte{0x21, 0x00, 0x1c, 0x0f, 0x00}, 0, false},
	} {
		got, err := DecodeSetTriggerRequest(test.body)
		if err != nil {
			t.Fatal(err)
		}
		if got.Answer != test.answer || got.HasAnswer != test.present {
			t.Fatalf("decode body=%x answer=%d present=%t want=%d/%t", test.body, got.Answer, got.HasAnswer, test.answer, test.present)
		}
		cmd := NewTriggerCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 19}, got)
		if cmd.Answer != test.answer || cmd.HasAnswer != test.present {
			t.Fatalf("command=%+v want answer=%d/%t", cmd, test.answer, test.present)
		}
	}
}

func TestQuestionOwnerPersistsAnswerAcrossOwnersAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acc", Job: "2", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3867: {Status: "completed"},
		3868: {Status: "active", ProgressValue: 7},
	}}); err != nil {
		t.Fatal(err)
	}
	catalog := questionOwnerTestCatalog(t)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.ApplyQuestionAnswer(ctx, catalog, questionOwnerCommand(19, 3868, 0, 1, true))
	if err != nil {
		t.Fatal(err)
	}
	if !first.StateChanged || first.ProgressValue != 0 {
		t.Fatalf("first answer result=%+v", first)
	}

	reloaded, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reloaded.ApplyQuestionAnswer(ctx, catalog, questionOwnerCommand(19, 3868, 0, 1, true))
	if err != nil || replay.StateChanged || replay.ProgressValue != 0 {
		t.Fatalf("same answer replay=%+v err=%v", replay, err)
	}
	if _, err := reloaded.ApplyQuestionAnswer(ctx, catalog, questionOwnerCommand(19, 3868, 0, 2, true)); !errors.Is(err, ErrQuestionAnswerConflict) {
		t.Fatalf("different answer error=%v", err)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load answered state found=%t err=%v", found, err)
	}
	state := stored.States[3868]
	if state.ProgressValue != 0 || state.Extra[questionAnswerExtraKey] != "1" || state.Extra[questionOptionCountExtraKey] != "3" {
		t.Fatalf("persisted answer state=%+v", state)
	}
}

func TestQuestionOwnerRejectsMissingInvalidInactiveAndWrongChannel(t *testing.T) {
	ctx := context.Background()
	catalog := questionOwnerTestCatalog(t)
	for _, test := range []struct {
		name   string
		status string
		cmd    Command
		want   error
	}{
		{"missing", "active", questionOwnerCommand(19, 3868, 0, 0, false), ErrQuestionAnswerRequired},
		{"out-of-range", "active", questionOwnerCommand(19, 3868, 0, 3, true), ErrQuestionAnswerInvalid},
		{"wrong-channel", "active", questionOwnerCommand(19, 3868, 1, 1, true), ErrQuestionAnswerInvalid},
		{"inactive", "completed", questionOwnerCommand(19, 3868, 0, 1, true), ErrQuestNotActive},
	} {
		t.Run(test.name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acc"}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3868: {Status: test.status, ProgressValue: 2},
			}}); err != nil {
				t.Fatal(err)
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ApplyQuestionAnswer(ctx, catalog, test.cmd); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			stored, _, _ := repos.Quest.Load(ctx, "19")
			if state := stored.States[3868]; state.ProgressValue != 2 || state.Extra[questionAnswerExtraKey] != "" {
				t.Fatalf("rejected answer mutated state=%+v", state)
			}
		})
	}
}

func TestQuestionAnswerGiveUpAndReacceptStartsCleanGeneration(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acc", Job: "2", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3867: {Status: "completed"},
		3868: {Status: "active", ProgressValue: 0, Extra: map[string]string{questionAnswerExtraKey: "1"}},
	}}); err != nil {
		t.Fatal(err)
	}
	catalog := questionOwnerTestCatalog(t)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyGiveUp(ctx, catalog, questionOwnerCommand(19, 3868, 0, 0, false)); err != nil {
		t.Fatal(err)
	}
	afterGiveUp, _, _ := repos.Quest.Load(ctx, "19")
	if _, exists := afterGiveUp.States[3868]; exists {
		t.Fatalf("give-up retained previous generation: %+v", afterGiveUp.States[3868])
	}
	accepted, err := owner.ApplyAccept(ctx, catalog, CharacterEligibility{Level: 90, Job: 2}, questionOwnerCommand(19, 3868, 0, 0, false))
	if err != nil || accepted.Idempotent {
		t.Fatalf("reaccept=%+v err=%v", accepted, err)
	}
	afterAccept, _, _ := repos.Quest.Load(ctx, "19")
	state := afterAccept.States[3868]
	if state.Status != "active" || state.Extra[questionAnswerExtraKey] != "" {
		t.Fatalf("reaccepted question retained previous answer: %+v", state)
	}
}

func TestQuestionAnswerSaveFailureDoesNotMutatePersistedState(t *testing.T) {
	ctx := context.Background()
	base := dnfrepomemory.NewMemoryGroup()
	if err := base.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acc"}); err != nil {
		t.Fatal(err)
	}
	if err := base.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3868: {Status: "active", ProgressValue: 2},
	}}); err != nil {
		t.Fatal(err)
	}
	saveErr := errors.New("transaction quest save failed")
	repos := base
	repos.CharacterSettlement = &questionAnswerTestSettlement{
		character: base.Character,
		quest:     &questionAnswerFailingQuestStore{QuestRepository: base.Quest, err: saveErr},
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyQuestionAnswer(ctx, questionOwnerTestCatalog(t), questionOwnerCommand(19, 3868, 0, 1, true)); !errors.Is(err, saveErr) {
		t.Fatalf("save failure error=%v want=%v", err, saveErr)
	}
	stored, _, _ := base.Quest.Load(ctx, "19")
	if state := stored.States[3868]; state.ProgressValue != 2 || state.Extra[questionAnswerExtraKey] != "" {
		t.Fatalf("save failure mutated base state=%+v", state)
	}
}

func TestQuestionAnswerRejectsMissingTransactionAndWrongAccount(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "acc"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3868: {Status: "active", ProgressValue: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	catalog := questionOwnerTestCatalog(t)
	transaction := repos.CharacterSettlement

	repos.CharacterSettlement = nil
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyQuestionAnswer(ctx, catalog, questionOwnerCommand(19, 3868, 0, 1, true)); !errors.Is(err, dnfrepo.ErrCharacterSettlementTransactionUnavailable) {
		t.Fatalf("nil transaction error=%v", err)
	}
	if stored, _, _ := repos.Quest.Load(ctx, "19"); stored.States[3868].ProgressValue != 1 || stored.States[3868].Extra[questionAnswerExtraKey] != "" {
		t.Fatalf("nil transaction mutated quest=%+v", stored.States[3868])
	}

	repos.CharacterSettlement = transaction
	owner, err = NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	wrongAccount := NewTriggerCommand(alignedcmd.Request{AccountID: "other", SelectedCharacterID: 19}, SetTriggerRequest{
		QuestID: 3868, TriggerType: 0, Answer: 1, HasAnswer: true,
	})
	if _, err := owner.ApplyQuestionAnswer(ctx, catalog, wrongAccount); !errors.Is(err, ErrCharacterNotFound) {
		t.Fatalf("wrong account error=%v", err)
	}
	if stored, _, _ := repos.Quest.Load(ctx, "19"); stored.States[3868].ProgressValue != 1 || stored.States[3868].Extra[questionAnswerExtraKey] != "" {
		t.Fatalf("wrong account mutated quest=%+v", stored.States[3868])
	}
}

func TestQuestionFinishRequiresAnswerAndReplaysWithoutDuplicateReward(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	acceptedAt := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3868: {Status: "active", ProgressValue: 0, UpdatedAt: acceptedAt},
	}}); err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		ID: 3868, Path: "n_quest/question.qst", Type: "[question]", QuestionOptionCount: 3,
		LevelMin: 1, LevelMax: 99, Difficulty: "N", RewardType: "[item]",
	}
	catalog := &Catalog{ordered: []Definition{definition}, byID: map[int64]Definition{3868: definition}}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := FinishCommitInput{
		AccountID: "acc", CharacterID: "19", QuestID: 3868, RewardSelectIndex: ^uint16(0),
		Multiplier: 1, CommittedAt: acceptedAt.Add(time.Minute), Progression: finishOwnerProgression(t),
	}
	if _, err := owner.ApplyFinishSettlement(ctx, catalog, input); !errors.Is(err, ErrFinishQuestionAnswerRequired) {
		t.Fatalf("answerless finish error=%v", err)
	}
	record, _, _ := repos.Quest.Load(ctx, "19")
	state := record.States[3868]
	state.Extra = map[string]string{questionAnswerExtraKey: "0", questionOptionCountExtraKey: "3"}
	record.States[3868] = state
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.AtomicCommitted || first.Replayed || !second.Replayed || first.CompletionKey != second.CompletionKey {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	completed, _, _ := repos.Quest.Load(ctx, "19")
	if got := completed.States[3868]; got.Status != "completed" || got.Extra[questionAnswerExtraKey] != "0" {
		t.Fatalf("completed question state=%+v", got)
	}
}

type questionAnswerFailingQuestStore struct {
	dnfrepo.QuestRepository
	err error
}

func (s *questionAnswerFailingQuestStore) Save(context.Context, dnfrepo.QuestRecord) error {
	return s.err
}

type questionAnswerTestSettlement struct {
	character dnfrepo.CharacterRepository
	quest     dnfrepo.QuestRepository
}

func (u *questionAnswerTestSettlement) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	if u == nil || u.character == nil || u.quest == nil || apply == nil {
		return dnfrepo.ErrCharacterSettlementTransactionUnavailable
	}
	return apply(dnfrepo.Group{Character: u.character, Quest: u.quest})
}

func questionOwnerCommand(characterID uint16, questID uint16, triggerType, answer byte, hasAnswer bool) Command {
	return NewTriggerCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: characterID}, SetTriggerRequest{
		QuestID: questID, TriggerType: triggerType, Answer: answer, HasAnswer: hasAnswer, IsIncrement: answer != 0,
	})
}

func questionContainsID(ids []int32, want int32) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func questionContainsAny(ids []int32, wants ...int32) bool {
	for _, want := range wants {
		if questionContainsID(ids, want) {
			return true
		}
	}
	return false
}

type questionOwnerPVFSource map[string]string

func (s questionOwnerPVFSource) ReadText(path string) (string, error) {
	text, ok := s[path]
	if !ok {
		return "", errors.New("missing test PVF path: " + path)
	}
	return text, nil
}

func questionOwnerTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	base := func(extra string) string {
		return "[grade]\n`[epic]`\n[difficulty]\n`N`\n[level]\n1 99\n[job]\n`[all]`\n[exposed by npc]\n1\n" + extra
	}
	source := questionOwnerPVFSource{
		DefaultList:          "3867 `parent.qst`\n3868 `q3.qst`\n3869 `q2.qst`\n3870 `a0.qst`\n3871 `a1.qst`\n3872 `b0.qst`\n3873 `a2.qst`\n3874 `b1.qst`\n",
		"n_quest/parent.qst": base("[type]\n`[meet npc]`\n"),
		"n_quest/q3.qst": base("[type]\n`[question]`\n[pre required quest]\n3867\n[collision quest]\n3869\n" +
			"[question info]\n`Question one` `Question two` `Zero` `One` `Two`\n"),
		"n_quest/q2.qst": base("[type]\n`[question]`\n[pre required quest]\n3867\n[collision quest]\n3868\n" +
			"[question info]\n`Question one` `Question two` `Zero` `One`\n"),
		"n_quest/a0.qst": base("[type]\n`[meet npc]`\n[pre required quest answer]\n3868 0\n"),
		"n_quest/a1.qst": base("[type]\n`[meet npc]`\n[pre required quest answer]\n3868 1\n[collision quest]\n3872\n"),
		"n_quest/a2.qst": base("[type]\n`[meet npc]`\n[pre required quest answer]\n3868 2\n[collision quest]\n3874\n"),
		"n_quest/b0.qst": base("[type]\n`[meet npc]`\n[pre required quest answer]\n3869 0\n[collision quest]\n3871\n"),
		"n_quest/b1.qst": base("[type]\n`[meet npc]`\n[pre required quest answer]\n3869 1\n[collision quest]\n3873\n"),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
