package quest

import (
	"context"
	"os"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// professionOverrideTestSource mirrors the runtime Script.pvf shapes that the
// grow-type override depends on: one class-change terminal per branch with
// [collision quest] across branches, a shared "congrats" follow-up, a
// [target character] gated awakening chain, and one main-story prerequisite
// that must never be touched.
func professionOverrideTestSource() catalogTestSource {
	return catalogTestSource{
		DefaultList: "6926 `job/b1.qst`\n" +
			"6881 `job/b2.qst`\n" +
			"6927 `job/congrats.qst`\n" +
			"3356 `story/main.qst`\n" +
			"6884 `awaken/first_1.qst`\n" +
			"6885 `awaken/first_2.qst`\n" +
			"6887 `awaken/first_3.qst`\n" +
			"6888 `awaken/b2_first.qst`\n" +
			"6900 `awaken/second_1.qst`\n" +
			"6905 `awaken/second_6.qst`\n",
		"n_quest/job/b1.qst": questCatalogTestDefinition("[common unique]", 15, 99, "[gunner]",
			"[job change quest]\n1\n[collision quest]\n6881\n[reward type]\n`[grow type]`\n[reward int data]\n1\n"),
		"n_quest/job/b2.qst": questCatalogTestDefinition("[common unique]", 15, 99, "[gunner]",
			"[job change quest]\n1\n[collision quest]\n6926\n[reward type]\n`[grow type]`\n[reward int data]\n2\n"),
		"n_quest/job/congrats.qst": questCatalogTestDefinition("[common unique]", 15, 99, "[gunner]",
			"[job change quest]\n1\n[pre required quest]\n6926\n[pre required quest]\n6881\n"),
		"n_quest/story/main.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]", ""),
		"n_quest/awaken/first_1.qst": questCatalogTestDefinition("[achievement]", 50, 99, "[gunner]",
			"[job change quest]\n2\n[pre required quest]\n3356\n[grow type]\n1\n[target character]\n`[gunner]` 1 0\n"),
		"n_quest/awaken/first_2.qst": questCatalogTestDefinition("[achievement]", 50, 99, "[gunner]",
			"[job change quest]\n2\n[grow type]\n1\n[target character]\n`[gunner]` 1 0\n[pre required quest]\n6884\n"),
		"n_quest/awaken/first_3.qst": questCatalogTestDefinition("[achievement]", 50, 99, "[gunner]",
			"[job change quest]\n2\n[grow type]\n1\n[target character]\n`[gunner]` 1 0\n[pre required quest]\n6885\n[reward type]\n`[awakening type]`\n[reward int data]\n1\n"),
		"n_quest/awaken/b2_first.qst": questCatalogTestDefinition("[achievement]", 50, 99, "[gunner]",
			"[job change quest]\n2\n[grow type]\n2\n[target character]\n`[gunner]` 2 0\n[reward type]\n`[awakening type]`\n[reward int data]\n1\n"),
		"n_quest/awaken/second_1.qst": questCatalogTestDefinition("[achievement]", 75, 99, "[gunner]",
			"[job change quest]\n3\n[target character]\n`[gunner]` 1 1\n"),
		"n_quest/awaken/second_6.qst": questCatalogTestDefinition("[achievement]", 75, 99, "[gunner]",
			"[job change quest]\n3\n[grow type]\n1\n[pre required quest]\n6900\n[reward type]\n`[awakening type]`\n[reward int data]\n2\n"),
	}
}

func loadProfessionOverrideTestCatalog(t *testing.T, source catalogTestSource) *Catalog {
	t.Helper()
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

func TestProfessionProgressionQuestIDsFollowsGrowStageAndBranch(t *testing.T) {
	catalog := loadProfessionOverrideTestCatalog(t, professionOverrideTestSource())
	tests := []struct {
		name     string
		job      int
		growType int
		want     []int64
	}{
		// No class change yet: nothing may be pre-completed.
		{name: "grow 0", job: 2, growType: 0x00, want: nil},
		// Class change only: the branch terminal plus its follow-up. The
		// awakening chains stay untouched because the grow_type has no
		// awakening stage yet.
		{name: "class change branch1", job: 2, growType: 0x01, want: []int64{6926, 6927}},
		// Branch 2 selects branch 2's terminal.
		{name: "class change branch2", job: 2, growType: 0x02, want: []int64{6881, 6927}},
		// First awakening adds that branch's stage-1 chain.
		{name: "first awakening branch1", job: 2, growType: 0x11, want: []int64{6884, 6885, 6887, 6926, 6927}},
		// Branch 2 awakening never inherits branch 1's chain.
		{name: "first awakening branch2", job: 2, growType: 0x12, want: []int64{6881, 6888, 6927}},
		// Second awakening adds the stage-2 chain.
		{name: "second awakening branch1", job: 2, growType: 0x21, want: []int64{6884, 6885, 6887, 6900, 6905, 6926, 6927}},
		// Another job never inherits gunner profession quests.
		{name: "other job", job: 3, growType: 0x01, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := catalog.ProfessionProgressionQuestIDs(test.job, test.growType)
			if len(got) == 0 && len(test.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ProfessionProgressionQuestIDs(%d, 0x%02x) = %v, want %v", test.job, test.growType, got, test.want)
			}
		})
	}
}

// The main-story prerequisite of the first-awakening chain is PVF evidence for
// the chain shape, but the override must never claim it was completed.
func TestProfessionProgressionQuestIDsNeverClaimsStoryPrerequisites(t *testing.T) {
	catalog := loadProfessionOverrideTestCatalog(t, professionOverrideTestSource())
	got := catalog.ProfessionProgressionQuestIDs(2, 0x21)
	for _, id := range got {
		if id == 3356 {
			t.Fatalf("story quest 3356 must not be part of the override set: %v", got)
		}
	}
}

// Completed rows must actually silence the PVF-eligible profession quests: the
// acceptable-quest list (current EXE op21) drives the task manual and NPC
// offers, so a lingering row means the operator still sees "转职/觉醒" quests.
func TestProfessionProgressionQuestIDsSilenceProfessionQuestOffers(t *testing.T) {
	catalog := loadProfessionOverrideTestCatalog(t, professionOverrideTestSource())
	record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{}}
	for _, id := range catalog.ProfessionProgressionQuestIDs(2, 0x21) {
		record.States[id] = dnfrepo.QuestState{Status: "completed"}
	}
	eligibility := CharacterEligibility{Level: 90, Job: 2, GrowType: 0x21}
	for _, id := range catalog.QuestList(eligibility, record).IDs {
		definition, ok := catalog.Find(int64(id))
		if !ok {
			continue
		}
		if definition.JobChangeQuest >= 1 && definition.JobChangeQuest <= 3 {
			t.Fatalf("profession quest %d still offered after the override: %+v", id, definition)
		}
	}

	// Without the override the same state still offers the class-change quests,
	// which is the reported bug.
	before := catalog.QuestList(eligibility, dnfrepo.QuestRecord{})
	if len(before.IDs) == 0 {
		t.Fatal("control expectation failed: a grow_type 0x21 character with no quest rows must still be offered the class-change quests")
	}
}

// Real-PVF evidence for the reported case: a gun blader (job 15) forced to
// second awakening branch 1 must own exactly these Script.pvf profession
// quests, and no profession quest may remain acceptable afterwards.
func TestRealScriptPVFProfessionProgressionQuestIDs(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify profession progression quests")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}

	want := []int64{6878, 6884, 6885, 6886, 6887, 6900, 6901, 6902, 6903, 6904, 6905, 6926, 6927}
	got := catalog.ProfessionProgressionQuestIDs(15, 0x21)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("job 15 grow 0x21 override set = %v, want %v", got, want)
	}
	record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{}}
	for _, id := range got {
		record.States[id] = dnfrepo.QuestState{Status: "completed"}
	}
	offered := make([]int32, 0)
	for _, id := range catalog.QuestList(CharacterEligibility{Level: 90, Job: 15, GrowType: 0x21}, record).IDs {
		if definition, ok := catalog.Find(int64(id)); ok && definition.JobChangeQuest >= 1 && definition.JobChangeQuest <= 3 {
			offered = append(offered, id)
		}
	}
	if len(offered) != 0 {
		t.Fatalf("second-awakened gun blader still offered profession quests %v", offered)
	}
	// A first-awakened character keeps exactly the second-awakening entry quest
	// available; that is real progression, not a leftover.
	firstAwakened := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{}}
	for _, id := range catalog.ProfessionProgressionQuestIDs(15, 0x11) {
		firstAwakened.States[id] = dnfrepo.QuestState{Status: "completed"}
	}
	stillOpen := make([]int32, 0)
	for _, id := range catalog.QuestList(CharacterEligibility{Level: 90, Job: 15, GrowType: 0x11}, firstAwakened).IDs {
		if definition, ok := catalog.Find(int64(id)); ok && definition.JobChangeQuest >= 1 && definition.JobChangeQuest <= 3 {
			stillOpen = append(stillOpen, id)
		}
	}
	if !reflect.DeepEqual(stillOpen, []int32{6900}) {
		t.Fatalf("first-awakened gun blader open profession quests = %v, want [6900]", stillOpen)
	}
}
