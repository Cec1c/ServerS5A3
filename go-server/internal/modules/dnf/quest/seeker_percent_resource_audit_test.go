package quest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// This audits the source fields separately from settlement policy. Optional
// output is local evidence, not a replay of a character's actual quest history.
func TestSeekerPercentResourceAudit(t *testing.T) {
	path := os.Getenv("SEEKER_PERCENT_AUDIT_PVF")
	if path == "" {
		t.Skip("set SEEKER_PERCENT_AUDIT_PVF to the approved archive")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Snapshot().Checksum != "9dcd31a3e6001fd5563d36dc65cbaa96bed8b782700cba30625197e27cf1e43c" {
		t.Fatal("archive differs from reviewed input")
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	excluded := map[string][]int64{}
	var eligible []int64
	texts := map[string]string{}
	for _, p := range []string{DefaultList, progression.ExperienceTablePath, progression.SkillPointTablePath, progression.QuestParameterPath, dnfhonor.TablePath} {
		texts[p], err = archive.ReadText(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range catalog.ordered {
		texts[d.Path], err = archive.ReadText(d.Path)
		if err != nil {
			t.Fatal(err)
		}
		if normalizeQuestTag(d.Grade) != "epic" {
			continue
		}
		counts["epic"]++
		doc, found := index.Document(d.Path)
		if !found {
			t.Fatal(d.Path)
		}
		levels := doc.Ints("level")
		if len(levels) == 0 || int(levels[0]) != d.LevelMin || d.LevelMin <= 0 {
			t.Fatalf("epic source level mismatch: %+v raw=%v", d, levels)
		}
		reason := "eligible"
		switch {
		case d.MainQuestID > 0:
			reason = "child"
		case d.JobChangeQuest != 0:
			reason = "profession"
		case d.IgnoreQuestLevel4Exp:
			reason = "ignore-level"
		case d.NoExperience:
			reason = "no-experience"
		}
		counts[reason]++
		if reason == "eligible" {
			eligible = append(eligible, d.ID)
		} else {
			excluded[reason] = append(excluded[reason], d.ID)
		}
	}
	span := map[int]uint32{}
	for level := 1; level <= tables.Snapshot().ExperienceThresholds; level++ {
		upper, e := tables.ThresholdToNext(level)
		if e != nil {
			t.Fatal(e)
		}
		var lower uint32
		if level > 1 {
			lower, e = tables.ThresholdToNext(level - 1)
			if e != nil {
				t.Fatal(e)
			}
		}
		if upper < lower {
			t.Fatalf("descending span at %d", level)
		}
		span[level] = upper - lower
	}
	for level, want := range map[int]uint32{1: 150, 3: 192, 15: 14556, 35: 235974, 65: 2440828, 88: 32029581, 89: 42444667} {
		if got := uint32(uint64(span[level]) * 15 / 100); got != want {
			t.Fatalf("level %d got %d want %d", level, got, want)
		}
	}
	if counts["eligible"] != 639 || counts["profession"] != 129 || counts["ignore-level"] != 6 {
		t.Fatalf("reviewed policy scope changed: %v", counts)
	}
	for _, id := range eligible {
		d := catalog.byID[id]
		got, err := finishMainQuestExperiencePercent(tables, d, 15)
		want := uint32(uint64(span[d.LevelMin]) * 15 / 100)
		if err != nil || got != want {
			t.Fatalf("quest=%d got=%d want=%d err=%v", id, got, want, err)
		}
	}
	if out := os.Getenv("SEEKER_PERCENT_AUDIT_OUTPUT"); out != "" {
		if err := os.MkdirAll(out, 0755); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]any{"catalog.json": catalog.ordered, "sources.json": texts, "audit.json": map[string]any{"checksum": archive.Snapshot().Checksum, "counts": counts, "eligible": eligible, "excluded": excluded, "spans": span}} {
			body, err := json.MarshalIndent(data, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, name), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("definitions=%d, counts=%v", len(catalog.ordered), counts)
}
