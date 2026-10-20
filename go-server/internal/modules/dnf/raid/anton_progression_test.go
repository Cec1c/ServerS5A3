package raid

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func realAntonProgram(t *testing.T) *AntonProgram {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func clearAnton(t *testing.T, s *AntonProgression, run string, id uint32, now time.Time) *AntonProgression {
	t.Helper()
	next, replayed, err := s.ClearDungeon(run, id, s.Epoch(), now)
	if err != nil || replayed {
		t.Fatalf("clear %s/%d: replay=%v error=%v", run, id, replayed, err)
	}
	return next
}

func TestRealPVFAntonTwoPhaseClearThresholdsAndReplay(t *testing.T) {
	p := realAntonProgram(t)
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	s, err := p.BeginPhase(0, now, 812735)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClearDungeon("closed-leg", 213, s.Epoch(), now); err == nil {
		t.Fatal("hidden leg accepted")
	}
	for i := 1; i <= 4; i++ {
		old := s
		s = clearAnton(t, s, fmt.Sprintf("smoke-%d", i), 210, now)
		if old.ClearCount(210) != uint32(i-1) {
			t.Fatal("previous immutable snapshot mutated")
		}
		if i < 4 && s.dungeons[213] != 2 {
			t.Fatal("legs opened before four smoke clears")
		}
	}
	if s.dungeons[210] != 2 || s.dungeons[211] != 1 || s.dungeons[213] != 0 || s.dungeons[216] != 2 {
		t.Fatalf("smoke transition: %v", s.Dungeons())
	}
	if s.symbols["DGN SMOKE OPEN"] != 1 || s.timers[AntonTimerKey{2, 211}].Duration != 300 {
		t.Fatal("PVF smoke symbols/timer missing")
	}
	s = clearAnton(t, s, "leg-a-1", 213, now)
	if s.dungeons[213] != 0 || s.ClearCount(213) != 1 {
		t.Fatal("first leg clear exhausted two-clear dungeon")
	}
	copy, replayed, err := s.ClearDungeon("leg-a-1", 213, s.Epoch(), now)
	if err != nil || !replayed || copy.ClearCount(213) != 1 {
		t.Fatal("duplicate run advanced leg")
	}
	s = clearAnton(t, s, "leg-a-2", 213, now)
	if s.PhaseCleared() {
		t.Fatal("only one leg completed phase")
	}
	s = clearAnton(t, s, "leg-b-1", 215, now)
	s = clearAnton(t, s, "leg-b-2", 215, now)
	if !s.PhaseCleared() || len(s.Timers()) != 0 {
		t.Fatal("both two-clear legs did not finish phase")
	}
	for _, d := range s.Dungeons() {
		if d.State != 2 {
			t.Fatal("phase-clear left selectable map", d)
		}
	}
	if _, _, err := s.ClearDungeon("late", 210, s.Epoch(), now); err == nil {
		t.Fatal("phase-break accepted clear")
	}

	s, err = p.BeginPhase(1, now, 913749)
	if err != nil {
		t.Fatal(err)
	}
	if s.symbols["BLACK VOLCANO BARRIER"] != 10000 || s.symbols["2PHASE INIT"] != 1 {
		t.Fatal("phase-two PVF initialization missing")
	}
	s = clearAnton(t, s, "energy-1", 218, now)
	if s.dungeons[218] != 0 || s.ClearCount(218) != 0 {
		t.Fatal("energy repeat did not reset")
	}
	s, err = s.Tick(now.Add(180 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, id := range p.hatcheries {
		if s.dungeons[id] == 0 {
			open++
		}
	}
	if open != 3 || s.dungeons[s.InfectionDungeon()] != 0 {
		t.Fatal("PVF random hatchery selection invalid", s.Dungeons(), s.InfectionDungeon())
	}
	s = clearAnton(t, s, "volcano", 219, now.Add(181*time.Second))
	if s.dungeons[220] != 0 || s.dungeons[218] != 2 || s.dungeons[219] != 2 {
		t.Fatal("heart unlock missing")
	}
	for i := 1; i <= 5; i++ {
		s = clearAnton(t, s, fmt.Sprintf("heart-%d", i), 220, now.Add(181*time.Second))
		if i < 5 && s.PhaseCleared() {
			t.Fatal("heart finished before five clears")
		}
	}
	if !s.PhaseCleared() || s.ClearCount(220) != 5 {
		t.Fatal("five heart clears did not finish raid")
	}
}

func TestRealPVFAntonTimerRecoveryResetAndDelayedTick(t *testing.T) {
	p := realAntonProgram(t)
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	s, err := p.BeginPhase(0, now, 7823721)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		s = clearAnton(t, s, fmt.Sprintf("smoke%d", i), 210, now)
	}
	s, err = s.Tick(now.Add(20 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.dungeons[216] != 0 || s.timers[AntonTimerKey{1, 216}].Duration != 360 {
		t.Fatal("PVF reserved cannon did not open at 20s")
	}
	s = clearAnton(t, s, "quake", 212, now.Add(25*time.Second))
	if s.dungeons[212] != 1 {
		t.Fatal("quake clear not closed")
	}
	s, err = s.Tick(now.Add(175 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.dungeons[212] != 0 || s.ClearCount(212) != 0 || !s.timers[AntonTimerKey{1, 212}].End.Equal(now.Add(475*time.Second)) {
		t.Fatal("quake recovery failed", s.Timers())
	}
	// Other quake times out at 300s, resetting smoke and every leg counter.
	s = clearAnton(t, s, "partial-leg", 213, now.Add(176*time.Second))
	previousEpoch := s.Epoch()
	s, err = s.Tick(now.Add(301 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.dungeons[210] != 0 || s.dungeons[213] != 2 || s.ClearCount(210) != 0 || s.ClearCount(213) != 0 {
		t.Fatal("quake timeout did not reset phase", s.Dungeons(), s.clears)
	}
	if len(s.timers) != 0 || s.symbols["DGN SMOKE OPEN"] != 0 {
		t.Fatal("reset retained expired timers")
	}
	if s.Epoch() == previousEpoch {
		t.Fatal("reset did not invalidate frozen entries")
	}
	if _, _, err := s.ClearDungeon("stale-smoke-run", 210, previousEpoch, now.Add(302*time.Second)); err == nil {
		t.Fatal("old entry advanced restarted smoke")
	}
	if _, err := s.Tick(now); err == nil {
		t.Fatal("backwards clock accepted")
	}

	s, err = p.BeginPhase(1, now, 827731)
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.Tick(now.Add(300 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	id := s.InfectionDungeon()
	if s.symbols[fmt.Sprintf("%d HATCHERY FAIL", id)] != 1 || s.timers[AntonTimerKey{1, id}].Duration != 45 || s.timers[AntonTimerKey{3, id}].Duration != 50 {
		t.Fatal("delayed tick skipped PVF effect timers", s.symbols, s.timers)
	}
	s = clearAnton(t, s, "hatchery", id, now.Add(301*time.Second))
	if s.dungeons[id] != 1 {
		t.Fatal("hatchery clear not closed")
	}
	if s.symbols["IS EXIST INFECTION DUNGEON"] != 0 || s.symbols[fmt.Sprintf("%d HATCHERY FAIL", id)] != 0 || s.symbols[fmt.Sprintf("%d HATCHERY NAMED CREATE", id)] != 0 {
		t.Fatal("cleared hatchery retained active effects")
	}
	s, err = s.Tick(now.Add(541 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.dungeons[id] != 0 || s.ClearCount(id) != 0 {
		t.Fatal("hatchery recovery failed")
	}
	if s.symbols["IS EXIST INFECTION DUNGEON"] != 1 {
		t.Fatal("recovered infection was not restored")
	}
}

func TestAntonProgramRejectsUnknownActionsAndForeignDungeon(t *testing.T) {
	p := realAntonProgram(t)
	_ = p
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText("etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	symbolText, err := archive.ReadText("etc/raid/anton.etc.symbol")
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(text, "[OPEN RANDOM HATCHERY]", "[UNKNOWN ACTION]", 1),
		strings.Replace(text, "216 `open` 20", "999999 `open` 20", 1),
		strings.Replace(text, "[210 DGN HP]\n4", "[210 DGN HP]\n0", 1),
	} {
		if changed == text {
			t.Fatal("mutation did not match actual PVF")
		}
		if _, err := LoadAntonProgram(admissionSource{"etc/raid/anton.etc": changed, "etc/raid/anton.etc.symbol": symbolText}); err == nil {
			t.Fatal("unsupported PVF program accepted")
		}
	}
}

func TestRealPVFAntonNativeSymbolTableAndProjection(t *testing.T) {
	p := realAntonProgram(t)
	for name, id := range map[string]uint32{"SMOKE PASSIVE CREATE": 1, "210 DGN HP": 50, "BLACK VOLCANO BARRIER": 110, "IS EXIST INFECTION DUNGEON": 7, "MOVIE HATCHERY OPEN": 128} {
		if p.symbols[name] != id {
			t.Fatal("actual PVF symbol ID shifted", name, p.symbols[name], id)
		}
	}
	s, err := p.BeginPhase(1, time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC), 765341)
	if err != nil {
		t.Fatal(err)
	}
	values := s.NativeSymbols()
	body, err := BuildSymbolBody(values)
	if err != nil {
		t.Fatal(err)
	}
	if int(body[0]) != len(p.symbols)+1 || len(body) != 1+8*(len(p.symbols)+1) {
		t.Fatal("native symbol projection shape")
	}
	seen := map[uint32]uint32{}
	for offset := 1; offset < len(body); offset += 8 {
		seen[binary.LittleEndian.Uint32(body[offset:])] = binary.LittleEndian.Uint32(body[offset+4:])
	}
	if seen[106] != 1 || seen[110] != 10000 || seen[127] != 1 || seen[50] != 0 {
		t.Fatal("native symbol projection lost actual phase values", seen)
	}
	if value, exists := seen[AntonVolcanoOpenSymbolID]; !exists || value != 0 {
		t.Fatal("native volcano initial closed state missing", seen)
	}
	for name, id := range p.symbols {
		if id == AntonVolcanoOpenSymbolID {
			t.Fatal("native-only volcano state was inserted into the formal PVF table", name)
		}
	}
	if dir := os.Getenv("DNF90_ANTON_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "phase2-symbols.bin"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := BuildSymbolBody(map[uint32]uint32{0: 0, 110: 10000}); err == nil {
		t.Fatal("reset sentinel mixed into ordinary symbols")
	}
}

func TestAntonSymbolsRejectsMalformedPairingAndDuplicates(t *testing.T) {
	for _, text := range []string{"[SMOKE]\n1", "1\n[A]\n1\n[B]", "1\n[A]\n2\n[A]", "0\n[A]", "1\n[A]\n2"} {
		if _, err := LoadAntonSymbols(admissionSource{"etc/raid/anton.etc.symbol": text}); err == nil {
			t.Fatal("invalid symbol file accepted", text)
		}
	}
}
