package darkknightcombo

import (
	"context"
	"testing"
	"time"

	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestBuildSettingsRecordRoundTripsBothIndependentPages(t *testing.T) {
	pages := []Page{
		{Index: 1, Roots: []Root{{SkillID: 119, Children: []uint16{8}}}},
		{Index: 0, Roots: []Root{{SkillID: 118, Children: []uint16{46}}}},
	}
	record, err := BuildSettingsRecord("77", pages, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if record.Scope != SettingsScope("77") {
		t.Fatalf("scope=%q want=%q", record.Scope, SettingsScope("77"))
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Settings.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found {
		t.Fatalf("load found=%t err=%v", found, err)
	}
	if len(loaded) != MaximumPageCount || loaded[0].Index != 0 || loaded[1].Index != 1 {
		t.Fatalf("loaded pages=%+v", loaded)
	}
	if loaded[0].Roots[0].Children[0] != 46 || loaded[1].Roots[0].Children[0] != 8 {
		t.Fatalf("loaded pages changed=%+v", loaded)
	}
}

func TestBuildSettingsRecordRejectsDuplicatePages(t *testing.T) {
	_, err := BuildSettingsRecord("77", []Page{{Index: 0}, {Index: 0}}, time.Time{})
	if err == nil {
		t.Fatal("duplicate page indexes were accepted")
	}
}
