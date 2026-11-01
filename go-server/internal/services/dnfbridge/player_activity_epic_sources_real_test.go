package dnfbridge

import (
	"context"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 核对当前资源的编号与路径；不把资源分类验收当作真实副本战斗验收。
func TestRealPVFPlayerEpicSourceDungeonClassification(t *testing.T) {
	a := real90DropArchive(t)
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id           int64
		path, source string
	}{
		{316, "dungeon/MetroCenter/Hell/Hell.dgn", "demon_rift"},
		{7539, "dungeon/InfernoMode/Inferno_MetroCenter1.dgn", "time_rift"},
		{6062, "dungeon/Event/2018_EpciRoad2/forestofsilent.dgn", "epic_road"},
		{6063, "dungeon/Event/2018_EpciRoad2/forestofdesire.dgn", "epic_road"},
		{6064, "dungeon/Event/2018_EpciRoad2/forestofcorruption.dgn", "epic_road"},
		{6065, "dungeon/Event/2018_EpciRoad2/forestofunrevert.dgn", "epic_road"},
	} {
		d, ok := table.FindDungeon(tc.id)
		if !ok || !strings.EqualFold(d.Path, tc.path) || playerActivityEpicSourceKey(tc.id) != tc.source {
			t.Fatalf("史诗来源资源不匹配：编号=%d 路径=%q 来源=%q", tc.id, d.Path, playerActivityEpicSourceKey(tc.id))
		}
		t.Logf("史诗来源：编号=%d 路径=%s 分类=%s", tc.id, d.Path, tc.source)
	}
}
