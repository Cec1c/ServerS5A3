package worldmap

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func cacheFixture(t *testing.T, capacity int) (*Table, textSource) {
	t.Helper()
	source := textSource{DefaultMapList: "1 `a.map`\n2 `b.map`\n3 `c.map`\n", "map/a.map": "[map name]\n`A`\n[import script]\n`common.map`\n[monster]\n1 1 0 10 20 0 0 0 `[fixed]` `[normal]`\n", "map/common.map": "[npc]\n22 `[right]` 187 200 0\n", "map/b.map": "[map name]\n`B`\n", "map/c.map": "[map name]\n`C`\n"}
	table, err := LoadSource(context.Background(), source, Options{SkipAreas: true, SkipDungeons: true})
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultMapCacheOptions()
	options.Capacity = capacity
	options.PinnedBytes = 0
	if err = table.EnableMapCache(source, options); err != nil {
		t.Fatal(err)
	}
	return table, source
}

func TestMapCacheEvictionPreservesImportsAndReturnedIsolation(t *testing.T) {
	table, _ := cacheFixture(t, 1)
	first, ok := table.FindMap(1)
	if !ok || len(first.NPCs) != 1 {
		t.Fatal("按需导入失败", first)
	}
	expected := cloneMap(first)
	first.SourceSections[0].Name = "调用方改动"
	first.Monsters[0].MonsterID = 999
	if _, ok = table.FindMap(2); !ok {
		t.Fatal("第二张图未读取")
	}
	restored, ok := table.FindMap(1)
	if !ok || !reflect.DeepEqual(expected, restored) {
		t.Fatal("淘汰重读或副本隔离改变地图")
	}
	snap := table.MapCacheSnapshot()
	if snap.Entries != 1 || snap.Loads != 3 || snap.Evictions != 2 || snap.EstimatedBytes > table.mapCache.options.MaxBytes {
		t.Fatal("缓存没有按预算淘汰", snap)
	}
	byPath, ok := table.FindMapPath("MAP/A.MAP")
	if !ok || !reflect.DeepEqual(restored, byPath) {
		t.Fatal("路径出口不一致")
	}
}

func TestMapCacheReadFailureDoesNotCacheAnIncompleteMap(t *testing.T) {
	table, source := cacheFixture(t, 1)
	saved := source["map/common.map"]
	delete(source, "map/common.map")
	_, err := table.readMap(0)
	if !errors.Is(err, ErrMapDefinitionRead) {
		t.Fatal("导入错误被吞掉", err)
	}
	if _, ok := table.FindMap(1); ok {
		t.Fatal("读取失败返回了元数据假图")
	}
	if _, found, e := table.FindMapWithError(1); found || !errors.Is(e, ErrMapDefinitionRead) {
		t.Fatal("显式查询没有保留错误", found, e)
	}
	if values, e := table.MapsWithError(); values != nil || !errors.Is(e, ErrMapDefinitionRead) {
		t.Fatal("全量查询静默返回了不完整集合", e)
	}
	if table.MapCacheSnapshot().Entries != 0 {
		t.Fatal("读取失败污染缓存")
	}
	source["map/common.map"] = saved
	value, ok := table.FindMap(1)
	if !ok || len(value.NPCs) != 1 {
		t.Fatal("资源恢复后不能重试")
	}
}

type blockingMapSource struct {
	source  textSource
	start   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingMapSource) ReadText(p string) (string, error) {
	s.once.Do(func() { close(s.start); <-s.release })
	return s.source.ReadText(p)
}

func TestMapCacheConcurrentColdRequestParsesOnce(t *testing.T) {
	table, source := cacheFixture(t, 2)
	slow := &blockingMapSource{source: source, start: make(chan struct{}), release: make(chan struct{})}
	table.mapCache.source = slow
	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := table.readMap(0)
			if err == nil && len(value.NPCs) != 1 {
				err = errors.New("并发地图不完整")
			}
			errCh <- err
		}()
	}
	<-slow.start
	deadline := time.Now().Add(3 * time.Second)
	for table.MapCacheSnapshot().Waits < 11 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(slow.release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	snap := table.MapCacheSnapshot()
	if snap.Loads != 1 || snap.Waits != 11 {
		t.Fatal("同图并发没有合并", snap)
	}
}

func TestMapCacheHeavyMapsAndByteBudget(t *testing.T) {
	_, source := cacheFixture(t, 1)
	for _, pin := range []bool{false, true} {
		table, err := LoadSource(context.Background(), source, Options{SkipAreas: true, SkipDungeons: true})
		if err != nil {
			t.Fatal(err)
		}
		options := DefaultMapCacheOptions()
		if pin {
			options.HeavySections = 1
		} else {
			options.PinnedBytes = 0
			options.MaxBytes = 1
		}
		if err = table.EnableMapCache(source, options); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, ok := table.FindMap(1); !ok {
				t.Fatal("预算内读取失败")
			}
		}
		got := table.MapCacheSnapshot()
		if pin {
			if got.Loads != 0 || got.PinnedHits != 3 || got.Pinned != 3 {
				t.Fatal("保留图重新解析或筛选错误", got)
			}
		} else {
			if got.Entries != 0 || got.Loads != 3 {
				t.Fatal("超预算图被常驻保存", got)
			}
		}
	}
}

func TestMapCachePreservesDuplicateIDAndPathPrecedence(t *testing.T) {
	source := textSource{DefaultMapList: "1 `a.map`\n1 `b.map`\n2 `a.map`\n", "map/a.map": "[map name]\n`A`\n", "map/b.map": "[map name]\n`B`\n"}
	table, err := LoadSource(context.Background(), source, Options{SkipAreas: true, SkipDungeons: true})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := table.FindMap(1)
	last, _ := table.FindMapPath("map/a.map")
	all := table.Maps()
	if err = table.EnableMapCache(source, DefaultMapCacheOptions()); err != nil {
		t.Fatal(err)
	}
	got, _ := table.FindMap(1)
	gotPath, _ := table.FindMapPath("map/a.map")
	if !reflect.DeepEqual(got, first) || !reflect.DeepEqual(gotPath, last) || !reflect.DeepEqual(all, table.Maps()) {
		t.Fatal("按需查询改变了ID首项或路径末项语义")
	}
}
