package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
)

const testRadianceItem uint32 = 10163136

func configureRadianceTestSource(t *testing.T, s *Service, rows string) {
	t.Helper()
	configureBirthBudDropTestSource(t, s)
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	source[dungeonDropStackableList] += "\n10163136 `material/radiance.stk`\n"
	source["stackable/material/radiance.stk"] = "[name]\n`次元光辉`\n[stackable type]\n`[material]` 0\n[stack limit]\n999\n[attach type]\n`[free]`\n"
	source[currentDungeonIndependentDropPath] = "[independent drop]\n[/independent drop]\n"
	if rows != "" {
		source[currentDungeonIndependentDropPath] += "[" + dnfdrops.DungeonMaterialSection + "]\n" + rows + "\n[/" + dnfdrops.DungeonMaterialSection + "]\n"
	}
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredMaterialPipelineScopeAndRanks(t *testing.T) {
	s, r, _ := prepareCurrentDungeonDropTest(t, 1, 3227)
	// Guaranteed rows make inclusion/exclusion deterministic; the probability
	// test below separately checks the actual low rates and RNG boundaries.
	rows := ""
	for id := 310; id <= 316; id++ {
		rows += fmt.Sprintf("%d 10163136 0 0 0 1000000\n", id)
	}
	rows += "7539 10163136 1000000 1000000 1000000 1000000"
	configureRadianceTestSource(t, s, rows)
	scene, _ := r.Session.Scene()
	visit, _ := r.currentDungeonRoomVisit(scene)
	m := r.Room.Snapshot().Monsters[0]
	for _, id := range []int64{309, 310, 311, 312, 313, 314, 315, 316, 317, 5000, 7538, 7539, 7540} {
		for rank, name := range []string{"[normal]", "[champion]", "[super champion]", "[boss]"} {
			for difficulty := byte(0); difficulty < 5; difficulty++ {
				r.Dungeon.ID, m.Spawn.Rank, r.Request.Difficulty = id, name, difficulty
				visit.DropRNG, r.NextObjectKey, r.DropOwner = 79, 200, newRuntimeDungeonDropOwner()
				wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, wire := range wires {
					if binary.LittleEndian.Uint32(wire.Item.data[2:6]) == testRadianceItem {
						count++
						if binary.LittleEndian.Uint32(wire.Item.data[6:10]) != 1 {
							t.Fatal("amount is not one")
						}
					}
				}
				want := 0
				if id == 7539 || (id >= 310 && id <= 316 && rank == 3) {
					want = 1
				}
				if count != want {
					t.Fatalf("dungeon=%d rank=%s difficulty=%d count=%d want=%d", id, name, difficulty, count, want)
				}
			}
		}
	}
}

func TestConfiguredMaterialProbabilityAndInvalidReference(t *testing.T) {
	s, _, _ := prepareCurrentDungeonDropTest(t, 1, 3227)
	configureRadianceTestSource(t, s, "")
	catalog, err := s.dungeonMonsterTable.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, probability := range []int{0, 10000, 100000, 1000000} {
		rule := dnfdrops.DungeonMaterialRule{ItemID: testRadianceItem, Probability: [4]int{probability, probability, probability, probability}}
		for rank := byte(0); rank < 4; rank++ {
			rng := newCurrentDungeonDropLCG(72)
			hits := 0
			for i := 0; i < 10000; i++ {
				model := newCurrentDungeonDropLCG(rng.Seed())
				want := probability > 0 && model.Next(1000000) < probability
				items, err := currentDungeonConfiguredMaterialItems(catalog, []dnfdrops.DungeonMaterialRule{rule}, rank, rng)
				if err != nil || (len(items) == 1) != want || len(items) > 1 || rng.Seed() != model.Seed() {
					t.Fatalf("prob=%d rank=%d items=%v err=%v", probability, rank, items, err)
				}
				if len(items) == 1 {
					hits++
					if items[0] != testRadianceItem {
						t.Fatal(items)
					}
				}
			}
			if probability == 10000 && (hits < 60 || hits > 140) || probability == 100000 && (hits < 850 || hits > 1150) {
				t.Fatal("unexpected low-rate sample", probability, hits)
			}
		}
	}
	for _, item := range []uint32{9001, 99999999} {
		rng := newCurrentDungeonDropLCG(72)
		_, err := currentDungeonConfiguredMaterialItems(catalog, []dnfdrops.DungeonMaterialRule{{ItemID: item, Probability: [4]int{1000000}}}, 0, rng)
		if err == nil || rng.Seed() != 72 {
			t.Fatal("invalid reference accepted or advanced stream", item, err)
		}
	}
	rng := newCurrentDungeonDropLCG(72)
	if items, err := currentDungeonConfiguredMaterialItems(catalog, nil, 0, rng); err != nil || len(items) != 0 || rng.Seed() != 72 {
		t.Fatal("absent rules changed behavior")
	}
}

func TestConfiguredMaterialAbsentAndDisabledPreserveLegacyDrops(t *testing.T) {
	for _, rows := range []string{"", "700 10163136 0 0 0 0", "7539 10163136 10000 10000 10000 100000"} {
		s, r, _ := prepareCurrentDungeonDropTest(t, 1, 3227)
		configureRadianceTestSource(t, s, "")
		scene, _ := r.Session.Scene()
		visit, _ := r.currentDungeonRoomVisit(scene)
		m := r.Room.Snapshot().Monsters[0]
		type snapshot struct {
			wires []currentDungeonDeathDropWire
			seed  uint32
		}
		var before []snapshot
		for seed := uint32(0); seed < 32; seed++ {
			visit.DropRNG, r.NextObjectKey, r.DropOwner = seed, 200, newRuntimeDungeonDropOwner()
			wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
			if err != nil {
				t.Fatal(err)
			}
			before = append(before, snapshot{wires, visit.DropRNG})
		}
		configureRadianceTestSource(t, s, rows)
		for seed := uint32(0); seed < 32; seed++ {
			visit.DropRNG, r.NextObjectKey, r.DropOwner = seed, 200, newRuntimeDungeonDropOwner()
			wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
			if err != nil || !reflect.DeepEqual(wires, before[seed].wires) || visit.DropRNG != before[seed].seed {
				t.Fatalf("legacy drops or RNG changed: rows=%q seed=%d err=%v", rows, seed, err)
			}
		}
	}
}

func TestConfiguredMaterialPartyDeathReplayAndPickupOnce(t *testing.T) {
	s, leader, peer, r, peerRuntime := partyDungeonDeathFixture(t)
	configureRadianceTestSource(t, s, "7539 10163136 1000000 1000000 1000000 1000000")
	r.Dungeon.ID, peerRuntime.Dungeon.ID = 7539, 7539
	m := r.Room.Snapshot().Monsters[0]
	for _, member := range []*gameSession{leader, peer} {
		member.conn.(*bufferConn).write.Reset()
	}
	killDungeonActors(t, s, leader, m.ObjectKey)
	keys := map[uint16]uint32{}
	for _, member := range []*gameSession{leader, peer} {
		for _, body := range abyssOp38Bodies(t, member.conn.(*bufferConn).write.Bytes()) {
			for _, row := range abyssEpicFragmentDropRows(t, body) {
				if row.ItemID == testRadianceItem {
					if keys[member.selectedCharacterID] != 0 {
						t.Fatal("duplicate personal material")
					}
					keys[member.selectedCharacterID] = row.ObjectKey
				}
			}
		}
		if keys[member.selectedCharacterID] == 0 {
			t.Fatal("missing personal material", member.selectedCharacterID)
		}
	}
	if keys[leader.selectedCharacterID] == keys[peer.selectedCharacterID] {
		t.Fatal("shared object key")
	}
	before := len(r.DropOwner.byObjectKey)
	killDungeonActors(t, s, peer, m.ObjectKey)
	if len(r.DropOwner.byObjectKey) != before {
		t.Fatal("death replay rerolled")
	}
	for _, member := range []*gameSession{leader, peer} {
		for attempt := 0; attempt < 2; attempt++ {
			if err := s.handleCurrentDungeonPickup(member, currentDungeonPickupTestBody(keys[member.selectedCharacterID])); err != nil {
				t.Fatal(err)
			}
		}
		repos, _ := s.repositoryGroup()
		bag, found, err := repos.Inventory.Load(context.Background(), strconv.Itoa(int(member.selectedCharacterID)))
		if err != nil || !found {
			t.Fatal("inventory load", err)
		}
		var amount int64
		for _, stack := range bag.Slots {
			if stack.ItemID == int64(testRadianceItem) {
				amount += stack.Count
			}
		}
		if amount != 1 {
			t.Fatal("duplicate pickup", amount)
		}
	}
}

func TestConfiguredMaterialRiftBossDedicatedPipeline(t *testing.T) {
	for _, id := range []int64{316, 7539, 6063} {
		s, r, _ := prepareCurrentEpicPieceDropTest(t)
		configureRadianceTestSource(t, s, fmt.Sprintf("%d 10163136 0 0 0 1000000", id))
		s.options.currentClientVersion = "official-24.9"
		r.Dungeon.ID = id
		catalog, err := s.dungeonMonsterTable.DropCatalog()
		if err != nil {
			t.Fatal(err)
		}
		r.riftLoot = &currentRiftLootDefinition{Equipment: []uint32{9001}, Pieces: []uint32{epicPieceBookTestPieceA}, EpicRate: 0, FragmentCount: 10, DropCatalog: catalog, MonsterCatalog: s.dungeonMonsterTable}
		scene, _ := r.Session.Scene()
		session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
		wires, err := s.planCurrentRiftBossDrops(session, r, scene)
		if err != nil {
			t.Fatal(err)
		}
		pieces, materials := 0, 0
		for _, wire := range wires {
			switch binary.LittleEndian.Uint32(wire.Item.data[2:6]) {
			case epicPieceBookTestPieceA:
				pieces++
			case testRadianceItem:
				materials++
			}
		}
		want := 1
		if id == 6063 {
			want = 0
		} // Epic Road already rolls ordinary monster drops.
		if pieces != 10 || materials != want {
			t.Fatalf("dungeon=%d fragments=%d materials=%d want=%d", id, pieces, materials, want)
		}
	}
}
