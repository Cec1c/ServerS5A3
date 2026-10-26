package pvf

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestReleaseReadCachesPreservesArchiveAndPreviouslyReturnedValues(t *testing.T) {
	archive, err := OpenBytes(buildSyntheticNKPIArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := archive.ReadText("dir/test.ani")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := archive.ReadRaw("dir/test.ani")
	if err != nil {
		t.Fatal(err)
	}
	wantRaw := append([]byte(nil), raw...)
	before := archive.Snapshot()
	file, _ := archive.FindFile("dir/test.ani")
	if before.CachedTexts == 0 || before.CachedChunks == 0 {
		t.Fatal("测试未实际填充解码缓存")
	}
	archive.ReleaseReadCaches()
	cleared := archive.Snapshot()
	if cleared.CachedTexts != 0 || cleared.CachedChunks != 0 || cleared.Checksum != before.Checksum || cleared.FileCount != before.FileCount {
		t.Fatalf("清理结果=%+v", cleared)
	}
	if got, _ := archive.FindFile("DIR/TEST.ANI"); got != file || !bytes.Equal(raw, wantRaw) {
		t.Fatal("文件索引或已返回的数据被修改")
	}
	for i := 0; i < 3; i++ {
		got, err := archive.ReadText("dir/test.ani")
		if err != nil || got != want {
			t.Fatalf("重读文本不一致: %v", err)
		}
		gotRaw, err := archive.ReadRaw("dir/test.ani")
		if err != nil || !bytes.Equal(gotRaw, wantRaw) {
			t.Fatalf("重读数据不一致: %v", err)
		}
		archive.ReleaseReadCaches()
	}
}

func TestReleaseReadCachesConcurrentReadersRemainConsistent(t *testing.T) {
	archive, err := OpenBytes(buildSyntheticNKPIArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := archive.ReadText("dir/test.ani")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				got, err := archive.ReadText("dir/test.ani")
				if err != nil || got != want {
					errs <- fmt.Errorf("并发重读不一致: %v", err)
					return
				}
				archive.ReleaseReadCaches()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	archive.ReleaseReadCaches()
	if snapshot := archive.Snapshot(); snapshot.CachedTexts != 0 || snapshot.CachedChunks != 0 {
		t.Fatalf("最终缓存未释放: %+v", snapshot)
	}
	var empty *Archive
	empty.ReleaseReadCaches()
}
