package dnfbridge

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"sort"
	"testing"
)

func TestAccountActivityRealPVFEquipmentRarityClassification(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要本机实际PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	g := memory.NewMemoryGroup()
	s := &Service{options: options{accountID: "a"}, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{selectedCharacterID: 1}
	r := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 310}}
	ids := []uint32{}
	for id, ref := range catalog.itemRefs {
		if ref.kind == dungeonDropItemEquipment {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	found := map[int64]bool{}
	for _, id := range ids {
		definition, e := catalog.ResolveItem(id)
		if e != nil {
			t.Fatal(e)
		}
		if definition.Rarity < 3 || definition.Rarity > 5 || found[definition.Rarity] {
			continue
		}
		event, e := s.currentActivityPickupPartyLocked(session, r, runtimeDungeonDrop{ObjectKey: id, Item: definition, Amount: 1})
		if e != nil || event == nil || event.ItemID != id || event.Rarity != definition.Rarity {
			t.Fatalf("真实品质分类失败：%+v %v", definition, e)
		}
		if err := event.Validate(); err != nil {
			t.Fatal(err)
		}
		found[definition.Rarity] = true
		t.Logf("实际PVF装备：编号=%d，品质=%d，统计=%s", id, definition.Rarity, event.Metric)
		if len(found) == 3 {
			break
		}
	}
	if len(found) != 3 {
		t.Fatalf("实际PVF未覆盖全部目标品质：%v", found)
	}
}
