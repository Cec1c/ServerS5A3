package raid

import (
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReviewActualLukeFirstPhaseGeneratorTierSurvivesPhaseTwo(t *testing.T) {
	p := realLukeProgram(t)
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		total, tier uint32
		seconds     uint32
	}{{0, 0, 45}, {5, 0, 45}, {6, 1, 90}, {10, 1, 90}, {11, 2, 120}, {15, 2, 120}} {
		t.Run(fmt.Sprint(tc.total), func(t *testing.T) {
			s := completeLukeFirst(t, p, now, 5055)
			// Authoritative count fixture isolates transition semantics; public bridge
			// acceptance is separately tested against actual indexed room machinery.
			left := tc.total
			for _, name := range []string{"BIRTH SANCTUM OBJECT", "EXTINCTION SANCTUM OBJECT", "SPREAD SANCTUM OBJECT"} {
				n := left
				if n > 5 {
					n = 5
				}
				s.symbols[name] = n
				left -= n
			}
			next, e := s.NextPhase(now)
			if e != nil {
				t.Fatal(e)
			}
			if next.NativeSymbols()[133] != tc.tier {
				t.Fatalf("sum%d tier=%d expected%d", tc.total, next.NativeSymbols()[133], tc.tier)
			}
			for _, id := range []uint32{5311, 5313} {
				next, _, e = next.EnterDungeon(fmt.Sprint(id), id, next.Epoch(), now)
				if e != nil {
					t.Fatal(e)
				}
				next, _, e = next.ClearDungeon(fmt.Sprint(id), id, next.Epoch(), now)
				if e != nil {
					t.Fatal(e)
				}
			}
			next, _, e = next.EnterDungeon("energy-control", 5063, next.Epoch(), now)
			if e != nil {
				t.Fatal(e)
			}
			next, _, e = next.ClearDungeon("energy-control", 5063, next.Epoch(), now)
			if e != nil {
				t.Fatal(e)
			}
			if got := next.timers[AntonTimerKey{2, 5063}].Duration; got != tc.seconds {
				t.Fatalf("tier%d cooldown%d expected%d", tc.tier, got, tc.seconds)
			}
		})
	}
}
func TestReviewActualLukeGeneratorCreditIsImmutableBoundedAndOwned(t *testing.T) {
	p := realLukeProgram(t)
	now := time.Unix(1800000000, 0)
	s, e := p.BeginPhase(now, 82811)
	if e != nil {
		t.Fatal(e)
	}
	s, _, e = s.EnterDungeon("birth", 5054, s.Epoch(), now)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []struct {
		run   string
		id    uint32
		epoch uint64
		at    time.Time
	}{{"foreign", 5054, s.Epoch(), now}, {"birth", 5055, s.Epoch(), now}, {"birth", 5054, s.Epoch() + 1, now}, {"birth", 5054, s.Epoch(), now.Add(-time.Second)}} {
		if _, _, e = s.CreditSanctumGenerator(bad.run, "bad", bad.id, bad.epoch, bad.at); e == nil {
			t.Fatal("foreign or backward credit accepted")
		}
	}
	for n := 0; n < 5; n++ {
		old := s
		var replay bool
		s, replay, e = s.CreditSanctumGenerator("birth", fmt.Sprint(n), 5054, s.Epoch(), now)
		if e != nil || replay {
			t.Fatal(e, replay)
		}
		if old.NativeSymbols()[117] != uint32(n) || s.NativeSymbols()[117] != uint32(n+1) {
			t.Fatal("credit modified old snapshot or wrong counter")
		}
		copy, replay, e := s.CreditSanctumGenerator("birth", fmt.Sprint(n), 5054, s.Epoch(), now)
		if e != nil || !replay || copy.NativeSymbols()[117] != s.NativeSymbols()[117] {
			t.Fatal("duplicate changed credit", e)
		}
	}
	capped, replay, e := s.CreditSanctumGenerator("birth", "overflow", 5054, s.Epoch(), now)
	if e != nil || !replay || capped.NativeSymbols()[117] != 5 {
		t.Fatal("cap exceeded", e)
	}
}

type reviewLukeGeneratorSource struct {
	dnfpvf.Source
	change func(string, string) (string, error)
}

func (s reviewLukeGeneratorSource) ReadText(ref string) (string, error) {
	v, e := s.Source.ReadText(ref)
	if e != nil {
		return "", e
	}
	return s.change(strings.ToLower(ref), v)
}
func TestReviewActualLukeGeneratorMalformedResourcesFailClosed(t *testing.T) {
	ref := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if ref == "" {
		t.Skip("requires actual production PVF")
	}
	a, e := platformpvf.Open(ref)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		change func(string, string) (string, error)
	}{
		{"zero-budget", func(ref, v string) (string, error) {
			if ref == "etc/raid/luke/luke.etc" {
				v = strings.Replace(v, "[BIRTH SANCTUM]\n5 5054", "[BIRTH SANCTUM]\n0 5054", 1)
			}
			return v, nil
		}},
		{"tier-gap", func(ref, v string) (string, error) {
			if ref == "etc/raid/luke/luke.etc" {
				v = strings.Replace(v, "0 0 5 1 6 10 2 11 15", "0 0 5 1 7 10 2 11 15", 1)
			}
			return v, nil
		}},
		{"tier-missing-coverage", func(ref, v string) (string, error) {
			if ref == "etc/raid/luke/luke.etc" {
				v = strings.Replace(v, "0 0 5 1 6 10 2 11 15", "0 0 5 1 6 10 2 11 14", 1)
			}
			return v, nil
		}},
		{"missing-indexed-machinery", func(ref, v string) (string, error) {
			if ref == "passiveobject/passiveobject.lst" {
				return "", nil
			}
			return v, nil
		}},
		{"missing-essential-action", func(ref, v string) (string, error) {
			if strings.HasSuffix(ref, "luke_mainmchine_destroy_raid.act") {
				return "", fmt.Errorf("injected essential action read failure")
			}
			return v, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := LoadLukeProgram(reviewLukeGeneratorSource{a, tc.change}); e == nil {
				t.Fatal("malformed actual resource accepted")
			}
		})
	}
}

func TestReviewActualLukeGeneratorIndexedOwnersExcludeRearVisualAndNormalMaps(t *testing.T) {
	p := realLukeProgram(t)
	s, e := p.BeginPhase(time.Unix(1800000000, 0), 123)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		d uint32
		o int64
	}{{5054, 64249}, {5054, 64250}, {5055, 64251}, {5056, 64252}} {
		if !s.SanctumGeneratorOwner(tc.d, tc.o) {
			t.Fatalf("actual indexed machinery%d missing owner%d", tc.o, tc.d)
		}
		for _, foreign := range []uint32{5054, 5055, 5056} {
			if foreign != tc.d && s.SanctumGeneratorOwner(foreign, tc.o) {
				t.Fatal("foreign sanctum adopted machinery")
			}
		}
	}
	for _, o := range []int64{64030, 64067, 64068, 64287} {
		for _, d := range []uint32{5054, 5055, 5056} {
			if s.SanctumGeneratorOwner(d, o) {
				t.Fatalf("normal or rear visual object%d granted credit", o)
			}
		}
	}
}
