package pvf

import (
	"bytes"
	"container/list"
	"fmt"
	"sync"
	"testing"
)

func TestBoundedReadCacheKeepsValuesAndEnforcesPayloadBudget(t *testing.T) {
	a, err := OpenBytes(buildSyntheticNKPIArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := a.ReadText("dir/test.ani")
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, err := a.ReadRaw("dir/test.ani")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnableBoundedReadCaches(64, 64); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, e := a.ReadText("dir/test.ani")
		if e != nil || got != want {
			t.Fatalf("有界文本重读改变语义：%v", e)
		}
		gotRaw, e := a.ReadRaw("dir/test.ani")
		if e != nil || !bytes.Equal(gotRaw, wantRaw) {
			t.Fatalf("有界原文重读改变语义：%v", e)
		}
		s := a.BoundedReadCacheSnapshot()
		if s.TextBytes > s.TextBudget || s.ChunkBytes > s.ChunkBudget {
			t.Fatalf("缓存超过预算：%+v", s)
		}
	}
	if a.Snapshot().CachedChunks != 0 || a.Snapshot().CachedTexts != 0 {
		t.Fatal("超大条目不应保留在缓存")
	}
	if err := a.EnableBoundedReadCaches(1024, 1024); err == nil {
		t.Fatal("不允许运行期重复切换预算")
	}
}

func TestBoundedReadCacheConcurrentReleaseAndRead(t *testing.T) {
	a, err := OpenBytes(buildSyntheticNKPIArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := a.ReadText("dir/test.ani")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.EnableBoundedReadCaches(1024*1024, 1024*1024); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				got, e := a.ReadText("dir/test.ani")
				if e != nil || got != want {
					errs <- fmt.Errorf("并发读取不一致：%v", e)
					return
				}
				a.ReleaseReadCaches()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	a.ReleaseReadCaches()
	s := a.BoundedReadCacheSnapshot()
	if s.Texts != 0 || s.Chunks != 0 || s.TextBytes != 0 || s.ChunkBytes != 0 {
		t.Fatalf("最终清理未完成：%+v", s)
	}
}

func TestBoundedReadCacheRetainsRecentlyUsedEntry(t *testing.T) {
	c := byteReadCache{budget: 136, entries: make(map[int]*list.Element)}
	c.put(1, "first", 4)
	c.put(2, "second", 4)
	if _, ok := c.get(1); !ok {
		t.Fatal("预算内条目未保留")
	}
	c.put(3, "third", 4)
	if _, ok := c.get(2); ok {
		t.Fatal("应淘汰最久未使用的条目")
	}
	if value, ok := c.get(1); !ok || value != "first" {
		t.Fatal("最近命中的条目被淘汰")
	}
	if _, ok := c.get(3); !ok {
		t.Fatal("新条目没有保留")
	}
}
