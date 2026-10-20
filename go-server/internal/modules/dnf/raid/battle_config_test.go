package raid

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFRaidBattleConfigurationAndSingleLinks(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		route   byte
		path    string
		first   uint32
		members int
	}{
		{0, "etc/raid/anton.etc", 210, 20}, {1, "etc/raid/luke/luke.etc", 5054, 8},
	} {
		cfg, err := LoadBattleConfiguration(archive, row.path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.StartDelaySeconds != 3 || cfg.WeeklyClearLimit != 2 || len(cfg.PhaseDungeons) != 2 || len(cfg.PhaseLimitSeconds) != 2 || cfg.PhaseDungeons[0][0] != (DungeonState{ID: row.first, State: 0}) {
			t.Fatalf("PVF battle configuration: %+v", cfg)
		}
		admission, err := LoadAdmissionRule(archive, row.path)
		if err != nil || admission.MaximumMembers != row.members {
			t.Fatalf("PVF maximum: %+v %v", admission, err)
		}
		body, err := BuildDungeonStateBody(cfg.PhaseDungeons[0], 0)
		if err != nil {
			t.Fatal(err)
		}
		if dir := os.Getenv("DNF90_CURRENT_RAID_BATTLE_NATIVE_DIR"); dir != "" {
			name := "anton"
			if row.route == 1 {
				name = "luke"
			}
			for filename, data := range map[string][]byte{name + "-map.bin": body, name + "-timer.bin": BuildTimerBody(0, 0, 1791329738+cfg.PhaseLimitSeconds[0], cfg.PhaseLimitSeconds[0]), name + "-remain.bin": BuildRemainingTimeBody(0, cfg.PhaseLimitSeconds[0])} {
				if err := os.WriteFile(filepath.Join(dir, filename), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, row := range []struct {
		route    byte
		clears   int
		sequence []uint32
	}{
		{0, 0, []uint32{5314, 5315, 5316, 5318, 5319, 5320}},
		{0, 1, []uint32{5314, 5315, 5317, 5321, 5319, 5320}},
		{1, 0, []uint32{5323, 5324, 5325, 5326, 5327, 5328}},
		{1, 1, []uint32{5329, 5330, 5331, 5332, 5333, 5334}},
	} {
		got, err := LoadSingleRaidSequence(archive, row.route, row.clears)
		if err != nil || !reflect.DeepEqual(got, row.sequence) {
			t.Fatalf("real single link %d/%d = %v %v", row.route, row.clears, got, err)
		}
	}
	for _, row := range []struct {
		route  byte
		clears int
	}{{0, -1}, {0, 2}, {1, 2}, {2, 0}} {
		if _, err := LoadSingleRaidSequence(archive, row.route, row.clears); err == nil {
			t.Fatalf("missing/unsupported single link was accepted: %+v", row)
		}
	}
}

func TestBattleConfigurationRejectsIncompleteRepeatedAndUnsupportedPhaseData(t *testing.T) {
	base := "[PHASE MAX]\n1\n[START DELAY TIME]\n3\n[WEEKLY CLEAR COUNT]\n2\n[PHASE TIME OVER]\n2400\n[PHASE]\n[DUNGEON INFO]\n[INDEX]\n210\n[INIT STATE]\n`open`\n[/DUNGEON INFO]\n[/PHASE]\n"
	for _, body := range []string{
		base[:len(base)-10],
		base + "[PHASE]\n[/PHASE]\n",
		"[PHASE MAX]\n1\n[START DELAY TIME]\n3\n[WEEKLY CLEAR COUNT]\n2\n[PHASE TIME OVER]\n2400\n[PHASE]\n[DUNGEON INFO]\n[INDEX]\n210\n[/DUNGEON INFO]\n[/PHASE]\n",
	} {
		if _, err := LoadBattleConfiguration(admissionSource{"x": body}, "x"); err == nil {
			t.Fatal("invalid phase data accepted")
		}
	}
	if _, err := LoadBattleConfiguration(admissionSource{"x": base}, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildDungeonStateBody([]DungeonState{{ID: 210}, {ID: 210}}, 0); err == nil {
		t.Fatal("duplicate dungeon state accepted")
	}
}
