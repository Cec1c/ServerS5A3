package dnfbridge

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestDungeonDropPreloadIncludesAbyssIndexWithoutChangingCandidates(t *testing.T) {
	t.Setenv(abyssItemLevelFloorEnv, "")
	t.Setenv(abyssItemLevelCeilingEnv, "")
	source := bridgeAbyssEpicFragmentPVF()
	cold, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	band := abyssItemLevelBandFromEnv().forDungeon(87, 90)
	pieces, err := currentEpicPieceCatalogFor(cold)
	if err != nil {
		t.Fatal(err)
	}
	want, wantIndex, err := currentAbyssEpicPieceLevelCandidates(cold, pieces, band)
	if err != nil || len(want) == 0 {
		t.Fatalf("原候选不可用：%v", err)
	}
	probe := &dungeonGenericPoolReadProbe{source: source}
	warm, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{dungeonMonsterTable: warm}
	if err := s.preloadDungeonMonsterDropTables(context.Background()); err != nil {
		t.Fatal(err)
	}
	reads := probe.reads
	for i := 0; i < 4; i++ {
		pieces, err := currentEpicPieceCatalogFor(warm)
		if err != nil {
			t.Fatal(err)
		}
		got, index, err := currentAbyssEpicPieceLevelCandidates(warm, pieces, band)
		if err != nil || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(wantIndex, index) || probe.reads != reads {
			t.Fatalf("首次碎片候选仍读文件或改变候选顺序、等级：reads=%d/%d err=%v", probe.reads, reads, err)
		}
	}
}

func TestDungeonDropPreloadKeepsAbyssLegacyWithoutIndexReads(t *testing.T) {
	t.Setenv(abyssItemLevelFloorEnv, "legacy")
	probe := &dungeonGenericPoolReadProbe{source: bridgeAbyssEpicFragmentPVF()}
	catalog, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Service{dungeonMonsterTable: catalog}).preloadDungeonMonsterDropTables(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := currentAbyssEpicPieceLevelIndexes.Load(catalog); found {
		t.Fatal("legacy档提前创建了不会使用的等级索引")
	}
}

func TestRealPVFDungeonDropPreloadRemovesAbyssIndexColdRead(t *testing.T) {
	t.Setenv(abyssItemLevelFloorEnv, "")
	t.Setenv(abyssItemLevelCeilingEnv, "")
	a := real90DropArchive(t)
	probe := &dungeonGenericPoolReadProbe{source: a}
	cold, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	a.ReleaseReadCaches()
	start := time.Now()
	wantIndex, err := currentAbyssEpicPieceLevelIndexFor(cold)
	if err != nil || wantIndex.Resolved == 0 {
		t.Fatalf("现用PVF原索引不可用：%v", err)
	}
	coldElapsed := time.Since(start)
	a.ReleaseReadCaches()
	start = time.Now()
	if err := (&Service{dungeonMonsterTable: warm}).preloadDungeonMonsterDropTables(context.Background()); err != nil {
		t.Fatal(err)
	}
	preloadElapsed := time.Since(start)
	a.ReleaseReadCaches()
	reads := probe.reads
	start = time.Now()
	for i := 0; i < 64; i++ {
		got, err := currentAbyssEpicPieceLevelIndexFor(warm)
		if err != nil || !reflect.DeepEqual(wantIndex, got) || probe.reads != reads {
			t.Fatalf("现用PVF预载索引与原索引不等价或仍有脚本访问：%d/%d %v", probe.reads, reads, err)
		}
	}
	t.Logf("现用PVF碎片等级索引首次构建%s；启动静态预载%s；释放文本缓存后64次复用%s、脚本新增读取0，解析等级数%d", coldElapsed, preloadElapsed, time.Since(start), wantIndex.Resolved)
}
