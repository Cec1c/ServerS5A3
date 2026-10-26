package guild

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

const practiceCatalogFixture = `[friend agit]
[index]
8510
[/friend agit]
[friend barricade dungeon]
8511 8512 8513
[/friend barricade dungeon]
[guardian dummy monster]
9
[guardian list]
[guardian]
1
[index]
71021 71022 71023 71024 71025
[/guardian]
[guardian]
2
[index]
63830 63830 63831 63831 63832
[/guardian]
[/guardian list]`

func TestPracticeCatalogNativeGuardianAndSelector(t *testing.T) {
	text := practiceCatalogFixture
	if file := os.Getenv("GUILD_PRACTICE_EXTRACTED_ETC"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text = string(b)
		t.Log("using extracted matching agitwar.etc")
	}
	catalog, err := ParsePracticeCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	for expansion := uint8(0); expansion < 3; expansion++ {
		base, barricade, err := catalog.FriendlyDungeons(expansion)
		if err != nil || base != 8510 || barricade != 8511+uint32(expansion) {
			t.Fatal(base, barricade, err)
		}
	}
	for _, expansion := range []uint8{3, 255} {
		if _, _, err := catalog.FriendlyDungeons(expansion); err == nil {
			t.Fatal("unowned expansion accepted")
		}
	}
	for id, monsters := range map[uint32][5]uint32{1: {71021, 71022, 71023, 71024, 71025}, 2: {63830, 63830, 63831, 63831, 63832}} {
		for level := uint8(1); level <= 50; level++ {
			t.Run(fmt.Sprintf("guardian%d/level%d", id, level), func(t *testing.T) {
				state := &GuardianState{EverAcquired: true, ActiveID: id, Owned: []Guardian{{ID: id, Level: level, Skills: [5]uint8{1, 2, 3, 4, 5}, APProgress: 10, GPProgress: 20}}}
				owned := state.Owned[0]
				projection, err := catalog.GuardianProjection(state)
				if err != nil || projection.MonsterID != monsters[(level-1)/10] || projection.Guardian != owned {
					t.Fatal(projection, err)
				}
				state.Owned[0].Level = 1
				if projection.Guardian != owned {
					t.Fatal("projection aliases mutable guild state")
				}
				if projection.ReplaceMonster(9, 3) != projection.MonsterID || projection.ReplaceMonster(9, 2) != 9 || projection.ReplaceMonster(63829, 3) != 63829 {
					t.Fatal("native type/dummy restriction changed")
				}
			})
		}
	}
	for _, state := range []*GuardianState{nil, {}, {EverAcquired: true, Owned: []Guardian{{ID: 1, Level: 1}}}} {
		p, err := catalog.GuardianProjection(state)
		if err != nil || p.ReplaceMonster(9, 3) != 9 || p.MonsterID != 0 {
			t.Fatal(p, err)
		}
	}
	for _, state := range []*GuardianState{
		{ActiveID: 1}, {EverAcquired: true, ActiveID: 1, Owned: []Guardian{{ID: 1, Level: 0}}},
		{EverAcquired: true, ActiveID: 1, Owned: []Guardian{{ID: 1, Level: 51}}},
	} {
		if _, err := catalog.GuardianProjection(state); err == nil {
			t.Fatal("invalid owned state accepted")
		}
	}
}

func TestPracticeCatalogRejectsMalformedAndUsesRealIndexOrder(t *testing.T) {
	for name, text := range map[string]string{
		"empty": "", "missing-list-end": strings.TrimSuffix(practiceCatalogFixture, "[/guardian list]"),
		"duplicate-list":          practiceCatalogFixture + "\n[guardian list]\n[/guardian list]",
		"duplicate-base":          strings.Replace(practiceCatalogFixture, "[/friend agit]", "[index]\n8510\n[/friend agit]", 1),
		"missing-base":            strings.Replace(practiceCatalogFixture, "[index]\n8510", "", 1),
		"duplicate-dungeon":       strings.Replace(practiceCatalogFixture, "8511 8512 8513", "8510 8512 8513", 1),
		"short-array":             strings.Replace(practiceCatalogFixture, "8511 8512 8513", "8511 8512", 1),
		"duplicate-guardian":      strings.Replace(practiceCatalogFixture, "[guardian]\n2", "[guardian]\n1", 1),
		"extra-guardian":          strings.Replace(practiceCatalogFixture, "[guardian]\n2", "[guardian]\n3", 1),
		"short-monsters":          strings.Replace(practiceCatalogFixture, "71021 71022 71023 71024 71025", "71021 71022", 1),
		"duplicate-monster-field": strings.Replace(practiceCatalogFixture, "71021 71022 71023 71024 71025", "71021 71022 71023 71024 71025\n[index]\n1 2 3 4 5", 1),
		"zero-monster":            strings.Replace(practiceCatalogFixture, "71025", "0", 1),
		"negative-monster":        strings.Replace(practiceCatalogFixture, "71025", "-1", 1),
		"overflow":                strings.Replace(practiceCatalogFixture, "71025", "2147483648", 1),
		"noninteger":              strings.Replace(practiceCatalogFixture, "71025", "`bad`", 1),
		"no-dummy":                strings.Replace(practiceCatalogFixture, "[guardian dummy monster]\n9", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePracticeCatalog(text); err == nil {
				t.Fatal("malformed catalog accepted")
			}
		})
	}
	c, err := ParsePracticeCatalog(strings.Replace(practiceCatalogFixture, "8511 8512 8513", "8513 8511 8512", 1))
	if err != nil {
		t.Fatal(err)
	}
	var got []uint32
	for i := uint8(0); i < 3; i++ {
		_, id, err := c.FriendlyDungeons(i)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if !reflect.DeepEqual(got, []uint32{8513, 8511, 8512}) {
		t.Fatal(got)
	}
	var nilCatalog *PracticeCatalog
	if _, _, err := nilCatalog.FriendlyDungeons(0); err == nil {
		t.Fatal("nil catalog accepted")
	}
	if _, err := nilCatalog.GuardianProjection(nil); err == nil {
		t.Fatal("nil catalog accepted")
	}
	if _, err := LoadPracticeCatalog(nil); err == nil {
		t.Fatal("nil source accepted")
	}
}
