package dnfbridge

import (
	"context"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
	"time"
)

type genericTextOnlySource struct{ archive *platformpvf.Archive }

func (s genericTextOnlySource) ReadText(path string) (string, error) { return s.archive.ReadText(path) }

func TestGenericPoolGroupedRealPVF(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires deployed PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.EnableBoundedReadCaches(8<<20, 8<<20); err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	n := 600
	if len(c.stackableIDs) < n {
		n = len(c.stackableIDs)
	}
	ids := make([]uint32, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, c.stackableIDs[i*len(c.stackableIDs)/n])
	}
	old := &pvfDungeonDropCatalog{source: genericTextOnlySource{a}, itemRefs: c.itemRefs, stackableIDs: ids, genericStackables: make(map[dungeonDropGradeRarityKey][]uint32)}
	grouped := &pvfDungeonDropCatalog{source: a, itemRefs: c.itemRefs, stackableIDs: ids, genericStackables: make(map[dungeonDropGradeRarityKey][]uint32)}
	if err = old.loadGenericStackablesLocked(); err != nil {
		t.Fatal(err)
	}
	if err = grouped.loadGenericStackablesLocked(); err != nil {
		t.Fatal(err)
	}
	if len(old.genericStackables) == 0 {
		t.Fatal("sample has no eligible entries")
	}
	if !reflect.DeepEqual(old.genericStackables, grouped.genericStackables) {
		t.Fatal("grouped load differs from existing candidate order")
	}
	a.ReleaseReadCaches()
	start := time.Now()
	if err = c.ensureGenericPools(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, v := range c.genericStackables {
		count += len(v)
	}
	t.Logf("stackable refs=%d eligible=%d grouped pool load=%s", len(c.stackableIDs), count, time.Since(start))
	if count == 0 || len(c.genericEquipment) == 0 {
		t.Fatal("real pools empty")
	}
}

// Exercise the actual startup boundary: no first player can trigger a cold
// full-PVF generic pool scan after the listeners have opened.
func TestGenericDropPoolPrewarmedBeforeDungeonService(t *testing.T) {
	s, _, _ := prepareSemesterActualReady(t)
	s.options.lowMemory = true
	if err := s.prepareLowMemoryPVFReadCaches(s.initialEquipmentArchive); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.preloadDungeonWorldMap(context.Background()); err != nil {
		t.Fatal(err)
	}
	table, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := table.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.genericLoaded || catalog.genericErr != nil {
		t.Fatalf("pool not ready at startup: loaded=%v error=%v", catalog.genericLoaded, catalog.genericErr)
	}
	callStart := time.Now()
	_, _, err = catalog.SelectGenericStackable(newCurrentDungeonDropLCG(1), 17, 2, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(callStart); elapsed > time.Second {
		t.Fatalf("prewarmed selection blocked for %s", elapsed)
	}
	t.Logf("real PVF world-map and generic preload completed in %s; later selection %s", time.Since(start), time.Since(callStart))
}
