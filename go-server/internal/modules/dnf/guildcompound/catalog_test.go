package guildcompound

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

type catalogTestSource struct {
	text string
	err  error
}

func (s catalogTestSource) ReadText(string) (string, error) {
	return s.text, s.err
}

const liveGuildCompoundCatalog = `[compound info]
0 4 2000 1 4 1500 2 4 750 3 2 0
[/compound info]

[guild flag compound info]
0 4 2500 1 4 2000 2 4 1000 3 4 500 6 2 0
[/guid flag compound info]
`

func TestLoadCompoundCatalogParsesOriginalPVF(t *testing.T) {
	catalog, err := LoadCompoundCatalog(catalogTestSource{text: liveGuildCompoundCatalog})
	if err != nil {
		t.Fatalf("LoadCompoundCatalog: %v", err)
	}

	tests := []struct {
		kind CompoundKind
		rows []CompoundRecipe
	}{
		{
			kind: CompoundGem,
			rows: []CompoundRecipe{
				{Rarity: 0, MaxMaterialCount: 4, RatePerMaterial: 2000},
				{Rarity: 1, MaxMaterialCount: 4, RatePerMaterial: 1500},
				{Rarity: 2, MaxMaterialCount: 4, RatePerMaterial: 750},
				{Rarity: 3, MaxMaterialCount: 2, RatePerMaterial: 0},
			},
		},
		{
			kind: CompoundFlag,
			rows: []CompoundRecipe{
				{Rarity: 0, MaxMaterialCount: 4, RatePerMaterial: 2500},
				{Rarity: 1, MaxMaterialCount: 4, RatePerMaterial: 2000},
				{Rarity: 2, MaxMaterialCount: 4, RatePerMaterial: 1000},
				{Rarity: 3, MaxMaterialCount: 4, RatePerMaterial: 500},
				{Rarity: 6, MaxMaterialCount: 2, RatePerMaterial: 0},
			},
		},
	}
	for _, test := range tests {
		ladder := catalog.Ladder(test.kind)
		for _, want := range test.rows {
			got, found := ladder.Recipe(want.Rarity)
			if !found || !reflect.DeepEqual(got, want) {
				t.Fatalf("%s grade %d = %+v, found=%t; want %+v", test.kind, want.Rarity, got, found, want)
			}
		}
	}
}

func TestCompoundLadderUsesConfiguredNextGradeAndStopsAtTerminal(t *testing.T) {
	catalog, err := LoadCompoundCatalog(catalogTestSource{text: liveGuildCompoundCatalog})
	if err != nil {
		t.Fatalf("LoadCompoundCatalog: %v", err)
	}

	next, ok := catalog.Ladder(CompoundGem).NextRarity(2)
	if !ok || next != 3 {
		t.Fatalf("gem grade 2 next = %d, %t; want 3, true", next, ok)
	}
	if _, ok := catalog.Ladder(CompoundGem).NextRarity(3); ok {
		t.Fatal("gem grade 3 must not name a next grade")
	}

	// 勋章 3 之后的档位是表里的 6，不是 grade+1。
	next, ok = catalog.Ladder(CompoundFlag).NextRarity(3)
	if !ok || next != 6 {
		t.Fatalf("flag grade 3 next = %d, %t; want 6, true", next, ok)
	}
	if _, ok := catalog.Ladder(CompoundFlag).NextRarity(6); ok {
		t.Fatal("flag grade 6 must not name a next grade")
	}
}

func TestCompoundSuccessRateUsesTwoThroughFourMaterials(t *testing.T) {
	catalog, err := LoadCompoundCatalog(catalogTestSource{text: liveGuildCompoundCatalog})
	if err != nil {
		t.Fatalf("LoadCompoundCatalog: %v", err)
	}
	recipe, _ := catalog.Ladder(CompoundGem).Recipe(0)
	for _, test := range []struct {
		count uint32
		want  uint32
	}{
		{count: 0, want: 0},
		{count: 1, want: 0},
		{count: 2, want: 4000},
		{count: 3, want: 6000},
		{count: 4, want: 8000},
		{count: 5, want: 0},
		{count: math.MaxUint32, want: 0},
	} {
		if got := recipe.SuccessRateFor(test.count); got != test.want {
			t.Fatalf("gem grade 0 with %d materials = %d, want %d", test.count, got, test.want)
		}
		if got := recipe.AcceptsMaterialCount(test.count); got != (test.want > 0) {
			t.Fatalf("gem grade 0 accepts %d = %t, want %t", test.count, got, test.want > 0)
		}
	}

	terminal, _ := catalog.Ladder(CompoundGem).Recipe(3)
	if terminal.AcceptsMaterialCount(2) || terminal.SuccessRateFor(2) != 0 {
		t.Fatalf("terminal recipe must not be compoundable: %+v", terminal)
	}
}

func TestCompoundSuccessRateCapsWithoutOverflow(t *testing.T) {
	for _, test := range []struct {
		count uint32
		want  uint32
	}{
		{count: 2, want: CompoundRateDenominator},
		{count: 4, want: CompoundRateDenominator},
	} {
		recipe := CompoundRecipe{Rarity: 0, MaxMaterialCount: 4, RatePerMaterial: math.MaxUint32}
		if got := recipe.SuccessRateFor(test.count); got != test.want {
			t.Fatalf("count=%d rate=%d, want cap %d", test.count, got, test.want)
		}
	}

	full := CompoundRecipe{Rarity: 0, MaxMaterialCount: 4, RatePerMaterial: 2500}
	if got := full.SuccessRateFor(4); got != CompoundRateDenominator {
		t.Fatalf("full rate = %d, want %d", got, CompoundRateDenominator)
	}
}

func TestLoadCompoundCatalogRejectsMalformedTables(t *testing.T) {
	validGem := "[compound info]\n0 4 2000 1 4 1500 2 4 750 3 2 0\n[/compound info]\n"
	validFlag := "[guild flag compound info]\n0 4 2500 1 4 2000 2 4 1000 3 4 500 6 2 0\n[/guid flag compound info]\n"
	tests := map[string]string{
		"missing gem section":    validFlag,
		"missing flag section":   validGem,
		"missing closing marker": "[compound info]\n0 4 2000 1 4 1500 2 4 750 3 2 0\n" + validFlag,
		"wrong closing marker":   "[compound info]\n0 4 2000 1 4 1500 2 4 750 3 2 0\n[/guid flag compound info]\n" + validFlag,
		"non-integer token":      "[compound info]\n0 4 2000 1 4 1500 2 four 750 3 2 0\n[/compound info]\n" + validFlag,
		"incomplete row":         "[compound info]\n0 4 2000 1 4\n[/compound info]\n" + validFlag,
		"count below two":        "[compound info]\n0 1 2000 1 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
		"count above four":       "[compound info]\n0 5 2000 1 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
		"grade not ascending":    "[compound info]\n1 4 2000 0 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
		"grade duplicated":       "[compound info]\n0 4 2000 0 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
		"non-terminal zero rate": "[compound info]\n0 4 0 1 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
		"terminal nonzero rate":  "[compound info]\n0 4 2000 1 4 1500 2 4 750 3 2 1\n[/compound info]\n" + validFlag,
		"rate above denominator": "[compound info]\n0 4 10001 1 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
		"negative value":         "[compound info]\n-1 4 2000 1 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
		"oversized value":        "[compound info]\n4294967296 4 2000 1 4 1500 2 4 750 3 2 0\n[/compound info]\n" + validFlag,
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := LoadCompoundCatalog(catalogTestSource{text: text})
			if !errors.Is(err, ErrCompoundCatalogInvalid) {
				t.Fatalf("error = %v, want ErrCompoundCatalogInvalid", err)
			}
		})
	}
}

func TestLoadCompoundCatalogErrors(t *testing.T) {
	text := validCatalogWithoutComments()
	if _, err := LoadCompoundCatalog(nil); !errors.Is(err, ErrCompoundCatalogUnavailable) {
		t.Fatalf("nil source error = %v, want ErrCompoundCatalogUnavailable", err)
	}
	wantRead := errors.New("read failed")
	if _, err := LoadCompoundCatalog(catalogTestSource{err: wantRead}); !errors.Is(err, wantRead) {
		t.Fatalf("read error = %v, want wrapped %v", err, wantRead)
	}
	if _, err := LoadCompoundCatalog(catalogTestSource{text: "[compound info]\n`unterminated"}); err == nil {
		t.Fatal("unterminated PVF string must fail")
	}
	if _, err := LoadCompoundCatalog(catalogTestSource{text: text + "\n[compound info]\n0 4 2000 1 4 1500 2 4 750 3 2 0\n[/compound info]\n"}); !errors.Is(err, ErrCompoundCatalogInvalid) {
		t.Fatalf("duplicate section error = %v, want ErrCompoundCatalogInvalid", err)
	}
}

func validCatalogWithoutComments() string {
	return "[compound info]\n0 4 2000 1 4 1500 2 4 750 3 2 0\n[/compound info]\n[guild flag compound info]\n0 4 2500 1 4 2000 2 4 1000 3 4 500 6 2 0\n[/guid flag compound info]\n"
}
