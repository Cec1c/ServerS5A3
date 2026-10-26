package raid

import (
	"fmt"
	"testing"
	"time"
)

func TestRealPVFAntonR180FixedHatcheriesAndInfection(t *testing.T) {
	p := realAntonProgram(t)
	base := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	infected := map[uint32]bool{}
	for seed := uint64(1); seed <= 64; seed++ {
		s, err := p.BeginPhase(1, base, seed)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []uint32{221, 222, 223, 224} {
			if s.dungeons[id] == 0 {
				t.Fatalf("seed %d: hatchery %d opened before timer", seed, id)
			}
		}
		s, err = s.Tick(base.Add(180 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if s.dungeons[221] != 2 {
			t.Fatalf("seed %d: first hatchery state %d", seed, s.dungeons[221])
		}
		for _, id := range []uint32{222, 223, 224} {
			if s.dungeons[id] != 0 {
				t.Fatalf("seed %d: hatchery %d not open", seed, id)
			}
		}
		if s.InfectionDungeon() < 222 || s.InfectionDungeon() > 224 {
			t.Fatal("infection outside allowed hatcheries", s.InfectionDungeon())
		}
		infected[s.InfectionDungeon()] = true
		if _, _, err := s.ClearDungeon("disabled-first", 221, s.Epoch(), base.Add(181*time.Second)); err == nil {
			t.Fatal("disabled first hatchery accepted clear")
		}
	}
	if len(infected) != 3 {
		t.Fatal("infection selection lost an allowed hatchery", infected)
	}
}

func TestRealPVFAntonR180AllowedHatcheriesRecover(t *testing.T) {
	p := realAntonProgram(t)
	base := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	s, err := p.BeginPhase(1, base, 812735)
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.Tick(base.Add(180 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{222, 223, 224} {
		s = clearAnton(t, s, fmt.Sprintf("r180-%d", id), id, base.Add(181*time.Second))
		if s.dungeons[id] != 1 {
			t.Fatal("cleared hatchery did not close", id)
		}
	}
	s, err = s.Tick(base.Add(421 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{222, 223, 224} {
		if s.dungeons[id] != 0 {
			t.Fatal("allowed hatchery did not recover", id)
		}
	}
	if s.dungeons[221] != 2 {
		t.Fatal("first hatchery reopened")
	}
	// Even an explicit delayed open directive must respect the operator policy.
	var events []antonEvent
	s.setDungeon(221, 0, &events)
	if s.dungeons[221] != 2 || len(events) != 0 {
		t.Fatal("disabled hatchery accepted reopen directive")
	}
}
