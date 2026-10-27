package dnfbridge

import (
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type disjointCountingSource struct {
	dnfpvf.Source
	reads atomic.Int64
}

func (s *disjointCountingSource) ReadText(path string) (string, error) {
	s.reads.Add(1)
	return s.Source.ReadText(path)
}

func TestDisjointConfigCacheIsPerCatalogAndRetriesFailure(t *testing.T) {
	_, base := mustCurrentDisjointTestAssets(t, nil)
	expected, err := loadCurrentDisjointPVFConfig(base.source)
	if err != nil {
		t.Fatal(err)
	}
	source := &disjointCountingSource{Source: base.source}
	catalog := &pvfDungeonDropCatalog{source: source}
	first, err := catalog.currentDisjointPVFConfig()
	if err != nil || !reflect.DeepEqual(first, expected) {
		t.Fatal("缓存改变静态规则", err)
	}
	reads := source.reads.Load()
	var wait sync.WaitGroup
	for i := 0; i < 24; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, err := catalog.currentDisjointPVFConfig()
			if err != nil || !reflect.DeepEqual(got, expected) {
				t.Error("并发缓存结果改变", err)
			}
		}()
	}
	wait.Wait()
	if source.reads.Load() != reads {
		t.Fatal("成功后仍重复读取和解析配置")
	}
	copy := bridgePVFSource{}
	for path, value := range base.source.(bridgePVFSource) {
		copy[path] = value
	}
	original := copy["etc/disjoint.etc"]
	delete(copy, "etc/disjoint.etc")
	other := &pvfDungeonDropCatalog{source: copy}
	if _, err := other.currentDisjointPVFConfig(); err == nil {
		t.Fatal("缺少规则被忽略")
	}
	copy["etc/disjoint.etc"] = strings.Replace(original, "150 1", "300 1", 1)
	second, err := other.currentDisjointPVFConfig()
	if err != nil || second.CubeBase != 300 || first.CubeBase != 150 {
		t.Fatal("失败被缓存或目录间相互污染", err)
	}
}

func BenchmarkRealPVFDisjointConfig(b *testing.B) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		b.Skip("需要匹配真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	for _, cached := range []bool{false, true} {
		name := "重复解析"
		if cached {
			name = "目录复用"
		}
		b.Run(name, func(b *testing.B) {
			catalog := &pvfDungeonDropCatalog{source: archive}
			if _, err := catalog.currentDisjointPVFConfig(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if cached {
					_, err = catalog.currentDisjointPVFConfig()
				} else {
					_, err = loadCurrentDisjointPVFConfig(archive)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
