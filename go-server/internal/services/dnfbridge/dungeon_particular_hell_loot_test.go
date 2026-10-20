package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestDemonRiftMultiEpicsPreserveRealPoolAndFragmentGuarantee(t *testing.T) {
	def := currentRiftLootDefinition{Equipment: []uint32{10, 11}, Pieces: []uint32{20, 21}, FragmentCount: 10, MultiEpicCount: true}
	var seen [5]bool
	for seed := uint32(1); seed <= 10000; seed++ {
		items := def.selectItems(newCurrentDungeonDropLCG(seed))
		epics := len(items) - 10
		if epics < 1 || epics > 4 {
			t.Fatalf("seed=%d epics=%d", seed, epics)
		}
		seen[epics] = true
		for i, item := range items {
			if item.Amount != 1 || (i < 10 && item.ItemID != 20 && item.ItemID != 21) || (i >= 10 && item.ItemID != 10 && item.ItemID != 11) {
				t.Fatal("fragment guarantee or real epic pool changed")
			}
		}
	}
	for n := 1; n <= 4; n++ {
		if !seen[n] {
			t.Fatalf("never observed %d equipment", n)
		}
	}
}

func TestRiftRegisteredFragmentsUseNormalPickupAndReplayLedger(t *testing.T) {
	s, runtime, repos := prepareCurrentEpicPieceDropTest(t)
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("prepared scene missing")
	}
	drops, err := s.dungeonMonsterTable.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runtime.riftLoot = &currentRiftLootDefinition{Equipment: []uint32{9001}, Pieces: []uint32{epicPieceBookTestPieceA}, EpicRate: 0, FragmentCount: 10, DropCatalog: drops, MonsterCatalog: s.dungeonMonsterTable}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	wires, err := s.planCurrentRiftBossDrops(session, runtime, scene)
	if err != nil || len(wires) != 10 {
		t.Fatalf("registered fragments=%d err=%v", len(wires), err)
	}
	for _, wire := range wires {
		if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(wire.SceneObjectKey)); err != nil {
			t.Fatal(err)
		}
		if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(wire.SceneObjectKey)); err != nil {
			t.Fatal(err)
		}
	}
	account, found, err := repos.Account.Load(context.Background(), epicPieceBookTestAccountID)
	if err != nil || !found || dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA] != 10 {
		t.Fatalf("fragment balance=%v found=%t err=%v", dnfrepo.AccountEpicPieceBalances(account), found, err)
	}
	bag, found, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil || !found || len(bag.Slots) != 0 {
		t.Fatalf("fragments leaked into bag: %+v err=%v", bag, err)
	}
}

func TestRiftLootPreservesDungeonPoolAndGuaranteedFragments(t *testing.T) {
	def, err := parseCurrentRiftLoot("[dungeon]\n[index] 100\n[item list] 99\n[/item list]\n[/dungeon]\n[dungeon]\n[index] 316\n[item list] 10 11\n[/item list]\n[/dungeon]\n[inferno weapon creation rate] 0.50\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Equipment) != 2 || def.Equipment[0] != 10 || def.Equipment[1] != 11 || def.EpicRate != 0.5 {
		t.Fatalf("pool=%+v", def)
	}
	def.Pieces = []uint32{20, 21}
	def.FragmentCount = 10
	for _, rate := range []float64{0, 0.5, 1} {
		def.EpicRate = rate
		for seed := uint32(1); seed < 200; seed++ {
			items := def.selectItems(newCurrentDungeonDropLCG(seed))
			if len(items) < 10 || len(items) > 11 || (rate == 0 && len(items) != 10) || (rate == 1 && len(items) != 11) {
				t.Fatalf("rate %f rows=%v", rate, items)
			}
			for i, item := range items {
				if item.Amount != 1 || (i < 10 && item.ItemID != 20 && item.ItemID != 21) || (i == 10 && item.ItemID != 10 && item.ItemID != 11) {
					t.Fatal("invented loot or missing guarantee")
				}
			}
		}
	}
	if _, err := parseCurrentRiftLoot("[inferno weapon creation rate] 0.5\n[dungeon]\n[index] 100\n[item list] 99\n[/item list]\n"); err == nil {
		t.Fatal("unrelated dungeon borrowed as fallback")
	}
}

func TestRealPVFRiftLootAndNativeTicketWeight(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("live PVF not configured")
	}
	t.Setenv("DNFBRIDGE_RIFT_EPIC_RATE", "")
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	m, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{pvfPath: path}, initialEquipmentArchive: a, dungeonMonsterTable: m}
	weight, err := s.currentRiftRequiredItemWeight()
	if err != nil || weight != 1 {
		t.Fatalf("native ticket weight=%f err=%v", weight, err)
	}
	def, err := s.prepareCurrentRiftLoot(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Equipment) < 200 || len(def.Pieces) < 200 || def.EpicRate != 0.5 || def.FragmentCount != 10 {
		t.Fatalf("pool=%d pieces=%d probability=%f fragments=%d", len(def.Equipment), len(def.Pieces), def.EpicRate, def.FragmentCount)
	}
	t.Logf("real PVF rift: %d equipment entries, %d mapped fragment entries, epic probability %.2f, fragments %d", len(def.Equipment), len(def.Pieces), def.EpicRate, def.FragmentCount)
}
