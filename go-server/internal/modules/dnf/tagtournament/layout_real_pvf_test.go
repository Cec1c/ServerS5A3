package tagtournament_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type arenaLayoutReadSource struct {
	dnfpvf.Source
	lists map[string]string
}

func (s arenaLayoutReadSource) ReadText(name string) (string, error) {
	if v, ok := s.lists[name]; ok {
		return v, nil
	}
	return s.Source.ReadText(name)
}

func TestRealPVFTagDailyLayoutMatchesEveryRoundAndKeepsMechanicObjects(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("未提供真实PVF，只读专项未执行")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: name})
	if err != nil {
		t.Fatal(err)
	}
	c, err := tagtournament.LoadCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	// 沿用原归档索引的路径和编号，仅读取帝国的两副本及18张图。
	source := arenaLayoutReadSource{Source: archive, lists: map[string]string{}}
	for _, folder := range []string{"map", "dungeon"} {
		body, err := archive.ReadText(folder + "/" + folder + ".lst")
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse("索引", body)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for _, row := range dnfpvf.ParseList(doc) {
			if folder == "map" && row.ID >= 44901 && row.ID <= 44918 || folder == "dungeon" && (row.ID == 320 || row.ID == 336) {
				fmt.Fprintf(&out, "%d `%s`\n", row.ID, row.Path)
			}
		}
		source.lists[folder+"/tag_layout_readonly.lst"] = out.String()
	}
	table, err := worldmap.LoadSource(context.Background(), source, worldmap.Options{MapListPath: "map/tag_layout_readonly.lst", DungeonListPath: "dungeon/tag_layout_readonly.lst", SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for day := 18; day <= 24; day++ {
			now := time.Date(2026, 10, day, 12, 0, 0, 0, time.FixedZone("北京时间", 8*3600))
			plan, _ := c.DailyPlan(now, mode)
			layout, err := c.BuildLayout(table, resolver, now, plan)
			if err != nil {
				t.Fatalf("模式%d/日期%d: %v", mode, day, err)
			}
			for i, row := range layout.Rounds {
				room, _ := layout.Topology.Room(row.Coordinate)
				if room.Map.Map.ID != int64(plan.Encounters[i].MapID) || row.Encounter != plan.Encounters[i] || mode == tagtournament.Hard && row.BasisLevel != 120 {
					t.Fatal("真实回合与今日头像、难度资料不符", row)
				}
			}
		}
	}
}
