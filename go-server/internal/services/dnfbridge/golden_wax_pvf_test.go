package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	dnfitemgrade "longheng.io/server/internal/modules/dnf/itemgrade"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// The section bodies below follow the etc/reseal.etc shape recorded in the
// round20 reference (the embedded real-key sample in the reference tree's
// item_grade_adjust_test.go): level ranges and count limits are triplets, and
// the three rarity sequences hold seven entries each.
const (
	goldenWaxTestGradeRows    = "1 80 3 81 85 4 86 90 5"
	goldenWaxTestLimitRows    = "1 85 7 86 90 4"
	goldenWaxTestRarityRows   = "1 2 1 2 8 8 6"
	goldenWaxTestExtendedRows = "0 0 1 1 1 1 1"
	goldenWaxTestGrowthRows   = "0 0 1.10 1.10 0 0 1.11"
)

// goldenWaxTestDocument assembles a reseal.etc-shaped document. An override
// replaces the sample body of one section; an empty body drops that section, and
// a body holding only whitespace emits a present-but-empty section.
func goldenWaxTestDocument(override map[string]string) string {
	bodies := map[string]string{
		goldenWaxGradeSection:    goldenWaxTestGradeRows,
		goldenWaxLimitSection:    goldenWaxTestLimitRows,
		goldenWaxRaritySection:   goldenWaxTestRarityRows,
		goldenWaxExtendedSection: goldenWaxTestExtendedRows,
		goldenWaxGrowthSection:   goldenWaxTestGrowthRows,
	}
	for name, body := range override {
		bodies[name] = body
	}
	order := []string{goldenWaxGradeSection, goldenWaxRaritySection, goldenWaxExtendedSection, goldenWaxGrowthSection, goldenWaxLimitSection}
	var builder strings.Builder
	for _, name := range order {
		if body := bodies[name]; body != "" {
			fmt.Fprintf(&builder, "[%s]\n%s\n[/%s]\n", name, body, name)
		}
	}
	return builder.String()
}

func goldenWaxTestSource(text string) dnfpvf.Source {
	return bridgePVFSource{currentGoldenWaxResealConfigPath: text}
}

// goldenWaxTestClosedDocument builds a correctly closed required section by hand:
// one open marker, the given rows, one close marker, and the Section metadata the
// parser records for them (body = just after the open marker up to the close
// marker). Tests need this because text cannot produce NaN or infinite floats.
func goldenWaxTestClosedDocument(name string, rows []dnfpvf.Token) *dnfpvf.Document {
	tokens := append([]dnfpvf.Token{{Kind: dnfpvf.TokenSection, Raw: "[" + name + "]", Value: name}}, rows...)
	tokens = append(tokens, dnfpvf.Token{Kind: dnfpvf.TokenSection, Raw: "[/" + name + "]", Value: "/" + name})
	return &dnfpvf.Document{
		Path:   currentGoldenWaxResealConfigPath,
		Tokens: tokens,
		Sections: []dnfpvf.Section{
			{Name: name, Start: 1, End: len(tokens) - 1},
			{Name: "/" + name, Start: len(tokens), End: len(tokens)},
		},
	}
}

func TestCurrentGoldenWaxRulesReadResealTable(t *testing.T) {
	rules, err := loadCurrentGoldenWaxRules(goldenWaxTestSource(goldenWaxTestDocument(nil)))
	if err != nil {
		t.Fatalf("loadCurrentGoldenWaxRules: %v", err)
	}
	wantGrades := []dnfitemgrade.WaxRangeWeight{{Minimum: 1, Maximum: 80, Weight: 3}, {Minimum: 81, Maximum: 85, Weight: 4}, {Minimum: 86, Maximum: 90, Weight: 5}}
	wantLimits := []dnfitemgrade.WaxRangeWeight{{Minimum: 1, Maximum: 85, Weight: 7}, {Minimum: 86, Maximum: 90, Weight: 4}}
	assertRows := func(name string, got, want []dnfitemgrade.WaxRangeWeight) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %+v, want %+v", name, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("%s[%d] = %+v, want %+v", name, index, got[index], want[index])
			}
		}
	}
	assertRows("grades", rules.Grades, wantGrades)
	assertRows("limits", rules.Limits, wantLimits)
	if rules.Rarity != [7]float64{1, 2, 1, 2, 8, 8, 6} {
		t.Fatalf("rarity = %v", rules.Rarity)
	}
	if rules.Extended != [7]float64{0, 0, 1, 1, 1, 1, 1} {
		t.Fatalf("extended = %v, want the zero weights of unsupported rarities kept", rules.Extended)
	}
	if rules.Growth != [7]float64{0, 0, 1.10, 1.10, 0, 0, 1.11} {
		t.Fatalf("growth = %v", rules.Growth)
	}

	// The projection must feed the cost function the way the reference formula
	// reads it: level 85 sits in the 4-weight band, rarity 3 costs
	// 4 x 2 x 1 x 1.10^0, and the same level band allows seven reseals.
	if cost, limit, err := rules.WaxCost(85, 3, 0); err != nil || cost != 8 || limit != 7 {
		t.Fatalf("level 85 rarity 3 cost=%d limit=%d err=%v, want 8/7", cost, limit, err)
	}
	if cost, limit, err := rules.WaxCost(86, 3, 1); err != nil || cost != 11 || limit != 4 {
		t.Fatalf("level 86 rarity 3 count 1 cost=%d limit=%d err=%v, want 11/4", cost, limit, err)
	}
	// Rarity 0 carries extended weight 0: the table stays loadable and only a
	// request for that rarity is rejected.
	if _, _, err := rules.WaxCost(85, 0, 0); !errors.Is(err, dnfitemgrade.ErrWaxReseal) {
		t.Fatalf("rarity 0 err=%v, want %v", err, dnfitemgrade.ErrWaxReseal)
	}
	if _, _, err := rules.WaxCost(86, 3, 4); !errors.Is(err, dnfitemgrade.ErrWaxReseal) {
		t.Fatalf("count at the level limit err=%v, want %v", err, dnfitemgrade.ErrWaxReseal)
	}
}

func TestCurrentGoldenWaxRulesRejectMalformedTables(t *testing.T) {
	// Every fixture below damages the same reference table in exactly one way. The
	// unterminated, mismatched and reordered texts are derived here, with guards,
	// so a case cannot pass merely because its fixture stopped breaking anything.
	base := goldenWaxTestDocument(nil)
	if _, err := loadCurrentGoldenWaxRules(goldenWaxTestSource(base)); err != nil {
		t.Fatalf("the reference table must load: %v", err)
	}
	unterminated := strings.Replace(base, "[/"+goldenWaxGradeSection+"]\n", "", 1)
	if unterminated == base {
		t.Fatal("the unterminated fixture did not drop a closing marker")
	}
	unterminatedLast := strings.Replace(base, "[/"+goldenWaxLimitSection+"]\n", "", 1)
	if unterminatedLast == base {
		t.Fatal("the trailing fixture did not drop the last closing marker")
	}
	mismatched := strings.Replace(base, "[/"+goldenWaxGradeSection+"]", "[/"+goldenWaxGradeSection+"s]", 1)
	if mismatched == base {
		t.Fatal("the mismatched fixture did not rename a closing marker")
	}
	closedFirst := "[/" + goldenWaxGradeSection + "]\n" + unterminated
	cases := []struct {
		name     string
		override map[string]string
		document string
	}{
		{name: "missing section", override: map[string]string{goldenWaxLimitSection: ""}},
		{name: "empty section", override: map[string]string{goldenWaxGrowthSection: " "}},
		{name: "duplicated section", document: base + "[" + goldenWaxGradeSection + "]\n1 80 3\n[/" + goldenWaxGradeSection + "]\n"},
		{name: "unterminated section", document: unterminated},
		{name: "unterminated last section", document: unterminatedLast},
		{name: "mismatched closing marker", document: mismatched},
		{name: "closing marker before the section", document: closedFirst},
		{name: "nested section marker", override: map[string]string{goldenWaxRaritySection: goldenWaxTestRarityRows + "\n[extra]\n1\n[/extra]"}},
		{name: "identifier after a full sequence (NaN token)", override: map[string]string{goldenWaxRaritySection: goldenWaxTestRarityRows + " nan"}},
		{name: "quoted string in a sequence", override: map[string]string{goldenWaxRaritySection: goldenWaxTestRarityRows + " `seven`"}},
		{name: "identifier in a triplet section", override: map[string]string{goldenWaxLimitSection: goldenWaxTestLimitRows + " seven"}},
		{name: "broken triplet", override: map[string]string{goldenWaxLimitSection: "1 85 7 86 90"}},
		{name: "fractional triplet", override: map[string]string{goldenWaxGradeSection: "1 80 3 81 85 4.5 86 90 5"}},
		{name: "inverted range", override: map[string]string{goldenWaxGradeSection: "1 80 3 85 81 4"}},
		{name: "overlapping ranges", override: map[string]string{goldenWaxGradeSection: "1 85 3 81 90 4"}},
		{name: "negative range bound", override: map[string]string{goldenWaxGradeSection: "-1 80 3"}},
		{name: "non-positive triplet", override: map[string]string{goldenWaxLimitSection: "0 85 7"}},
		{name: "limit above seven", override: map[string]string{goldenWaxLimitSection: "1 85 8 86 90 4"}},
		{name: "short sequence", override: map[string]string{goldenWaxRaritySection: "1 2 1 2 8 8"}},
		{name: "long sequence", override: map[string]string{goldenWaxGrowthSection: "0 0 1.10 1.10 0 0 1.11 1.12"}},
		{name: "negative sequence entry", override: map[string]string{goldenWaxExtendedSection: "0 0 1 1 1 1 -1"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			text := test.document
			if text == "" {
				text = goldenWaxTestDocument(test.override)
			}
			rules, err := loadCurrentGoldenWaxRules(goldenWaxTestSource(text))
			if !errors.Is(err, errCurrentGoldenWaxPVFInvalid) {
				t.Fatalf("rules=%+v err=%v, want a wrapped %v", rules, err, errCurrentGoldenWaxPVFInvalid)
			}
		})
	}
}

func TestCurrentGoldenWaxRulesRejectNonFiniteNumbers(t *testing.T) {
	// The current dnfpvf text parser cannot deliver NaN or an infinity through
	// Numbers (a literal "nan" stays an identifier and "1e999" loses its float
	// shape), so these cases hand-build correctly closed sections. The finite
	// controls below show that closed shape still loads, so a rejection of the
	// non-finite rows can only come from the value check.
	floatRow := func(value float64) dnfpvf.Token { return dnfpvf.Token{Kind: dnfpvf.TokenFloat, Float: value} }
	sequenceRows := []dnfpvf.Token{floatRow(0), floatRow(1), floatRow(2), floatRow(3), floatRow(4), floatRow(5), floatRow(6)}
	if got, err := goldenWaxSequence(goldenWaxTestClosedDocument(goldenWaxRaritySection, sequenceRows), goldenWaxRaritySection); err != nil || got != [7]float64{0, 1, 2, 3, 4, 5, 6} {
		t.Fatalf("finite sequence control got=%v err=%v", got, err)
	}
	tripletRows := []dnfpvf.Token{{Kind: dnfpvf.TokenInt, Int: 1}, {Kind: dnfpvf.TokenInt, Int: 80}, {Kind: dnfpvf.TokenInt, Int: 3}}
	if rows, err := goldenWaxRangeWeights(goldenWaxTestClosedDocument(goldenWaxGradeSection, tripletRows), goldenWaxGradeSection, 0, 0); err != nil || len(rows) != 1 || rows[0] != (dnfitemgrade.WaxRangeWeight{Minimum: 1, Maximum: 80, Weight: 3}) {
		t.Fatalf("finite triplet control rows=%+v err=%v", rows, err)
	}
	for _, test := range []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
	} {
		rows := append([]dnfpvf.Token(nil), sequenceRows...)
		rows[len(rows)-1] = floatRow(test.value)
		t.Run("sequence "+test.name, func(t *testing.T) {
			if _, err := goldenWaxSequence(goldenWaxTestClosedDocument(goldenWaxRaritySection, rows), goldenWaxRaritySection); !errors.Is(err, errCurrentGoldenWaxPVFInvalid) {
				t.Fatalf("sequence ending in %s err=%v, want a wrapped %v", test.name, err, errCurrentGoldenWaxPVFInvalid)
			}
		})
		t.Run("triplet "+test.name, func(t *testing.T) {
			damaged := []dnfpvf.Token{floatRow(test.value), tripletRows[1], tripletRows[2]}
			if _, err := goldenWaxRangeWeights(goldenWaxTestClosedDocument(goldenWaxGradeSection, damaged), goldenWaxGradeSection, 0, 0); !errors.Is(err, errCurrentGoldenWaxPVFInvalid) {
				t.Fatalf("triplet starting with %s err=%v, want a wrapped %v", test.name, err, errCurrentGoldenWaxPVFInvalid)
			}
		})
	}
}

func TestCurrentGoldenWaxRulesRequireTheResealTable(t *testing.T) {
	if _, err := loadCurrentGoldenWaxRules(nil); !errors.Is(err, errDungeonDropSourceRequired) {
		t.Fatalf("nil source err=%v, want %v", err, errDungeonDropSourceRequired)
	}
	if _, err := loadCurrentGoldenWaxRules(bridgePVFSource{}); !errors.Is(err, errCurrentGoldenWaxPVFInvalid) {
		t.Fatalf("absent %s err=%v, want a wrapped %v", currentGoldenWaxResealConfigPath, err, errCurrentGoldenWaxPVFInvalid)
	}
}
