package raid

import (
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func realFiendWarSource(t *testing.T) *platformpvf.Archive {
	t.Helper()
	ref := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if ref == "" {
		t.Skip("requires current production PVF")
	}
	a, err := platformpvf.Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRealPVFFiendWarBattlefieldConfigurationAndDependencies(t *testing.T) {
	a := realFiendWarSource(t)
	c, err := LoadFiendWarConfiguration(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Areas) != 30 || c.MemberMin != 1 || c.MemberMax != 8 || len(c.DungeonDependencies()) != 26 || len(c.Battle.PhaseDungeons[0]) != 2 || c.Battle.PhaseLimitSeconds[0] != 3600 {
		t.Fatal("actual battlefield configuration changed", c)
	}
	if err := ValidateDungeonDependencies(a, c.DungeonDependencies()); err != nil {
		t.Fatal("intermediate battlefield dependencies", err)
	}
	r, err := LoadFiendWarRewardConfiguration(a)
	if err != nil || len(r.Phases) != 1 || r.Phases[0].MinimumClears != 1 {
		t.Fatal("actual Fiend War rewards unavailable", err)
	}
	for rank := byte(0); rank < 4; rank++ {
		for _, typ := range []string{"party_card", "fw_party_card", "pcroom_card", "squad_item"} {
			pool, total, err := r.Phases[0].Pool(typ, rank)
			if err != nil || len(pool) == 0 || total != 100 {
				t.Fatal("Fiend War pool", typ, rank, total, err)
			}
		}
	}
}

func TestRealPVFFiendWarRejectsIncompleteMovementOrFinalArea(t *testing.T) {
	a := realFiendWarSource(t)
	const ref = "etc/raid/fiendwar/fiendwar.etc"
	text, err := a.ReadText(ref)
	if err != nil {
		t.Fatal(err)
	}
	symbols, err := a.ReadText(ref + ".symbol")
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]string{
		"foreign-area":       strings.Replace(text, "3 4 5 ", "3 4 999 ", 1),
		"missing-boss-owner": strings.Replace(text, "[DUNGEON INDEX]\n4978 ", "[DUNGEON INDEX]\n-1 ", 1),
		"missing-deadline":   strings.Replace(text, "[DUNGEON LIMIT TIME]\n600 ", "[DUNGEON LIMIT TIME]\n0 ", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if changed == text {
				t.Fatal("mutation did not match actual PVF")
			}
			if _, err := LoadFiendWarConfiguration(admissionSource{ref: changed, ref + ".symbol": symbols}); err == nil {
				t.Fatal("incomplete battlefield accepted")
			}
		})
	}
}
