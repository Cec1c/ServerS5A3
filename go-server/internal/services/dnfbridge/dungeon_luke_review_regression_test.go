package dnfbridge

import (
	"context"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The committed multiplayer Luke binding suppresses monster materials by the
// real PVF `[stackable type] [material]`. This pins the actual item types of the
// Luke raid materials so a later asset or type change cannot silently escape the
// filter, and it proves the ordinary light-altar essence ([quest]) is not caught
// by the material filter.
func TestRealPVFLukeRaidMaterialsAreMaterialTypeAndSuppressed(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	r := reviewLukeRuntimeFor(t, session, 5054)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
		t.Fatal(err)
	}
	catalog, _ := actualAntonRewardFixture(t)
	for id, name := range map[uint32]string{
		10159142: "光之根源",
		10159143: "暗之根源",
		10159623: "魔岩石",
		10159649: "黑白的守护",
	} {
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatalf("resolve raid material %d (%s): %v", id, name, err)
		}
		if definition.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(definition.StackableType) != "[material]" {
			t.Fatalf("raid material %d (%s) kind=%v type=%q", id, name, definition.Kind, definition.StackableType)
		}
		if !f.service.currentLukeTeamMonsterMaterialSuppressed(r, definition) {
			t.Fatalf("raid material %d (%s) escaped the owned multiplayer material filter", id, name)
		}
	}
	essence, err := catalog.ResolveItem(10159404)
	if err != nil {
		t.Fatal(err)
	}
	if definition := essence; normalizeDungeonDropStackableType(definition.StackableType) != "[quest]" {
		t.Fatalf("light altar essence type=%q", definition.StackableType)
	}
	if f.service.currentLukeTeamMonsterMaterialSuppressed(r, essence) {
		t.Fatal("quest-type light altar essence must not be suppressed by the material filter")
	}
}

// Documents the current limitation: Mechanical Throne (5106) admission is
// satisfied by the persisted lifetime clear record, because
// etc/sequential_dungeon_info.etc sequence 42 exposes no reset/day field and no
// etc/ table references 5101..5106. This pins today's behavior so that adding a
// service-day reset later is an intentional change, not an accidental one.
func TestRealPVFLukeThroneAdmissionUsesPersistedLifetimeClears(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	source, err := f.service.initialEquipmentArchiveLocked()
	if err != nil {
		t.Fatal(err)
	}
	prerequisites, err := loadCurrentLukeThronePrerequisites(source)
	if err != nil {
		t.Fatal(err)
	}
	text, err := source.ReadText("dungeon/luke_normal/solium.dgn")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("dungeon/luke_normal/solium.dgn", text)
	if err != nil {
		t.Fatal(err)
	}
	dungeon := worldmap.ParseDungeon(5106, doc.Path, doc)
	for _, id := range prerequisites {
		if _, _, err := f.repos.DungeonPermission.UpsertMax(context.Background(), "19", id, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.validateCurrentLukeNormalThroneEntry(context.Background(), session, dungeon); err != nil {
		t.Fatalf("persisted lifetime clears rejected: %v", err)
	}
}

// The dual-boss clear gate is only meaningful if the real boss rooms actually
// spawn the blocking bosses. This reads the shipped maps directly: the light
// altar boss room (dungeon 5104) declares two [boss] monsters 56699/56698,
// while the dark altar boss room (dungeon 5105) declares a single [boss].
func TestRealPVFLukeAltarBossRoomsAreDualAndSingleBlocking(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	source := catalog.source
	for _, tc := range []struct {
		path       string
		dungeon    int64
		bosses     []int64
		bossMarker int
	}{
		{"map/luke_normal/light_altar/55946.map", 5104, []int64{56699, 56698}, 2},
		{"map/luke_normal/dark_altar/55953.map", 5105, []int64{56068}, 1},
	} {
		text, err := source.ReadText(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		doc, err := dnfpvf.Parse(tc.path, text)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.path, err)
		}
		if got := doc.Ints("dungeon"); len(got) != 1 || got[0] != tc.dungeon {
			t.Fatalf("%s dungeon=%v want %d", tc.path, got, tc.dungeon)
		}
		if types := doc.Texts("type"); len(types) != 1 || types[0] != "[boss]" {
			t.Fatalf("%s type=%v", tc.path, types)
		}
		tokens, ok := doc.Section("monster")
		if !ok {
			t.Fatalf("%s has no [monster] section", tc.path)
		}
		found := map[int64]int{}
		markers := 0
		for _, token := range tokens {
			if token.Kind == dnfpvf.TokenString && token.Value == "[boss]" {
				markers++
			}
			if token.Kind == dnfpvf.TokenInt {
				for _, boss := range tc.bosses {
					if token.Int == boss {
						found[boss]++
					}
				}
			}
		}
		if markers != tc.bossMarker {
			t.Fatalf("%s %d boss markers want %d", tc.path, markers, tc.bossMarker)
		}
		for _, boss := range tc.bosses {
			if found[boss] != 1 {
				t.Fatalf("%s boss %d occurrences=%d", tc.path, boss, found[boss])
			}
		}
	}
}
