package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDungeonDropTablesPreloadKeepsCancellationAndMissingTableBehavior(t *testing.T) {
	probe := &dungeonGenericPoolReadProbe{source: genericDropPreloadTestSource()}
	monsters, err := newPVFDungeonMonsterCatalog(probe)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{dungeonMonsterTable: monsters}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reads := probe.reads
	if err := s.preloadDungeonMonsterDropTables(ctx); !errors.Is(err, context.Canceled) || probe.reads != reads {
		t.Fatal("取消仍读取静态表")
	}
	if err := s.preloadDungeonMonsterDropTables(context.Background()); err != nil {
		t.Fatal("缺失可选表阻断启动", err)
	}
	reads = probe.reads
	if err := s.preloadDungeonMonsterDropTables(context.Background()); err != nil || probe.reads != reads {
		t.Fatal("重复预载绕过原缓存", err)
	}
	if _, err := currentDungeonMonsterDropRules(monsters); err == nil {
		t.Fatal("缺失规则表被制造为有效概率")
	}
	if _, err := currentDungeonGoldReferences(monsters); err == nil {
		t.Fatal("缺失金币表被制造为奖励")
	}
}

func TestRealPVFDungeonDropTablesPreloadKeepsCachesAndContent(t *testing.T) {
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
	readTables := func(c *pvfDungeonMonsterCatalog) []any {
		rules, err := currentDungeonMonsterDropRules(c)
		if err != nil {
			t.Fatal(err)
		}
		gold, err := currentDungeonGoldReferences(c)
		if err != nil {
			t.Fatal(err)
		}
		special, err := currentDungeonSpecialDrops(c)
		if err != nil {
			t.Fatal(err)
		}
		dimension, err := currentDungeonDimensionDrops(c)
		if err != nil {
			t.Fatal(err)
		}
		return []any{rules.RawTables(), gold, special, dimension}
	}
	a.ReleaseReadCaches()
	started := time.Now()
	want := readTables(cold)
	elapsed := time.Since(started)
	a.ReleaseReadCaches()
	s := &Service{dungeonMonsterTable: warm}
	if err := s.preloadDungeonMonsterDropTables(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readTables(warm); !reflect.DeepEqual(want, got) {
		t.Fatal("预载改变现用PVF规则、金币或特殊掉落内容")
	}
	a.ReleaseReadCaches()
	reads := probe.reads
	for i := 0; i < 64; i++ {
		if err := s.preloadDungeonMonsterDropTables(context.Background()); err != nil {
			t.Fatal(err)
		}
		readTables(warm)
	}
	if probe.reads != reads {
		t.Fatalf("释放PVF文本缓存后又扫描脚本：%d→%d", reads, probe.reads)
	}
	t.Logf("现用PVF四类静态表首次合计加载%s；预载并释放解码缓存后64轮读取不新增脚本访问，表内容与原加载一致", elapsed)
}
