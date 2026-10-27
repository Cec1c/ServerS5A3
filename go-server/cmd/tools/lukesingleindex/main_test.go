package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	script "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	archive "longheng.io/server/internal/platform/pvf"
)

type alteredSource struct {
	script.Source
	name, text string
}

func (s alteredSource) ReadText(name string) (string, error) {
	if name == s.name {
		return s.text, nil
	}
	return s.Source.ReadText(name)
}

func realSources(t *testing.T) (*archive.Archive, *archive.Archive) {
	t.Helper()
	input, ref := os.Getenv("LUKE_INPUT_PVF"), os.Getenv("LUKE_REFERENCE_PVF")
	if input == "" || ref == "" {
		t.Skip("requires pinned Luke input and upstream PVFs")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if digest(raw) != inputHash {
		t.Fatal("input hash mismatch")
	}
	original, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if digest(original) != referenceHash {
		t.Fatal("reference hash mismatch")
	}
	a, err := archive.OpenBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := archive.OpenBytes(original)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// LUKE_TEST_BASELINE=1 runs this exact postcondition on the unchanged input,
// yielding the historical absent-index failure instead of a synthetic assertion.
func TestRealLukeIndexRestoration(t *testing.T) {
	a, b := realSources(t)
	p, err := buildPlan(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 70 || fmt.Sprint(p.MissingDoorRange) != "[95310 95311]" {
		t.Fatalf("unexpected plan: %+v", p)
	}
	next := a
	if os.Getenv("LUKE_TEST_BASELINE") != "1" {
		data, err := a.RepackScriptAdditions(additions(p))
		if err != nil {
			t.Fatal(err)
		}
		next, err = archive.OpenBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.VerifyScriptReplacementPreservation(next, []string{"map/map.lst", "dungeon/dungeon.lst"}); err != nil {
			t.Fatal(err)
		}
	}
	for route := byte(0); route < 2; route++ {
		if _, err := raid.ValidateSingleRaidDependenciesForRoute(next, route); err != nil {
			t.Fatal(err)
		}
	}
	after, err := buildPlan(next, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Rows) != 0 {
		t.Fatal("restoration left missing rows")
	}
	if fmt.Sprint(after.MissingDoorRange) != "[95310 95311]" {
		t.Fatal("index repair changed unresolved door evidence")
	}
}

func TestRealLukeRejectsUnprovenChanges(t *testing.T) {
	a, b := realSources(t)
	maps, err := a.ReadText("map/map.lst")
	if err != nil {
		t.Fatal(err)
	}
	upMaps, err := b.ReadText("map/map.lst")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name              string
		current, upstream script.Source
		want              string
	}{
		{"conflicting binding", alteredSource{a, "map/map.lst", maps + " 95267 `luke_single/wrong.map`"}, b, "conflicting existing"},
		{"duplicate binding", alteredSource{a, "map/map.lst", maps + " 95267 `a.map` 95267 `b.map`"}, b, "duplicate index"},
		{"missing upstream", a, alteredSource{b, "map/map.lst", "1 `other.map`"}, "upstream binding absent"},
		{"guessed mapping", a, alteredSource{b, "map/map.lst", strings.Replace(upMaps, "Sanctum_of_Birth_L1.map", "invented.map", 1)}, ""},
		{"changed map", alteredSource{a, "map/luke_single/altar_of_dark/altar_of_dark3.map", "[dungeon] 5331"}, b, "upstream script differs"},
		{"changed maze", alteredSource{a, "dungeon/luke_single/sanctum_of_birth_light.dgn", "[name] `test`"}, b, "upstream maze differs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildPlan(c.current, c.upstream)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want refusal %q", err, c.want)
			}
		})
	}
}

func TestOutputCannotOverwriteInput(t *testing.T) {
	name := filepath.Join(t.TempDir(), "input.pvf")
	if err := os.WriteFile(name, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generate(name, "unused", name, filepath.Join(t.TempDir(), "report.json")); err == nil {
		t.Fatal("accepted input overwrite")
	}
	if err := writeNew(name, []byte("changed")); err == nil {
		t.Fatal("overwrote file")
	}
	got, err := os.ReadFile(name)
	if err != nil || string(got) != "preserve me" {
		t.Fatal("input changed")
	}
}
