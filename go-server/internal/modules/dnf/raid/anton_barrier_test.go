package raid

import (
	"fmt"
	"testing"
	"time"
)

func TestRealPVFAntonBarrierActualAmountsChargeAndTransientMovies(t *testing.T) {
	p := realAntonProgram(t)
	c := p.barrier
	if c.Charge != [2]uint32{17, 34} || c.Small != 100 || c.Large != 3000 || c.Constructor != 1000 || c.InfectedConstructor != 500 {
		t.Fatal("actual shield actions", c)
	}
	base := time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC)
	s, err := p.BeginPhase(1, base, 89102)
	if err != nil {
		t.Fatal(err)
	}
	initialCursor := s.SymbolSequence()
	for n := 0; n < 4; n++ {
		next, replay, err := s.ApplyBarrierDamage(fmt.Sprintf("energy/run/room/object%d", n), AntonLargeTransmission, 218, s.Epoch(), base)
		if err != nil || replay {
			t.Fatal(n, replay, err)
		}
		if n == 0 && s.NativeSymbols()[110] != 10000 {
			t.Fatal("damage mutated previous snapshot")
		}
		s = next
	}
	if s.NativeSymbols()[110] != 0 {
		t.Fatal("shield subtraction wrapped", s.NativeSymbols()[110])
	}
	writes, err := s.SymbolWritesAfter(s.SymbolSequence() - 3)
	if err != nil || len(writes) != 3 || writes[0].ID != 110 || writes[0].Value != 0 || writes[1].ID != AntonVolcanoOpenSymbolID || writes[1].Value != 1 || writes[2].ID != 126 || writes[2].Value != 1 {
		t.Fatal("break movie journal", writes, err)
	}
	replay, replayed, err := s.ApplyBarrierDamage("energy/run/room/object3", AntonLargeTransmission, 218, s.Epoch(), base)
	if err != nil || !replayed || replay.SymbolSequence() != s.SymbolSequence() {
		t.Fatal("duplicate accepted event", err)
	}
	s, err = s.Tick(base.Add(time.Second))
	if err != nil || s.NativeSymbols()[110] != 17 {
		t.Fatal("normal one-second recovery", err, s.NativeSymbols()[110])
	}
	s, err = s.Tick(base.Add(180 * time.Second))
	if err != nil || s.NativeSymbols()[110] != 3060 || s.NativeSymbols()[7] != 1 {
		t.Fatal("ordered infection boundary", err, s.NativeSymbols())
	}
	s, err = s.Tick(base.Add(181 * time.Second))
	if err != nil || s.NativeSymbols()[110] != 3094 {
		t.Fatal("infected recovery must use symbol 7 and rate 34", err, s.NativeSymbols()[110])
	}
	infected := s.InfectionDungeon()
	s, replayed, err = s.ApplyBarrierDamage("hatch/run/room/constructor", AntonHatcheryConstructor, infected, s.Epoch(), base.Add(181*time.Second))
	if err != nil || replayed || s.NativeSymbols()[110] != 1594 {
		t.Fatal("actual infected constructor loss", err, s.NativeSymbols()[110])
	}
	s, err = s.Tick(base.Add(700 * time.Second))
	if err != nil || s.NativeSymbols()[110] != 10000 {
		t.Fatal("delayed recovery clamp", err, s.NativeSymbols()[110])
	}
	journal, err := s.SymbolWritesAfter(initialCursor)
	if err != nil {
		t.Fatal(err)
	}
	recoverOn, recoverOff := 0, 0
	for _, w := range journal {
		if w.ID == 127 {
			if w.Value == 1 {
				recoverOn++
			}
			if w.Value == 0 {
				recoverOff++
			}
		}
	}
	if recoverOn != 1 || recoverOff != 0 {
		t.Fatal("recovery movie not exactly one formal ON CHANGE write", recoverOn, recoverOff)
	}
	clone := s.Clone()
	delete(clone.barrierEvents, "energy/run/room/object3")
	if !s.barrierEvents["energy/run/room/object3"] {
		t.Fatal("clone aliases damage ledger")
	}
	stable := s.SymbolSequence()
	s, err = s.Tick(base.Add(701 * time.Second))
	if err != nil || s.SymbolSequence() != stable {
		t.Fatal("full shield repeated movie or symbol", err)
	}
}

func TestRealPVFAntonBarrierRejectsUnownedOrStaleSources(t *testing.T) {
	p := realAntonProgram(t)
	base := time.Now()
	s, err := p.BeginPhase(1, base, 19871)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		kind  AntonBarrierSource
		id    uint32
		epoch uint64
		at    time.Time
	}{
		{"unknown", 9, 218, 1, base}, {"small_in_volcano", AntonSmallTransmission, 219, 1, base},
		{"constructor_in_energy", AntonHatcheryConstructor, 218, 1, base}, {"closed_hatch", AntonHatcheryConstructor, 221, 1, base},
		{"old_attempt", AntonLargeTransmission, 218, 2, base}, {"backwards", AntonLargeTransmission, 218, 1, base.Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sequence := s.SymbolSequence()
			next, _, err := s.ApplyBarrierDamage(tc.name, tc.kind, tc.id, tc.epoch, tc.at)
			if err == nil || next != nil || s.SymbolSequence() != sequence || len(s.barrierEvents) != 0 {
				t.Fatal("invalid event changed state", err)
			}
		})
	}
	first, err := p.BeginPhase(0, base, 19871)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.ApplyBarrierDamage("phase0", AntonLargeTransmission, 218, 1, base); err == nil {
		t.Fatal("first phase accepted shield event")
	}
	cleared := clearAnton(t, s, "volcano", 219, base)
	if _, _, err := cleared.ApplyBarrierDamage("hidden_energy", AntonLargeTransmission, 218, 1, base); err == nil {
		t.Fatal("hidden energy accepted damage")
	}
}
