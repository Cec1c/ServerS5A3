package adventuregroup

import (
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestDayDungeonBenefitsAreExactRowsAndCloneIsDetached(t *testing.T) {
	doc, err := dnfpvf.Parse(AdventureSystem2018Path, "[attribute]\n1\n[/attribute]\n[attribute]\n6\n[etc]\n6 `add day of dungeon enter count` 1 `+%d` 1\n[/attribute]\n[attribute]\n11\n[etc]\n11 `add day of dungeon enter count` 2 `+%d` 1\n[/attribute]\n")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseDayDungeonCharacterBonuses(doc)
	if err != nil {
		t.Fatal(err)
	}
	c := RuntimeConfig{DayDungeonCharacterBonuses: rows}
	for level, want := range map[int]int64{0: 5, 1: 5, 6: 6, 11: 7} {
		got, err := c.DayDungeonAccountCharacterLimit(level)
		if err != nil || got != want {
			t.Fatalf("level %d got %d err %v", level, got, err)
		}
	}
	if _, err := c.DayDungeonAccountCharacterLimit(7); err == nil {
		t.Fatal("missing row inherited")
	}
	clone := c.Clone()
	clone.DayDungeonCharacterBonuses[6] = 5
	if c.DayDungeonCharacterBonuses[6] != 1 {
		t.Fatal("clone shares mutable benefit table")
	}
	for _, text := range []string{
		"[attribute]\n6\n[etc]\n6 `add day of dungeon enter count` 1 `+%d` 1\n[etc]\n6 `add day of dungeon enter count` 1 `+%d` 1\n[/attribute]\n",
		"[attribute]\n6\n[etc]\n6 `add day of dungeon enter count` 99 `+%d` 1\n[/attribute]\n",
		"[attribute]\n6\n[etc]\n7 `add day of dungeon enter count` 1 `+%d` 1\n[/attribute]\n",
	} {
		doc, err := dnfpvf.Parse(AdventureSystem2018Path, text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseDayDungeonCharacterBonuses(doc); err == nil {
			t.Fatal("unverified quota benefit accepted")
		}
	}
}

func TestRealPVFDayDungeonAccountCharacterBenefits(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText(AdventureSystem2018Path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(AdventureSystem2018Path, text)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseDayDungeonCharacterBonuses(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 30 {
		t.Fatalf("attribute rows %d, expected actual max level 30", len(rows))
	}
	config := RuntimeConfig{DayDungeonCharacterBonuses: rows}
	for level := 1; level <= 30; level++ {
		want := int64(5 + (level-1)/5)
		got, err := config.DayDungeonAccountCharacterLimit(level)
		if err != nil || got != want {
			t.Fatalf("actual level %d: got %d want %d err %v", level, got, want, err)
		}
	}
}
