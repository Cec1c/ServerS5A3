package epicbook

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type textSource string

func (s textSource) ReadText(path string) (string, error) {
	if path != CatalogPath {
		return "", errors.New("unexpected catalog path")
	}
	return string(s), nil
}

const twoRecipes = `[equipment piece drop info]
[piece list]
450004 10300000 100050696 10300728
[/piece list]
[/equipment piece drop info]
[equipment recipe info]
[info]
[output]
450004
[recipe]
10300000 1000 10099775 44 10158124 6
[/recipe]
[/info]
[info]
[output]
100050696
[recipe]
10300728 1000 10099775 58 10158124 11
[/recipe]
[/info]
[/equipment recipe info]`

func TestCatalogRepeatedSectionsStayBoundToTheirOutput(t *testing.T) {
	c, err := LoadCatalog(textSource(twoRecipes))
	if err != nil || c.Len() != 2 {
		t.Fatalf("catalog=%v err=%v", c, err)
	}
	for output, want := range map[uint32]Recipe{
		450004:    {450004, 10300000, []Material{{10300000, 1000}, {10099775, 44}, {10158124, 6}}},
		100050696: {100050696, 10300728, []Material{{10300728, 1000}, {10099775, 58}, {10158124, 11}}},
	} {
		got, ok := c.Lookup(output)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("output=%d recipe=%+v want=%+v", output, got, want)
		}
		got.Materials[0].Count = 1
		again, _ := c.Lookup(output)
		if !reflect.DeepEqual(again, want) {
			t.Fatal("lookup leaked mutable catalog material slice")
		}
	}
	for _, id := range []uint32{0, 1, 10300000, 10300728} {
		if _, ok := c.Lookup(id); ok {
			t.Fatalf("piece or row number %d accepted as output", id)
		}
	}
}

func TestCatalogRejectsAmbiguousOrIncompleteCosts(t *testing.T) {
	for name, text := range map[string]string{
		"missing_info_end":    strings.Replace(twoRecipes, "[/info]", "", 1),
		"missing_recipe_end":  strings.Replace(twoRecipes, "[/recipe]", "", 1),
		"missing_list_end":    strings.Replace(twoRecipes, "[/piece list]", "", 1),
		"missing_catalog_end": strings.ReplaceAll(twoRecipes, "[/equipment recipe info]", ""),
		"duplicate_output":    strings.ReplaceAll(twoRecipes, "100050696", "450004"),
		"duplicate_piece":     strings.ReplaceAll(twoRecipes, "10300728", "10300000"),
		"other_piece":         strings.Replace(twoRecipes, "10300000 1000", "10300728 1000", 1),
		"no_piece":            strings.Replace(twoRecipes, "10300000 1000", "42 1000", 1),
		"unknown_output":      strings.Replace(twoRecipes, "[output]\n450004", "[output]\n1234", 1),
		"zero_cost":           strings.Replace(twoRecipes, "10099775 44", "10099775 0", 1),
		"negative_cost":       strings.Replace(twoRecipes, "10099775 44", "10099775 -44", 1),
		"overflow_cost":       strings.Replace(twoRecipes, "10099775 44", "10099775 2147483648", 1),
		"nonnumeric_cost":     strings.Replace(twoRecipes, "10099775 44", "10099775 `44`", 1),
		"odd_pair":            strings.Replace(twoRecipes, "10158124 6", "10158124", 1),
		"duplicate_material":  strings.Replace(twoRecipes, "10158124 6", "10099775 6", 1),
		"unknown_field":       strings.Replace(twoRecipes, "[output]", "[key]", 1),
		"empty":               "",
	} {
		t.Run(name, func(t *testing.T) {
			if c, err := LoadCatalog(textSource(text)); err == nil || c != nil {
				t.Fatalf("invalid catalog accepted: %v %v", c, err)
			}
		})
	}
	if _, err := LoadCatalog(nil); err == nil {
		t.Fatal("nil source accepted")
	}
}

func TestRealPVFEpicBookRecipesAllOutputsAndCostBandsRound8(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the paired Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalog(archive)
	if err != nil || c.Len() != 966 {
		t.Fatalf("catalog=%v err=%v", c, err)
	}
	wantBands := map[[2]uint32]int{{44, 6}: 67, {47, 7}: 200, {49, 8}: 195, {52, 9}: 256, {55, 10}: 138, {58, 11}: 110}
	bands := map[[2]uint32]int{}
	for output, r := range c.recipes {
		if output != r.OutputID || len(r.Materials) != 3 || r.Materials[0] != (Material{r.PieceID, 1000}) || r.Materials[1].ItemID != 10099775 || r.Materials[2].ItemID != 10158124 {
			t.Fatalf("unexpected actual recipe %+v", r)
		}
		bands[[2]uint32{r.Materials[1].Count, r.Materials[2].Count}]++
	}
	if !reflect.DeepEqual(bands, wantBands) {
		t.Fatalf("actual cost bands %v want %v", bands, wantBands)
	}
	for output, want := range map[uint32][]Material{
		450004:    {{10300000, 1000}, {10099775, 44}, {10158124, 6}},
		100050050: {{10300043, 1000}, {10099775, 52}, {10158124, 9}},
		100050696: {{10300728, 1000}, {10099775, 58}, {10158124, 11}},
	} {
		r, ok := c.Lookup(output)
		if !ok || !reflect.DeepEqual(r.Materials, want) {
			t.Fatalf("actual output %d: %+v", output, r)
		}
	}
}
