package pvf

import (
	"errors"
	"testing"
)

func TestVisitTextsGroupedPreservesRequestedIndicesAndErrors(t *testing.T) {
	a, paths, texts, _ := r195CacheMultiChunkFixture(t)
	inputs := []string{paths[3], paths[0], "missing.txt", paths[2], paths[0]}
	expected := []string{texts[3], texts[0], "", texts[2], texts[0]}
	got := make([]string, len(inputs))
	seen := make([]bool, len(inputs))
	a.VisitTextsGrouped(inputs, func(i int, text string, err error) {
		if i < 0 || i >= len(inputs) || seen[i] {
			t.Fatalf("invalid or repeated index %d", i)
		}
		seen[i] = true
		if i == 2 {
			if !errors.Is(err, ErrFileNotFound) {
				t.Fatalf("missing error: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		got[i] = text
	})
	for i := range inputs {
		if !seen[i] || got[i] != expected[i] {
			t.Fatalf("index %d got %q want %q", i, got[i], expected[i])
		}
	}
}
