package raid

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealPVFAntonSymbolJournalPreservesTransientFailureAndRepeatedKill(t *testing.T) {
	program := realAntonProgram(t)
	now := time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)
	s, err := program.BeginPhase(0, now, 94123)
	if err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		for n := 0; n < 4; n++ {
			next, _, err := s.ClearDungeon(string(rune('a'+cycle*4+n)), 210, s.Epoch(), now)
			if err != nil {
				t.Fatal(err)
			}
			s = next
		}
		cursor := s.SymbolSequence()
		next, err := s.Tick(now.Add(301 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		writes, err := next.SymbolWritesAfter(cursor)
		if err != nil {
			t.Fatal(err)
		}
		failOn, failOff, kills := false, false, 0
		for _, w := range writes {
			if w.ID == 104 && w.Value == 1 {
				failOn = true
			}
			if w.ID == 104 && w.Value == 0 && failOn {
				failOff = true
			}
			if w.ID == 102 && w.Value == 1 {
				kills++
			}
		}
		if !failOn || !failOff || kills == 0 || next.NativeSymbols()[104] != 0 || next.NativeSymbols()[102] != 1 {
			t.Fatalf("cycle=%d transient journal lost: %+v", cycle, writes)
		}
		if s.SameProjection(next) || next.Epoch() != uint64(cycle+2) {
			t.Fatal("failure/reset projection missing")
		}
		t.Logf("cycle=%d writes=%d kill_callbacks=%d fail_1_then_0=true epoch=%d", cycle, len(writes), kills, next.Epoch())
		s = next
		now = now.Add(301 * time.Second)
	}
}

func TestRealPVFAntonSymbolJournalCloneGapAndIdleTick(t *testing.T) {
	s, err := realAntonProgram(t).BeginPhase(1, time.Now(), 93845)
	if err != nil {
		t.Fatal(err)
	}
	initial := s.SymbolSequence()
	if initial == 0 {
		t.Fatal("actual initial SET writes missing")
	}
	next, err := s.Tick(s.LastTime().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if next.SymbolSequence() != initial || !s.SameProjection(next) {
		t.Fatal("idle tick replayed SET effects")
	}
	if _, err := s.SymbolWritesAfter(initial + 1); err == nil {
		t.Fatal("future cursor accepted")
	}
	clone := s.Clone()
	for n := 0; n < AntonSymbolJournalLimit+5; n++ {
		clone.recordSymbolWrite("KILL ALL CHARACTER", 1)
	}
	if _, err := clone.SymbolWritesAfter(initial); err == nil {
		t.Fatal("journal gap silently skipped")
	}
	writes, err := clone.SymbolWritesAfter(clone.SymbolSequence() - 2)
	if err != nil || len(writes) != 2 || writes[0].ID != 102 || writes[1].ID != 102 {
		t.Fatal(writes, err)
	}
	writes[0].Value = 0
	if same, err := clone.SymbolWritesAfter(clone.SymbolSequence() - 2); err != nil || same[0].Value != 1 {
		t.Fatal("returned journal aliases owner")
	}
	if s.SymbolSequence() != initial {
		t.Fatal("clone journal aliases previous phase")
	}
}

func TestOrderedAntonSymbolBodyKeepsDuplicateIDsAndEmitsNativeFixture(t *testing.T) {
	entries := []SymbolValue{{104, 1}, {104, 0}, {102, 1}, {102, 1}}
	body, err := BuildOrderedSymbolBody(entries)
	if err != nil || len(body) != 33 || body[0] != 4 {
		t.Fatal(body, err)
	}
	if _, err := BuildOrderedSymbolBody([]SymbolValue{{0, 0}}); err == nil {
		t.Fatal("reset sentinel mixed into journal")
	}
	if _, err := BuildOrderedSymbolBody(make([]SymbolValue, 256)); err == nil {
		t.Fatal("overflow count accepted")
	}
	if out := os.Getenv("DNF90_ANTON_NATIVE_DIR"); out != "" {
		if err := os.MkdirAll(out, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "go-anton-ordered-symbols.bin"), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
