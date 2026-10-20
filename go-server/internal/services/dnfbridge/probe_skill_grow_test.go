package dnfbridge

import (
	"context"
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestProbePriestSkillGrowTypes 检查圣职者(job 14)技能 11/16 的运行时 PVF 规则：
// 线上 op29 对 skill=16 的拒绝原因是 "skill grow type rule rejected mutation:
// skill=16 grow=0"，需要确认 PVF 到底声明了什么。
func TestProbePriestSkillGrowTypes(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to inspect the runtime PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []byte{14, 4, 5, 15} {
		skills := catalog.SkillsForJob(job)
		if len(skills) == 0 {
			continue
		}
		t.Logf("job=%d skills=%d", job, len(skills))
		for _, definition := range skills {
			if definition.ID > 20 && definition.RequiredLevel > 5 {
				continue
			}
			t.Logf("  job=%d id=%d name=%q required=%d max=%d class=%d grow=%v second=%v path=%q",
				job, definition.ID, definition.Name, definition.RequiredLevel, definition.MaximumLevel,
				definition.SkillClass, definition.GrowTypes, definition.SecondGrowTypes, definition.Path)
		}
	}
	for _, target := range []struct {
		job byte
		id  uint16
	}{{14, 16}, {14, 11}, {14, 1}, {14, 2}} {
		definition, ok := catalog.Find(target.job, target.id)
		if !ok {
			t.Logf("job=%d id=%d NOT FOUND in catalog", target.job, target.id)
			continue
		}
		text, readErr := archive.ReadText(definition.Path)
		if readErr != nil {
			t.Logf("job=%d id=%d path=%s read error: %v", target.job, target.id, definition.Path, readErr)
			continue
		}
		t.Logf("job=%d id=%d name=%q grow=%v second=%v required=%d",
			target.job, target.id, definition.Name, definition.GrowTypes, definition.SecondGrowTypes, definition.RequiredLevel)
		for _, line := range strings.Split(text, "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "growtype") || strings.Contains(lower, "required level") ||
				strings.Contains(lower, "skill class") || strings.Contains(lower, "job") {
				t.Logf("    id=%d raw: %s", target.id, strings.TrimSpace(line))
			}
		}
	}
}
