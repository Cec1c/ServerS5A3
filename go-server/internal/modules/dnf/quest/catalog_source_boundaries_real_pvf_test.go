package quest

import (
	"errors"
	"path"
	"reflect"
	"slices"
	"sort"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The pinned 369d072b PVF lists two distinct class-change families. Audit the
// list entries and source sections before checking the parsed catalog, so a
// parser/counting regression cannot justify changing the expected totals.
func TestRealScriptPVFClassChangeFamiliesMatchSourceDocuments(t *testing.T) {
	catalog, index := loadRealScriptPVFCatalogForClientList(t)
	type counts struct{ quests, growRewards int }
	want := map[string]counts{
		"n_quest/job": {72, 57},
		"n_quest/quest_renewal/epic_quest/elvengard": {72, 57},
	}
	got := make(map[string]counts)
	ids, paths := make(map[int64]bool), make(map[string]bool)
	for _, entry := range index.List(DefaultList) {
		doc, ok := index.Document(entry.Path)
		if !ok {
			t.Fatalf("missing list-backed source %d %s", entry.ID, entry.Path)
		}
		if !reflect.DeepEqual(doc.Ints("job change quest"), []int64{1}) {
			continue
		}
		family := path.Dir(entry.Path)
		if _, ok := want[family]; !ok || ids[entry.ID] || paths[entry.Path] {
			t.Fatalf("unknown/duplicate class-change source id=%d path=%s family=%s", entry.ID, entry.Path, family)
		}
		ids[entry.ID], paths[entry.Path] = true, true
		d, ok := catalog.Find(entry.ID)
		reward, _ := doc.Text("reward type")
		if !ok || d.Path != entry.Path || d.JobChangeQuest != 1 || d.RewardType != reward ||
			!slices.Equal(d.RewardIntData, doc.Ints("reward int data")) {
			t.Fatalf("catalog differs from class-change source id=%d path=%s definition=%+v", entry.ID, entry.Path, d)
		}
		count := got[family]
		count.quests++
		if normalizeQuestTag(reward) == "grow type" {
			count.growRewards++
		}
		got[family] = count
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source class-change families=%v want=%v", got, want)
	}
	for _, d := range catalog.ordered {
		if d.JobChangeQuest == 1 && !ids[d.ID] {
			t.Fatalf("catalog invented class-change row %d %s", d.ID, d.Path)
		}
	}
	t.Logf("distinct source class-change families (quests/grow rewards)=%v", got)
}

// These fixtures contain no active rows, so the public per-quest Acceptable
// projection gives the candidates before QuestList's client-node closure.
// Check identities as well as the diagnostic count: a different rejection
// cannot silently replace one of the expected rows.
func assertRealPVFPreflightExcludedIDs(t *testing.T, catalog *Catalog, eligibility CharacterEligibility, record dnfrepo.QuestRecord, want []int32) {
	t.Helper()
	_, active := questStateSets(record)
	if len(active) != 0 {
		t.Fatal("excluded-ID fixture requires no active quests")
	}
	list := catalog.QuestList(eligibility, record)
	var got []int32
	for _, id := range catalog.Acceptable(eligibility, record).IDs {
		if !containsQuestID(list.IDs, id) {
			got = append(got, id)
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if !reflect.DeepEqual(got, want) || list.PreflightExcluded != len(want) {
		t.Fatalf("preflight exclusions=%v count=%d want=%v", got, list.PreflightExcluded, want)
	}
}

func assertHiddenParentOutsideCouponSweep(t *testing.T, catalog *Catalog, record dnfrepo.QuestRecord) {
	t.Helper()
	parent, ok := catalog.Find(12074)
	if !ok || parent.Path != "n_quest/new_quest/gent/revolt_se/gent_revolt_se_04.qst" ||
		parent.Grade != "[hidden]" || parent.Type != "[clear multi quest]" || parent.LevelMin != 90 ||
		!reflect.DeepEqual(parent.IntData, []int64{12075, 12076, 12077, 12078}) {
		t.Fatalf("hidden parent PVF boundary changed: %+v", parent)
	}
	if decision, err := catalog.DecideAccept(farewellMomentEligibility(), record, parent.ID); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("hidden parent decision=%v err=%v", decision, err)
	}
	for _, id := range append([]int64{parent.ID}, parent.IntData...) {
		if _, ok := record.States[id]; ok {
			t.Fatalf("coupon manufactured a state for hidden family quest %d", id)
		}
		if _, ok := record.Progress[id]; ok {
			t.Fatalf("coupon manufactured progress for hidden family quest %d", id)
		}
		if id != parent.ID {
			child, ok := catalog.Find(id)
			if !ok || child.Grade != "[sub]" || child.MainQuestID != parent.ID {
				t.Fatalf("hidden-family child %d changed: %+v", id, child)
			}
		}
	}
}
