package tagtournament

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func layoutSource(t *testing.T) testSource {
	t.Helper()
	s := catalogSource(t)
	for mode, id := range []int{300, 301} {
		count, first := 5, 11
		if mode == 1 {
			count, first = 7, 31
		}
		start := 700 + mode
		s["map/map.lst"] += fmt.Sprintf("%d `%d.pvf`\n", start, start)
		s[fmt.Sprintf("map/%d.pvf", start)] = fmt.Sprintf("[dungeon]\n%d\n[type]\n`[start]`\n", id)
		var specs strings.Builder
		fmt.Fprintf(&specs, "`map` 0 0 %d ", start)
		for i := 0; i < count; i++ {
			kind := "map"
			if i == count-1 {
				kind = "boss"
			}
			mapID := first + i
			if mode == 0 && i == 4 {
				mapID = 16
			}
			fmt.Fprintf(&specs, "`%s` %d 0 %d ", kind, i+1, mapID)
		}
		s[fmt.Sprintf("dungeon/%d.pvf", id)] += fmt.Sprintf("[maze info]\n[size]\n%d 1\n[greed]\n`BB%sEE`\n[map specification]\n%s\n[/map specification]\n[start map]\n0 0\n[/start map]\n[boss map]\n%d 0\n[/boss map]\n", count+1, strings.Repeat("FF", count-1), specs.String(), count)
	}
	return s
}

func TestDailyLayoutUsesWholeScheduledMapsAndPreservesSharedDefinitions(t *testing.T) {
	source := layoutSource(t)
	c, err := LoadCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), source, worldmap.Options{SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, beijing)
	for _, mode := range []Mode{Normal, Hard} {
		plan, _ := c.DailyPlan(now, mode)
		original, err := worldmap.BuildDungeonTopology(resolver, int64(plan.DungeonID), 0)
		if err != nil {
			t.Fatal(err)
		}
		before := original.Rooms()
		layout, err := c.BuildLayout(table, resolver, now, plan)
		if err != nil {
			t.Fatal(err)
		}
		if len(layout.Rounds) != len(plan.Encounters) {
			t.Fatal(layout.Rounds)
		}
		for i, row := range layout.Rounds {
			room, _ := layout.Topology.Room(row.Coordinate)
			m, found := table.FindMap(int64(plan.Encounters[i].MapID))
			if !found {
				t.Fatal("每日地图未解析")
			}
			if row.Encounter != plan.Encounters[i] || !reflect.DeepEqual(room.Map.Map, m) || row.Coordinate.X != int64(i+1) {
				t.Fatal("名单与回合地图不符", row)
			}
		}
		after, err := worldmap.BuildDungeonTopology(resolver, int64(plan.DungeonID), 0)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after.Rooms()) {
			t.Fatal("每日排列改写共享PVF模板")
		}
		for _, change := range []func(*DailyPlan){func(p *DailyPlan) { p.Encounters[0].MonsterID++ }, func(p *DailyPlan) { p.Encounters[0].Balance++ }, func(p *DailyPlan) { p.ServiceDay-- }, func(p *DailyPlan) { p.ConfigHash = "旧配置" }} {
			bad, _ := c.DailyPlan(now, mode)
			change(&bad)
			if result, err := c.BuildLayout(table, resolver, now, bad); err == nil || result != nil {
				t.Fatal("接受被修改的日计划")
			}
		}
	}
}

func TestDailyLayoutRetainsMechanicSpawnsAndRejectsWrongBoss(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		source := layoutSource(t)
		// 部分真实地图包含多个机制怪，整个房间必须保留，不能强求一房一怪。
		for _, pool := range []MapPool{candidateCatalog(t).config.NormalPool, candidateCatalog(t).config.SpecialPool, candidateCatalog(t).config.HardPool} {
			for _, row := range pool.Rows {
				name := fmt.Sprintf("map/%d.pvf", row.MapID)
				source[name] += "[monster]\n999 1 0 200 100 0 1 1 `[fixed]` `[normal]`\n[/monster]\n"
			}
		}
		c, err := LoadCatalog(source)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 10, 19, 12, 0, 0, 0, beijing)
		plan, _ := c.DailyPlan(now, Normal)
		if wrong {
			name := fmt.Sprintf("map/%d.pvf", plan.Encounters[0].MapID)
			source[name] = strings.ReplaceAll(source[name], fmt.Sprint(plan.Encounters[0].MonsterID), "888")
		}
		table, err := worldmap.LoadSource(context.Background(), source, worldmap.Options{SkipAreas: true})
		if err != nil {
			t.Fatal(err)
		}
		resolver, err := worldmap.NewResolver(table)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := c.BuildLayout(table, resolver, now, plan)
		if wrong {
			if err == nil || layout != nil {
				t.Fatal("接受错怪地图")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		room, _ := layout.Topology.Room(layout.Rounds[0].Coordinate)
		if len(room.Map.Map.Monsters) != 2 {
			t.Fatal("地图机制对象被丢弃")
		}
	}
}

func TestDailyLayoutRejectsConnectedVerticalChainWhoseXCannotDriveNativeRounds(t *testing.T) {
	source := layoutSource(t)
	text := source["dungeon/300.pvf"]
	text = text[:strings.Index(text, "[maze info]")]
	text += "[maze info]\n[size]\n1 6\n[greed]\nBB\nFF\nFF\nFF\nFF\nEE\n[map specification]\n`map` 0 0 700 `map` 0 1 11 `map` 0 2 12 `map` 0 3 13 `map` 0 4 14 `boss` 0 5 16\n[/map specification]\n[start map]\n0 0\n[/start map]\n[boss map]\n0 5\n[/boss map]\n"
	source["dungeon/300.pvf"] = text
	c, err := LoadCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), source, worldmap.Options{SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := worldmap.BuildDungeonTopology(resolver, 300, 0)
	if err != nil || len(topology.Rooms()) != 6 {
		t.Fatal("负例必须仍是有效连接地图", err)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, beijing)
	plan, _ := c.DailyPlan(now, Normal)
	if layout, err := c.BuildLayout(table, resolver, now, plan); err == nil || layout != nil {
		t.Fatal("竖直链会把每回合都投影成X=0")
	}
}
