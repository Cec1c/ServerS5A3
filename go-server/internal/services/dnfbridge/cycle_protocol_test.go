package dnfbridge

import (
	"errors"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestCycleAdmissionSharesNativeOrderAcrossVariants(t *testing.T) {
	rows, found, err := parseCurrentDungeonCycleRows("[dungeon cycle]\n45 -1 5097 3 0 45 -1 5098 1 1 45 0 3570 3 0 45 0 3571 1 1\n[/dungeon cycle]")
	if err != nil || !found {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC)
	character := dnfrepo.CharacterRecord{Stats: map[string]int64{}}
	for _, id := range []int64{5097, 3570} {
		rule, declared, err := cycleAdmissionForRows(rows, id)
		if err != nil || !declared || rule.Limit != 3 || len(rule.DungeonIDs) != 2 || rule.DungeonIDs[0] != 3570 || rule.DungeonIDs[1] != 5097 {
			t.Fatalf("shared normal=%+v found%t err%v", rule, declared, err)
		}
		if err := dnfdungeon.ValidateCycleEntryAllowance(character, rule, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{5098, 3571} {
		rule, _, err := cycleAdmissionForRows(rows, id)
		if err != nil || rule.Limit != 1 || len(rule.DungeonIDs) != 2 || len(rule.PrerequisiteDungeonIDs) != 2 || rule.PrerequisiteCount != 3 {
			t.Fatalf("shared trial=%+v err%v", rule, err)
		}
		if err := dnfdungeon.ValidateCycleEntryAllowance(character, rule, at); !errors.Is(err, dnfdungeon.ErrEntryPrerequisiteMissing) {
			t.Fatalf("trial prematurely open: %v", err)
		}
	}
	if _, found, err := cycleAdmissionForRows(rows, 316); err != nil || found {
		t.Fatal("rift entered cycle quota")
	}
	rows[2].DailyCount = 4
	if _, _, err := cycleAdmissionForRows(rows, 5097); err == nil {
		t.Fatal("accepted conflicting shared cap")
	}
	rows[2].DailyCount = 3
	rows[1].DailyCount = 2
	if _, _, err := cycleAdmissionForRows(rows, 5098); err == nil {
		t.Fatal("accepted unsupported trial cap")
	}
}

func TestCycleExplicitArchiveFailureAndProfileBoundary(t *testing.T) {
	s := &Service{options: options{pvfPath: "missing-cycle-archive.pvf", currentClientVersion: "official-24.9"}}
	if _, _, err := s.buildCurrentDungeonCycleInfo(dnfrepo.CharacterRecord{}); err == nil {
		t.Fatal("explicit missing archive produced a false empty snapshot")
	}
	// Other profiles and absent selection do not query an unavailable archive.
	s.options.currentClientVersion = "other"
	if err := s.sendCurrentDungeonCycleInfo(&gameSession{selectedCharacterID: 77}, dnfrepo.CharacterRecord{}); err != nil {
		t.Fatal(err)
	}
	s.options.currentClientVersion = "official-24.9"
	if err := s.sendCurrentDungeonCycleInfo(&gameSession{}, dnfrepo.CharacterRecord{}); err != nil {
		t.Fatal(err)
	}
}
