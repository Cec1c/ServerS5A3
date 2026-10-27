package worldmap

import (
	"context"
	"os"
	"reflect"
	"runtime"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

var compactMapLookupSink Map

func TestRealPVFCompactMapTokensPreserveAllMapFields(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("未指定真实PVF")
	}
	a, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), a, Options{SkipDungeons: true, SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	packed, largest, biggest := 0, -1, 0
	for index, stored := range table.maps {
		count := 0
		for _, tokens := range stored.compactSourceTokens {
			count += len(tokens)
		}
		packed += count
		if count > biggest {
			largest, biggest = index, count
		}
		text, err := a.ReadText(stored.Path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := ParseDocument(stored.Path, text)
		if err != nil {
			t.Fatal(err)
		}
		want, err := parseMapWithImports(stored.ID, stored.Path, doc, func(p string) (string, *dnfpvf.Document, error) {
			text, err := a.ReadText(p)
			if err != nil {
				return "", nil, err
			}
			d, err := ParseDocument(p, text)
			return p, d, err
		})
		if err != nil {
			t.Fatal(err)
		}
		// 比较目录公开查询既有的副本口径；其原有克隆会将空切片归一为 nil。
		want = cloneMap(want)
		if got := cloneMap(stored); !reflect.DeepEqual(got, want) {
			t.Fatalf("地图%d/%s公开查询字段不一致", stored.ID, stored.Path)
		}
		if index%128 == 0 {
			a.ReleaseReadCaches()
		}
	}
	if packed == 0 || largest < 0 {
		t.Fatal("真实地图没有实际采用紧凑保存")
	}
	a.ReleaseReadCaches()
	runtime.GC()
	stored := table.maps[largest]
	legacy := cloneMap(stored)
	baseline := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			compactMapLookupSink = cloneMap(legacy)
		}
	})
	optimized := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			compactMapLookupSink = cloneMap(stored)
		}
	})
	t.Logf("真实地图%d份、紧凑节点%d个；最大样本%s节点%d个，完整地图查询：原副本%d纳秒，紧凑恢复%d纳秒；分配字节%d/%d", len(table.maps), packed, stored.Path, biggest, baseline.NsPerOp(), optimized.NsPerOp(), baseline.AllocedBytesPerOp(), optimized.AllocedBytesPerOp())
	compactMapLookupSink = Map{}

	runtime.KeepAlive(table)
}
