package raid

import (
	"strings"
	"testing"
	"time"
)

func TestRealPVFAntonReconnectSnapshotOmitsMovieInstructions(t *testing.T) {
	p := realAntonProgram(t)
	for phase := 0; phase < 2; phase++ {
		s, err := p.BeginPhase(phase, time.Now(), 78117)
		if err != nil {
			t.Fatal(err)
		}
		before := s.NativeSymbols()
		got := s.ReconnectSymbols()
		for name, id := range p.symbols {
			if strings.HasPrefix(name, "MOVIE ") && id != 126 && id != 127 {
				if value, present := got[id]; present {
					t.Errorf("phase%d snapshot replays native movie callback %s id%d value%d", phase, name, id, value)
				}
			} else if id == 126 || id == 127 || name == "BLACK VOLCANO BARRIER" || name == "IS EXIST INFECTION DUNGEON" || strings.Contains(name, "DGN HP") {
				if value, present := got[id]; !present || value != before[id] {
					t.Errorf("phase%d persistent symbol lost %s", phase, name)
				}
			}
		}
		if s.NativeSymbols()[128] != before[128] {
			t.Fatal("snapshot mutated domain")
		}
	}
}

func TestRealPVFAntonMovieInstructionsRemainInOwnedLiveJournal(t *testing.T) {
	p := realAntonProgram(t)
	at := time.Now()
	s, err := p.BeginPhase(1, at, 78117)
	if err != nil {
		t.Fatal(err)
	}
	cursor := s.SymbolSequence()
	next, err := s.Tick(at.Add(180 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	writes, err := next.SymbolWritesAfter(cursor)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, w := range writes {
		if w.ID == p.symbols["MOVIE HATCHERY OPEN"] && w.Value == 1 {
			count++
		}
	}
	if count != 1 {
		t.Fatal("actual 180-second hatchery event lost its movie instruction", count)
	}
	if _, present := next.ReconnectSymbols()[128]; present {
		t.Fatal("restore replays past hatchery movie")
	}
}
