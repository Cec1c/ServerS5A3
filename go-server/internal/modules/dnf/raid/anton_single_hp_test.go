package raid

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFSingleAntonHPCatalogAndNormalPhaseIsolation(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_SINGLE_ANTON_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual single Anton catalog candidate PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := a.ReadText("etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	cut := strings.Index(text, "[SINGLE RAID PHASE]")
	if cut < 0 {
		t.Fatal("actual single block absent")
	}
	before, err := LoadBattleConfiguration(admissionSource{"etc/raid/anton.etc": text[:cut]}, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	after, err := LoadBattleConfiguration(a, "etc/raid/anton.etc")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("single block changed ordinary phases", before, after, err)
	}
	if _, err := LoadAntonProgram(a); err != nil {
		t.Fatal("actual normal Anton program changed", err)
	}
	bindings, err := LoadSingleAntonHPSymbols(a, after.WeeklyClearLimit)
	if err != nil || len(bindings) != 8 {
		t.Fatal("actual eight-map HP catalog", bindings, err)
	}
	for i := uint32(0); i < 8; i++ {
		if bindings[5314+i] != 64+i {
			t.Fatal("actual symbol token identity shifted", bindings)
		}
	}
	symbolText, err := a.ReadText("etc/raid/anton.etc.symbol")
	if err != nil {
		t.Fatal(err)
	}
	linkText, err := a.ReadText("etc/raid/singleraidlinksystem.etc")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "unclosed", "wrong_symbol", "wrong_threshold", "duplicate", "extra_map", "nested", "ordinary_error"} {
		t.Run(name, func(t *testing.T) {
			bad := text
			switch name {
			case "missing":
				bad = text[:cut]
			case "unclosed":
				bad = strings.Replace(text, "[/SINGLE RAID PHASE]", "", 1)
			case "wrong_symbol":
				bad = strings.Replace(text, "[5314 DGN HP]", "[5315 DGN HP]", 1)
			case "wrong_threshold":
				start := strings.Index(text, "[5314 DGN HP]") + len("[5314 DGN HP]")
				end := strings.Index(text[start:], "[PARTY MAX]") + start
				bad = text[:start] + "\n2\n" + text[end:]
			case "duplicate":
				bad = strings.Replace(text, "[/SINGLE RAID PHASE]", "[/SINGLE RAID PHASE]\n"+text[cut:], 1)
			case "extra_map":
				bad = strings.Replace(text[cut:], "5314", "900000", -1)
				bad = text[:cut] + bad
			case "nested":
				bad = strings.Replace(text, "[SINGLE RAID PHASE]", "[SINGLE RAID PHASE]\n[PHASE]", 1)
			case "ordinary_error":
				bad = strings.Replace(text, "[INDEX]", "[MISSING INDEX]", 1)
			}
			source := admissionSource{"etc/raid/anton.etc": bad, "etc/raid/anton.etc.symbol": symbolText, "etc/raid/singleraidlinksystem.etc": linkText}
			if name == "ordinary_error" {
				if _, err := LoadBattleConfiguration(source, "etc/raid/anton.etc"); err == nil {
					t.Fatal("ordinary malformed dungeon hidden by single block")
				}
				return
			}
			if _, err := LoadSingleAntonHPSymbols(source, 2); err == nil {
				t.Fatal("invalid real catalog mutation accepted", name)
			}
			if name == "unclosed" || name == "duplicate" || name == "nested" {
				if _, err := LoadBattleConfiguration(source, "etc/raid/anton.etc"); err == nil {
					t.Fatal("invalid single block boundary ignored")
				}
			}
		})
	}
	for weekly := 0; weekly < 2; weekly++ {
		sequence, err := LoadSingleRaidSequence(a, 0, weekly)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range sequence {
			if bindings[id] == 0 {
				t.Fatal("weekly link unmapped", strconv.Itoa(weekly), id)
			}
		}
	}
}
