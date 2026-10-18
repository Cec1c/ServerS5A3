package quest

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// TestRealPVFInitialClassChangeLayerSupersededByCompleted3155 covers the
// reported a56 overlap between the legacy transfer task and the renewal-forest
// branch. 3155 names every level-15 forest branch as an alternative prerequisite;
// once 3155 is completed, those earlier branches have already been consumed.
// The test loads the real archive only once and checks every ordinary job.
func TestRealPVFInitialClassChangeLayerSupersededByCompleted3155(t *testing.T) {
	catalog := level15BranchRealPVFCatalog(t)

	for _, id := range []int64{12757, 6986, 6987} {
		definition, found := catalog.Find(id)
		if !found {
			t.Fatalf("a56 quest %d missing", id)
		}
		if definition.JobChangeQuest != 1 {
			t.Fatalf("a56 quest %d job-change type=%d, want 1", id, definition.JobChangeQuest)
		}
	}
	if definition, _ := catalog.Find(12757); definition.Name != "转职 - 精灵骑士" {
		t.Fatalf("a56 quest 12757 name=%q", definition.Name)
	}
	if definition, _ := catalog.Find(6986); definition.Name != "守护森林的战斗" {
		t.Fatalf("a56 quest 6986 name=%q", definition.Name)
	}
	if definition, _ := catalog.Find(6987); definition.Name != "守护森林的战斗 - 精灵骑士" {
		t.Fatalf("a56 quest 6987 name=%q", definition.Name)
	}

	for job := 0; job <= jobmap.MaxID; job++ {
		if !jobmap.Valid(job) {
			continue
		}
		legacy := level15JobChangeQuestIDs(catalog, job)
		if len(legacy) == 0 {
			continue
		}
		character := CharacterEligibility{Level: 15, Job: job, GrowType: 0}
		forest := forestInitialClassChangeQuestIDs(catalog, character)
		if len(forest) == 0 {
			t.Fatalf("job=%d has legacy transfers %v but no renewal-forest branch", job, legacy)
		}

		before := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			3153: {Status: "completed"},
		}}
		for _, id := range forest {
			if decision, err := catalog.DecideAccept(character, before, id); err != nil || decision != AcceptNew {
				t.Fatalf("job=%d forest quest %d before 3155: decision=%v err=%v", job, id, decision, err)
			}
			if !questListed(catalog, character, before, id) {
				t.Fatalf("job=%d forest quest %d missing before 3155", job, id)
			}
		}

		after := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			3153: {Status: "completed"},
			3155: {Status: "completed"},
		}}
		for _, id := range forest {
			if decision, err := catalog.DecideAccept(character, after, id); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
				t.Fatalf("job=%d forest quest %d after 3155: decision=%v err=%v", job, id, decision, err)
			}
			if questListed(catalog, character, after, id) {
				t.Fatalf("job=%d forest quest %d remained visible after completed 3155", job, id)
			}
		}
		for _, id := range legacy {
			if decision, err := catalog.DecideAccept(character, after, id); err != nil || decision != AcceptNew {
				t.Fatalf("job=%d legacy quest %d after 3155: decision=%v err=%v", job, id, decision, err)
			}
			if !questListed(catalog, character, after, id) {
				t.Fatalf("job=%d legacy quest %d missing after 3155", job, id)
			}
		}
	}

	// Exercise the production accept owner once with the reported knight pair.
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19", AccountID: "acc", Job: "12", Level: 15,
		Stats: map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3153: {Status: "completed"},
		3155: {Status: "completed"},
	}}
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	eligibility := CharacterEligibility{Level: 15, Job: 12, GrowType: 0}
	for _, id := range []int64{6986, 6987} {
		_, acceptErr := owner.ApplyAccept(ctx, catalog, eligibility,
			NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: uint16(id)}))
		if !errors.Is(acceptErr, ErrQuestNotAcceptable) {
			t.Fatalf("ApplyAccept forest quest %d err=%v", id, acceptErr)
		}
	}
	if _, err := owner.ApplyAccept(ctx, catalog, eligibility,
		NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: 12757})); err != nil {
		t.Fatalf("ApplyAccept legacy elven-knight quest: %v", err)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload accepted record found=%t err=%v", found, err)
	}
	for _, id := range []int64{6986, 6987} {
		if _, exists := stored.States[id]; exists {
			t.Fatalf("refused forest quest %d wrote a row", id)
		}
	}
	if state := stored.States[12757]; !isActiveQuestStatus(state.Status) {
		t.Fatalf("legacy transfer state=%+v, want active", state)
	}

	// A historical active forest branch is exactly the case where successor
	// filtering alone cannot protect the list: 6986 is allowed to stay visible
	// for completion/abandonment, but the same-job legacy 12757 must be hidden by
	// the active-family conflict in QuestList as well as rejected by DecideAccept.
	activeForest := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		3153: {Status: "completed"},
		3155: {Status: "completed"},
		6986: {Status: "active"},
	}}
	listed := catalog.QuestList(CharacterEligibility{Level: 15, Job: 12, GrowType: 0}, activeForest)
	if !questIDsContain(listed.IDs, 6986) {
		t.Fatal("active forest branch must stay visible for completion or abandonment")
	}
	if questIDsContain(listed.IDs, 12757) {
		t.Fatal("QuestList offered legacy transfer beside an active same-job forest branch")
	}
	if questIDsContain(catalog.Acceptable(CharacterEligibility{Level: 15, Job: 12, GrowType: 0}, activeForest).IDs, 12757) {
		t.Fatal("Acceptable offered legacy transfer beside an active same-job forest branch")
	}
	if decision, err := catalog.DecideAccept(CharacterEligibility{Level: 15, Job: 12, GrowType: 0}, activeForest, 12757); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("active-family legacy accept decision=%v err=%v", decision, err)
	}
}

func forestInitialClassChangeQuestIDs(catalog *Catalog, character CharacterEligibility) []int64 {
	ids := make([]int64, 0, 5)
	for _, definition := range catalog.ordered {
		if definition.ID < level15BranchMinID+1 || definition.ID > level15BranchMaxID ||
			definition.JobChangeQuest != 1 || !definitionMatchesCharacter(definition, character) {
			continue
		}
		foundPrerequisite := false
		for _, group := range definition.PreRequiredGroups {
			for _, prerequisite := range group {
				if prerequisite == 3153 {
					foundPrerequisite = true
				}
			}
		}
		if foundPrerequisite {
			ids = append(ids, definition.ID)
		}
	}
	return ids
}

func questIDsContain(ids []int32, target int64) bool {
	for _, id := range ids {
		if int64(id) == target {
			return true
		}
	}
	return false
}
