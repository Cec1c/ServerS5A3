package raid

import (
	"fmt"
	"testing"
	"time"
)

func TestRealPVFAntonVolcanoNativeEdgesSurviveRechargeAndClone(t *testing.T) {
	p := realAntonProgram(t)
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s, err := p.BeginPhase(1, at, 78117)
	if err != nil {
		t.Fatal(err)
	}
	check := func(state *AntonProgression, want uint32) {
		t.Helper()
		for name, values := range map[string]map[uint32]uint32{"native": state.NativeSymbols(), "restore": state.Clone().ReconnectSymbols()} {
			if got, ok := values[AntonVolcanoOpenSymbolID]; !ok || got != want {
				t.Fatalf("%s volcano state=%d present=%v want%d", name, got, ok, want)
			}
		}
	}
	check(s, 0)
	initial := s
	for i := 0; i < 4; i++ {
		s, _, err = s.ApplyBarrierDamage(fmt.Sprintf("volcano-native/%d", i), AntonLargeTransmission, 218, s.Epoch(), at)
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			check(s, 0) // Partial loss cannot release the barrier.
		}
	}
	check(initial, 0) // The accepted edge must not mutate previous state.
	check(s, 1)
	cursor := s.SymbolSequence()
	s, err = s.Tick(at.Add(time.Second))
	if err != nil || s.NativeSymbols()[110] != 17 {
		t.Fatal("first recharge", err, s.NativeSymbols())
	}
	check(s, 1)
	writes, err := s.SymbolWritesAfter(cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range writes {
		if write.ID == AntonVolcanoOpenSymbolID {
			t.Fatal("intermediate recharge rewrote the native edge", write)
		}
	}
	cursor = s.SymbolSequence()
	s, err = s.Tick(at.Add(700 * time.Second))
	if err != nil || s.NativeSymbols()[110] != 10000 {
		t.Fatal("full recharge", err)
	}
	check(s, 0)
	writes, err = s.SymbolWritesAfter(cursor)
	if err != nil {
		t.Fatal(err)
	}
	edges := 0
	for i, write := range writes {
		if write.ID == AntonVolcanoOpenSymbolID {
			edges++
			if write.Value != 0 || i == 0 || writes[i-1].ID != 110 || writes[i-1].Value != 10000 || i+1 >= len(writes) || writes[i+1].ID != 127 || writes[i+1].Value != 1 {
				t.Fatal("recovery native edge order", writes[max(0, i-1):min(len(writes), i+2)])
			}
		}
	}
	if edges != 1 {
		t.Fatal("full recharge must publish exactly one native closed edge", edges)
	}
	for i := 4; i < 8; i++ {
		s, _, err = s.ApplyBarrierDamage(fmt.Sprintf("volcano-native/%d", i), AntonLargeTransmission, 218, s.Epoch(), at.Add(700*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s, 1) // A second complete depletion releases again.
}

func TestRealPVFAntonVolcanoNativeStateExcludedFromPhaseOne(t *testing.T) {
	s, err := realAntonProgram(t).BeginPhase(0, time.Now(), 78117)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := s.NativeSymbols()[AntonVolcanoOpenSymbolID]; exists {
		t.Fatal("phase one published a phase-two volcano state")
	}
	if _, exists := s.ReconnectSymbols()[AntonVolcanoOpenSymbolID]; exists {
		t.Fatal("phase-one restore published a phase-two volcano state")
	}
	luke, err := realLukeProgram(t).BeginPhase(time.Now(), 78117)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := luke.NativeSymbols()[AntonVolcanoOpenSymbolID]; exists {
		t.Fatal("Luke published an Anton-only native state")
	}
}
