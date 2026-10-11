package raid

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAntonFlowTimingAndOwnedNextPhase(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadAntonFlowConfiguration(archive)
	if err != nil || config.PhaseBreakSeconds != 600 || config.MovieEndSeconds != [4]uint32{20, 20, 20, 20} || config.ShieldChargePerSecond != [2]uint32{17, 34} {
		t.Fatal("actual flow timing changed", config, err)
	}
	p := realAntonProgram(t)
	at := time.Now()
	first, err := p.BeginPhase(0, at, 19703)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.BeginNextPhase(at); err == nil {
		t.Fatal("uncompleted phase advanced")
	}
	for n, id := range []uint32{210, 210, 210, 210, 213, 213, 215, 215} {
		first = clearAnton(t, first, fmt.Sprint("next-phase-", n), id, at)
	}
	second, err := first.BeginNextPhase(at.Add(3 * time.Second))
	if err != nil || second.Phase() != 1 || second.PhaseCleared() || second.ClearCount(210) != 0 || second.SymbolSequence() == 0 || !second.LastTime().Equal(at.Add(3*time.Second)) {
		t.Fatal("next phase did not start from actual initialization", err)
	}
	if second.dungeons[218] != 0 || second.dungeons[219] != 0 || second.dungeons[220] != 2 || second.symbols["BLACK VOLCANO BARRIER"] != 10000 {
		t.Fatal("second phase lost actual initial maps/barrier", second.Dungeons(), second.symbols)
	}
	if !first.PhaseCleared() || first.Phase() != 0 || first.ClearCount(213) != 2 {
		t.Fatal("old phase snapshot mutated")
	}
	if _, err := first.BeginNextPhase(at.Add(-time.Second)); err == nil {
		t.Fatal("phase time moved backwards")
	}
	if _, err := second.BeginNextPhase(at.Add(4 * time.Second)); err == nil {
		t.Fatal("second phase skipped terminal flow")
	}
}

func TestAntonFlowConfigurationRejectsMissingAndInvalidTiming(t *testing.T) {
	base := "[PHASE BREAK TIME]\n600\n[MOVIE END TIME]\n20 20 20 20\n[SHIELD CHARGE RATE]\n17 34\n"
	for _, text := range []string{"", strings.Replace(base, "600", "0", 1), strings.Replace(base, "600", "4294967296", 1), strings.Replace(base, "20 20 20 20", "20 20 20", 1), strings.Replace(base, "20 20 20 20", "20 0 20 20", 1), strings.Replace(base, "17 34", "17", 1), strings.Replace(base, "17 34", "17 10001", 1)} {
		if _, err := LoadAntonFlowConfiguration(admissionSource{"etc/raid/anton.etc": text}); err == nil {
			t.Fatal("invalid flow dependency accepted", text)
		}
	}
}
