package raid

import (
	"fmt"
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func realLukeProgram(t *testing.T) *LukeProgram {
	t.Helper()
	ref := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if ref == "" {
		t.Skip("requires actual production PVF")
	}
	a, err := platformpvf.Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadLukeProgram(a)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func completeLukeFirst(t *testing.T, p *LukeProgram, now time.Time, first uint32) *LukeProgression {
	t.Helper()
	s, err := p.BeginPhase(now, 82811)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClearDungeon("hidden", 5055, s.Epoch(), now); err == nil {
		t.Fatal("hidden sanctum accepted")
	}
	old := s
	s, replay, err := s.EnterDungeon("birth-enter", 5054, s.Epoch(), now)
	if err != nil || replay || s.dungeons[5055] != 0 || s.dungeons[5056] != 0 || old.dungeons[5055] != 2 {
		t.Fatalf("birth entry unlock: %v %v", err, s.Dungeons())
	}
	copy, replay, err := s.EnterDungeon("birth-enter", 5054, s.Epoch(), now)
	if err != nil || !replay || copy.SymbolSequence() != s.SymbolSequence() {
		t.Fatal("entry replay changed graph")
	}
	order := []uint32{first, 5054, 5055 + 5056 - first}
	for _, id := range order {
		for n := 1; n <= 3; n++ {
			before := s
			run := fmt.Sprintf("%d-%d", id, n)
			if id == 5054 && n == 1 {
				run = "birth-enter"
			} else {
				s, _, err = s.EnterDungeon(run, id, s.Epoch(), now)
				if err != nil {
					t.Fatal(err)
				}
			}
			s, replay, err = s.ClearDungeon(run, id, s.Epoch(), now)
			if err != nil || replay {
				t.Fatalf("sanctum clear %d/%d: %v", id, n, err)
			}
			if before.ClearCount(id) != uint32(n-1) {
				t.Fatal("immutable snapshot changed")
			}
			if n < 3 && s.dungeons[id] != 0 {
				t.Fatal("sanctum closed before three actual clears")
			}
		}
	}
	if !s.PhaseCleared() || s.PhaseFailed() {
		t.Fatal("all three sanctums did not complete phase")
	}
	want := uint32(1)
	if first == 5056 {
		want = 2
	}
	if s.symbols["2PHASE DUNGEON TYPE"] != want {
		t.Fatal("first completed sanctum route was not frozen")
	}
	return s
}

func TestRealPVFLukeBothRoutesCompleteTwoPhases(t *testing.T) {
	p := realLukeProgram(t)
	now := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
	for _, first := range []uint32{5055, 5056} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			s := completeLukeFirst(t, p, now, first)
			var err error
			s, err = s.NextPhase(now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			now2 := now.Add(time.Minute)
			if s.dungeons[5311] != 0 || s.dungeons[5313] != 0 || s.dungeons[5057] != 1 || s.dungeons[5061] != 2 {
				t.Fatal("actual phase-two initial graph missing")
			}
			if s.timers[AntonTimerKey{1, 5054}].Duration != 10 {
				t.Fatal("actual Luke sanctum timer missing")
			}
			for _, id := range []uint32{5311, 5313, 5057, 5058, 5059, 5060, 5064, 5065, 5061, 5062} {
				if s.dungeons[id] != 0 {
					t.Fatalf("next dungeon %d not opened: %v", id, s.Dungeons())
				}
				old := s
				run := fmt.Sprint("phase2-", id)
				s, _, err = s.EnterDungeon(run, id, s.Epoch(), now2)
				if err != nil {
					t.Fatal(err)
				}
				next, replay, e := s.ClearDungeon(run, id, s.Epoch(), now2)
				if e != nil || replay {
					t.Fatalf("phase2 %d: %v", id, e)
				}
				s = next
				if old.PhaseCleared() {
					t.Fatal("old snapshot completed")
				}
			}
			if !s.PhaseCleared() || s.PhaseFailed() || len(s.Timers()) != 0 {
				t.Fatal("actual dark throne clear did not complete raid")
			}
		})
	}
}

func TestRealPVFLukeFailureTimerAndOwnedEntry(t *testing.T) {
	p := realLukeProgram(t)
	now := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
	s, err := p.BeginPhase(now, 90991)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextPhase(now); err == nil {
		t.Fatal("unfinished phase advanced")
	}
	if _, _, err := s.EnterDungeon("bad-epoch", 5054, 2, now); err == nil {
		t.Fatal("foreign attempt entered")
	}
	if _, err := s.GiveUpDungeon("foreign", 5054, now); err == nil {
		t.Fatal("unowned giveup accepted")
	}
	failed, err := s.EndPhase(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !failed.PhaseFailed() || failed.PhaseCleared() || s.PhaseFailed() {
		t.Fatal("phase expiration manufactured success or changed prior snapshot")
	}
	if _, _, err := failed.ClearDungeon("late", 5054, failed.Epoch(), now.Add(2*time.Second)); err == nil {
		t.Fatal("failed raid accepted late clear")
	}
	s = completeLukeFirst(t, p, now, 5055)
	s, err = s.NextPhase(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	for _, id := range []uint32{5311, 5313} {
		s, _, err = s.EnterDungeon(fmt.Sprint(id), id, s.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
		s, _, err = s.ClearDungeon(fmt.Sprint(id), id, s.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
	}
	s, _, err = s.EnterDungeon("energy-control", 5063, s.Epoch(), now)
	if err != nil {
		t.Fatal(err)
	}
	s, _, err = s.ClearDungeon("energy-control", 5063, s.Epoch(), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.dungeons[5063] != 1 || s.symbols["ENERGY CONTROL EFFECT"] != 0 || s.timers[AntonTimerKey{2, 5063}].Duration != 45 {
		t.Fatal("energy control cooldown differs from actual PVF")
	}
	s, err = s.Tick(now.Add(44 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.dungeons[5063] != 1 {
		t.Fatal("energy control reopened early")
	}
	s, err = s.Tick(now.Add(45 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.dungeons[5063] != 0 || s.symbols["ENERGY CONTROL EFFECT"] != 1 {
		t.Fatal("energy control did not reopen at configured deadline")
	}
	if _, err := s.Tick(now); err == nil {
		t.Fatal("backward clock accepted")
	}
}

func TestRealPVFLukePartyReservationAndAbandonment(t *testing.T) {
	p := realLukeProgram(t)
	now := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
	s, err := p.BeginPhase(now, 8114)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ReserveDungeon("party-a", 5054, s.Epoch(), now)
	if err != nil || !s.SameProjection(r.AntonProgression) {
		t.Fatal("reservation emitted entry actions", err)
	}
	if _, err := r.ReserveDungeon("party-b", 5054, r.Epoch(), now); err == nil {
		t.Fatal("simultaneous parties exceeded actual PARTY MAX")
	}
	if _, _, err := r.EnterDungeon("party-b", 5054, r.Epoch(), now); err == nil {
		t.Fatal("unreserved party bypassed reservation")
	}
	r, err = r.CancelReservation("party-a", 5054)
	if err != nil {
		t.Fatal(err)
	}
	r, err = r.ReserveDungeon("party-b", 5054, r.Epoch(), now)
	if err != nil {
		t.Fatal("failed preflight did not release slot", err)
	}
	r, _, err = r.EnterDungeon("party-b", 5054, r.Epoch(), now)
	if err != nil {
		t.Fatal(err)
	}
	if r.dungeons[5055] != 0 || r.dungeons[5056] != 0 {
		t.Fatal("committed birth entry did not open sanctums")
	}
	r, err = r.CancelReservation("party-b", 5054)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReserveDungeon("party-c", 5054, r.Epoch(), now); err == nil {
		t.Fatal("cancelling reservation released committed run")
	}
	r, err = r.GiveUpDungeon("party-b", 5054, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReserveDungeon("party-b", 5054, r.Epoch(), now); err == nil {
		t.Fatal("retired run reused")
	}
	if _, err := r.ReserveDungeon("party-c", 5054, r.Epoch(), now); err != nil {
		t.Fatal("abandoned party retained slot", err)
	}
}
