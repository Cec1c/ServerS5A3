package dnfbridge

import (
	"os"
	"testing"

	appkit "longheng.io/server/internal/platform/app"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFLowMemoryReusesMatchingFrameworkArchive(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(&appkit.Env{PVF: a})
	s.options.lowMemory = true
	s.options.pvfPath = path
	s.initialEquipmentMu.Lock()
	got, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil || got != a {
		t.Fatalf("低内存档没有复用相同框架归档：%v", err)
	}
	budget := got.BoundedReadCacheSnapshot()
	if budget == nil || budget.TextBudget != 8<<20 || budget.ChunkBudget != 8<<20 {
		t.Fatalf("未启用预期预算：%+v", budget)
	}
}
