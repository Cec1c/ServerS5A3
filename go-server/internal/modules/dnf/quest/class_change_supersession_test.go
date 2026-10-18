package quest

import (
	"context"
	"errors"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCatalogInitialClassChangeBranchSupersededByCompletedSuccessor(t *testing.T) {
	source := catalogTestSource{
		DefaultList: "3153 `mainline.qst`\n" +
			"3155 `forest_followup.qst`\n" +
			"6986 `forest_knight_item.qst`\n" +
			"6987 `forest_knight_grow.qst`\n" +
			"12757 `legacy_elven_knight.qst`\n" +
			"12758 `legacy_swordman.qst`\n" +
			"3156 `ordinary_followup.qst`\n" +
			"40000 `linked_parent.qst`\n" +
			"40001 `linked_child.qst`\n",
		"n_quest/mainline.qst": questCatalogTestDefinition("[epic]", 15, 99, "[all]", ""),
		"n_quest/forest_followup.qst": questCatalogTestDefinition("[epic]", 15, 99, "[all]",
			"[pre required quest]\n6986\n[pre required quest]\n6987\n"),
		"n_quest/forest_knight_item.qst": questCatalogTestDefinition("[epic]", 15, 99, "[all]",
			"[job change quest]\n1\n[pre required quest]\n3153\n[reward type]\n`[item]`\n[reward int data]\n0 0\n[target character]\n`[knight]` -1 -1\n"),
		"n_quest/forest_knight_grow.qst": questCatalogTestDefinition("[epic]", 15, 99, "[all]",
			"[job change quest]\n1\n[pre required quest]\n3153\n[reward type]\n`[grow type]`\n[reward int data]\n1\n[target character]\n`[knight]` 0 -1\n"),
		"n_quest/legacy_elven_knight.qst": questCatalogTestDefinition("[common unique]", 15, 99, "[knight]",
			"[job change quest]\n1\n[pre required quest]\n3155\n[reward type]\n`[grow type]`\n[reward int data]\n1\n"),
		"n_quest/legacy_swordman.qst": questCatalogTestDefinition("[common unique]", 15, 99, "[swordman]",
			"[job change quest]\n1\n[pre required quest]\n3155\n[reward type]\n`[grow type]`\n[reward int data]\n1\n"),
		"n_quest/ordinary_followup.qst": questCatalogTestDefinition("[epic]", 15, 99, "[knight]",
			"[pre required quest]\n3155\n"),
		"n_quest/linked_parent.qst": questCatalogTestDefinition("[epic]", 15, 99, "[knight]",
			"[type]\n`[quest clear]`\n[int data]\n40001\n"),
		"n_quest/linked_child.qst": questCatalogTestDefinition("[sub]", 15, 99, "[knight]",
			"[main quest]\n40000\n"),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	character := CharacterEligibility{Level: 15, Job: 12, GrowType: 0}

	beforeSuccessor := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3153: {Status: "completed"},
	}}
	for _, id := range []int64{6986, 6987} {
		if decision, err := catalog.DecideAccept(character, beforeSuccessor, id); err != nil || decision != AcceptNew {
			t.Fatalf("forest branch %d before 3155: decision=%v err=%v", id, decision, err)
		}
		if !questListed(catalog, character, beforeSuccessor, id) {
			t.Fatalf("forest branch %d missing from the pre-successor list", id)
		}
		if !questAcceptable(catalog, character, beforeSuccessor, id) {
			t.Fatalf("forest branch %d missing from the pre-successor Acceptable result", id)
		}
	}
	if decision, err := catalog.DecideAccept(character, beforeSuccessor, 12757); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("legacy branch without 3155: decision=%v err=%v", decision, err)
	}
	if questAcceptable(catalog, character, beforeSuccessor, 12757) || questListed(catalog, character, beforeSuccessor, 12757) {
		t.Fatal("legacy branch must be absent from both Acceptable and QuestList without 3155")
	}

	afterSuccessor := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3153: {Status: "completed"},
		3155: {Status: "completed"},
	}}
	for _, id := range []int64{6986, 6987} {
		if decision, err := catalog.DecideAccept(character, afterSuccessor, id); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
			t.Fatalf("superseded forest branch %d: decision=%v err=%v", id, decision, err)
		}
		if questListed(catalog, character, afterSuccessor, id) {
			t.Fatalf("superseded forest branch %d remained visible", id)
		}
		if questAcceptable(catalog, character, afterSuccessor, id) {
			t.Fatalf("superseded forest branch %d remained in Acceptable", id)
		}
	}
	if decision, err := catalog.DecideAccept(character, afterSuccessor, 12757); err != nil || decision != AcceptNew {
		t.Fatalf("legacy branch after 3155: decision=%v err=%v", decision, err)
	}
	if !questListed(catalog, character, afterSuccessor, 12757) {
		t.Fatal("legacy branch after 3155 missing from the list")
	}
	if !questAcceptable(catalog, character, afterSuccessor, 12757) {
		t.Fatal("legacy branch after 3155 missing from Acceptable")
	}

	foreignActive := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3153:  {Status: "completed"},
		3155:  {Status: "completed"},
		12758: {Status: "active"},
	}}
	if decision, err := catalog.DecideAccept(character, foreignActive, 12757); err != nil || decision != AcceptNew {
		t.Fatalf("foreign active transfer must not lock job 12: decision=%v err=%v", decision, err)
	}

	activeForest := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3153: {Status: "completed"},
		3155: {Status: "completed"},
		6986: {Status: "active"},
	}}
	if !questListed(catalog, character, activeForest, 6986) {
		t.Fatal("an already active superseded branch must remain visible for completion or abandonment")
	}
	if questListed(catalog, character, activeForest, 12757) || questAcceptable(catalog, character, activeForest, 12757) {
		t.Fatal("a same-job active transfer sibling must be hidden by both QuestList and Acceptable")
	}
	if decision, err := catalog.DecideAccept(character, activeForest, 12757); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("legacy transfer accepted beside active forest branch: decision=%v err=%v", decision, err)
	}

	ordinary := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3153:  {Status: "completed"},
		3155:  {Status: "completed"},
		12757: {Status: "active"},
	}}
	if decision, err := catalog.DecideAccept(character, ordinary, 3156); err != nil || decision != AcceptNew {
		t.Fatalf("ordinary follow-up must stay independent: decision=%v err=%v", decision, err)
	}

	parent, found := catalog.Find(40000)
	if !found {
		t.Fatal("linked parent missing")
	}
	linked := catalog.linkedSubQuestAcceptPlans(
		parent,
		character,
		map[int64]struct{}{3153: {}},
		map[int64]struct{}{6986: {}},
	)
	if len(linked) != 1 || linked[0].QuestID != 40001 {
		t.Fatalf("linked subquest plan was blocked by an active initial-transfer sibling: %+v", linked)
	}

	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19", AccountID: "acc", Job: "12", Level: 15,
		Stats: map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	activeLegacy := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3153:  {Status: "completed"},
		3155:  {Status: "completed"},
		12757: {Status: "active"},
	}}
	if err := repos.Quest.Save(ctx, activeLegacy); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if !questListed(catalog, character, activeLegacy, 12757) {
		t.Fatal("active legacy transfer must remain in QuestList for abandon/completion")
	}
	if decision, err := catalog.DecideAccept(character, activeLegacy, 12757); err != nil || decision != AcceptIdempotent {
		t.Fatalf("active legacy transfer must be idempotent: decision=%v err=%v", decision, err)
	}
	if _, err := owner.ApplyGiveUp(ctx, catalog, Command{
		Operation:           "giveup_quest",
		AccountID:           "acc",
		SelectedCharacterID: 19,
		QuestID:             12757,
	}); err != nil {
		t.Fatalf("active legacy transfer must remain abandonable: %v", err)
	}
	afterGiveUp, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload after legacy give-up found=%t err=%v", found, err)
	}
	if _, exists := afterGiveUp.States[12757]; exists {
		t.Fatal("legacy give-up left the active row behind")
	}
}

func questAcceptable(catalog *Catalog, character CharacterEligibility, record dnfrepo.QuestRecord, questID int64) bool {
	for _, id := range catalog.Acceptable(character, record).IDs {
		if int64(id) == questID {
			return true
		}
	}
	return false
}
