package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestOfficialNormalAbyssSingleTrialAndReplay(t *testing.T) {
	s, session, conn, runtime, _, _ := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	s.options.currentClientVersion = "official-24.9"
	first, last := hellPartyRewardActorKeys(t, runtime)
	death := func(key uint32) []abyssEpicFragmentDropRow {
		conn.write.Reset()
		body := make([]byte, 88)
		binary.LittleEndian.PutUint32(body, key)
		binary.LittleEndian.PutUint16(body[4:], 99)
		body[30] = 1
		if err := s.handleDungeonMonsterDeath(session, body); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() == 0 {
			return nil
		}
		packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		return abyssEpicFragmentDropRows(t, packet.Body)
	}
	for _, key := range first {
		if rows := death(key); len(rows) != 0 {
			t.Fatal("earlier wave granted rewards")
		}
	}
	for _, key := range last[:len(last)-1] {
		if rows := death(key); len(rows) != 0 {
			t.Fatal("unfinished wave granted rewards")
		}
	}
	final := last[len(last)-1]
	rows := death(final)
	equipment, fragments := abyssEpicFragmentSplit(t, rows)
	if len(equipment) > 1 || len(fragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("epics=%d fragments=%d", len(equipment), len(fragments))
	}
	registered := len(runtime.DropOwner.byObjectKey)
	known := map[uint32]bool{}
	for _, row := range rows {
		known[row.ObjectKey] = true
	}
	for _, row := range death(final) {
		if !known[row.ObjectKey] {
			t.Fatal("death replay registered a new drop")
		}
	}
	if len(runtime.DropOwner.byObjectKey) != registered {
		t.Fatal("death replay changed reward ledger")
	}
}

func TestRealPVFTimeRiftAndNormalAbyssProbability(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	m, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{pvfPath: path, currentClientVersion: "official-24.9"}, initialEquipmentArchive: a, dungeonMonsterTable: m}
	w, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := w.FindDungeon(currentTimeRiftDungeonID)
	if !ok || d.Metadata.Name != "时空裂缝" {
		t.Fatalf("wrong dungeon: %+v", d.Metadata)
	}
	def, err := s.prepareCurrentTimeRiftLoot(d, 2)
	if err != nil {
		t.Fatal(err)
	}
	if def.EpicRate != 0.20 || def.FragmentCount != 9 || len(def.Equipment) == 0 || len(def.Pieces) == 0 {
		t.Fatalf("definition=%+v", def)
	}
	for seed := uint32(1); seed < 1000; seed++ {
		items := def.selectItems(newCurrentDungeonDropLCG(seed))
		if len(items) < 9 || len(items) > 10 {
			t.Fatalf("guarantee or single epic trial violated: %v", items)
		}
	}
	drops, err := m.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	request := hellPartyRewardRequest{DungeonMinimumLevel: d.Metadata.MinimumRequiredLevel.Value, DungeonBasisLevel: d.Metadata.BasisLevel.Value, dropCatalog: drops, rng: newCurrentDungeonDropLCG(1)}
	hits := 0
	for i := 0; i < 10000; i++ {
		result, err := currentNormalAbyssEpicClearReward(request)
		if err != nil {
			t.Fatal(err)
		}
		if result.Rolls != 1 || result.Chance != 130000 || len(result.Selected) > 1 {
			t.Fatal("normal abyss did not use one 13% trial")
		}
		hits += len(result.Selected)
	}
	if hits < 1100 || hits > 1500 {
		t.Fatalf("normal abyss sample: %d/10000", hits)
	}
	t.Logf("PVF7539 real equipment=%d mapped pieces=%d; guaranteed fragments=%d; epic=20%%; normal abyss one-trial sample=%d/10000 (13%%)", len(def.Equipment), len(def.Pieces), def.FragmentCount, hits)
}
