package worldmap

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestStreamingMapCachePreservesAliasesImportsAndActions(t *testing.T) {
	source := textSource{DefaultMapList: "1 `a.map`\n1 `b.map`\n2 `a.map`\n",
		"map/a.map": "[map name]\n`A`\n[basic action]\n`a.act`\n[monster]\n1 1 0 10 20 0 0 0 `[fixed]` `[normal]`\n[import script]\n`common.map`\n",
		"map/b.map": "[map name]\n`B`\n", "map/common.map": "[npc]\n22 `[right]` 187 200 0\n"}
	opts := Options{SkipAreas: true, SkipDungeons: true}
	base, err := LoadSource(context.Background(), source, opts)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultMapCacheOptions()
	options.Capacity = 1
	options.HeavySections = 1
	streamed, err := LoadSourceCached(context.Background(), source, opts, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base.Snapshot(), streamed.Snapshot()) || !reflect.DeepEqual(base.mapByID, streamed.mapByID) || !reflect.DeepEqual(base.mapByPath, streamed.mapByPath) {
		t.Fatal("逐张加载改变了计数或重复ID/路径规则")
	}
	for i := range base.maps {
		a, e := base.readMap(i)
		if e != nil {
			t.Fatal(e)
		}
		b, e := streamed.readMap(i)
		if e != nil || !reflect.DeepEqual(a, b) {
			t.Fatalf("位置%d的完整定义不同: %v", i, e)
		}
		if len(streamed.maps[i].SourceSections) != 0 {
			t.Fatal("普通索引仍持有完整源段")
		}
	}
	if !reflect.DeepEqual(base.MapActionReferences(), streamed.MapActionReferences()) {
		t.Fatal("教学动作投影不一致")
	}
	refs := streamed.MapActionReferences()
	if len(refs) == 0 {
		t.Fatal("动作样本未生效")
	}
	refs[0].Monsters[0].MonsterID = 999
	if streamed.MapActionReferences()[0].Monsters[0].MonsterID == 999 {
		t.Fatal("投影副本不独立")
	}
	if streamed.MapCacheSnapshot().Pinned == 0 {
		t.Fatal("复杂地图未按原评分保留")
	}
}

func TestStreamingMapCacheStillValidatesEveryMapAndContext(t *testing.T) {
	source := textSource{DefaultMapList: "1 `a.map`\n2 `missing.map`\n", "map/a.map": "[map name]\n`A`\n"}
	if table, err := LoadSourceCached(context.Background(), source, Options{SkipAreas: true, SkipDungeons: true}, DefaultMapCacheOptions()); err == nil || table != nil {
		t.Fatal("缺失地图被按需加载掩盖")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadSourceCached(ctx, source, Options{SkipAreas: true, SkipDungeons: true}, DefaultMapCacheOptions()); !errors.Is(err, context.Canceled) {
		t.Fatal("取消被吞掉", err)
	}
}
