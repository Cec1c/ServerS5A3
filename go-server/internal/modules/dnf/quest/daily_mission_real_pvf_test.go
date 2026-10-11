package quest

import (
	"context"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFDailyMissionManualIncludesAcceptedExamples(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
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
	record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{4872: {Status: "completed"}, 10019: {Status: "active", ProgressValue: 1}, 2901: {Status: "active", ProgressValue: 1}}}
	list := catalog.QuestList(CharacterEligibility{Level: 90, Job: 2, GrowType: 36}, record)
	for _, id := range []int64{10019, 2901} {
		definition, found := catalog.Find(id)
		if !found || normalizeQuestTag(definition.Grade) != "daily mission" {
			t.Fatalf("task %d grade=%s", id, definition.Grade)
		}
		included := false
		for _, listed := range list.IDs {
			if int64(listed) == id {
				included = true
			}
		}
		if !included {
			t.Fatalf("accepted daily mission %d omitted from manual", id)
		}
	}
}
