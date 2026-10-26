package worldmap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func streamingPinnedPositions(t *Table) []int {
	var out []int
	for p := range t.mapCache.pinned {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func TestRealPVFLowMemoryWholeArchive(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("未指定真实PVF")
	}
	out := t.TempDir()
	a, err := platformpvf.Open(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnableBoundedReadCaches(8<<20, 8<<20); err != nil {
		t.Fatal(err)
	}
	base, err := LoadSource(context.Background(), a, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(base)
	if err != nil {
		t.Fatal(err)
	}
	wantRoutes := mapCacheRouteGoldens(resolver)
	wantSnapshot := base.Snapshot()
	wantByID, wantByPath := base.mapByID, base.mapByPath
	wantActions, err := json.Marshal(base.MapActionReferences())
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([][32]byte, len(base.maps))
	for i := range base.maps {
		value, e := base.readMap(i)
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		hashes[i] = sha256.Sum256(b)
	}
	if err = base.EnableMapCache(a, DefaultMapCacheOptions()); err != nil {
		t.Fatal(err)
	}
	wantPinned := streamingPinnedPositions(base)
	wantPinBytes := base.MapCacheSnapshot().PinnedEstimatedBytes
	base = nil
	resolver = nil
	a.ReleaseReadCaches()
	runtime.GC()
	streamed, err := LoadSourceCached(context.Background(), a, Options{}, DefaultMapCacheOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantSnapshot, streamed.Snapshot()) || !reflect.DeepEqual(wantByID, streamed.mapByID) || !reflect.DeepEqual(wantByPath, streamed.mapByPath) {
		t.Fatal("整档表计数或别名位置改变")
	}
	gotActions, err := json.Marshal(streamed.MapActionReferences())
	if err != nil || string(gotActions) != string(wantActions) {
		t.Fatal("教学动作元数据改变", err)
	}
	if !reflect.DeepEqual(wantPinned, streamingPinnedPositions(streamed)) || streamed.MapCacheSnapshot().PinnedEstimatedBytes != wantPinBytes {
		t.Fatal("复杂地图选择、顺序或预算改变")
	}
	for i, want := range hashes {
		value, e := streamed.readMap(i)
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if sha256.Sum256(b) != want {
			t.Fatalf("地图位置%d的公开完整字段改变，id=%d path=%s", i, value.ID, value.Path)
		}
	}
	resolver, err = NewResolver(streamed)
	if err != nil {
		t.Fatal(err)
	}
	gotRoutes := mapCacheRouteGoldens(resolver)
	if !reflect.DeepEqual(wantRoutes, gotRoutes) {
		t.Fatal("路由候选、来源、错误或顺序改变")
	}
	if len(hashes) == 0 || len(wantRoutes) == 0 {
		t.Fatal("实际整档样本数量不足")
	}
	t.Logf("流式整档地图字段一致数=%d 路由候选及顺序一致数=%d 复杂地图数=%d 缓存=%+v", len(hashes), len(wantRoutes), len(wantPinned), a.BoundedReadCacheSnapshot())
	receipt := map[string]any{"完整地图字段一致数": len(hashes), "路由与顺序一致数": len(wantRoutes), "教学动作元数据一致": true, "复杂地图保留位置一致": true, "保留复杂地图数": len(wantPinned), "保留估算字节": wantPinBytes, "范围": "同一固定PVF逐地图公开字段摘要、整份路由候选及错误对照；不等于Linux带数据库或客户端验收"}
	b, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(out, "流式地图整档验收.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
