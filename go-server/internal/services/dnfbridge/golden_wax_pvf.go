package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"strings"

	dnfitemgrade "longheng.io/server/internal/modules/dnf/itemgrade"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// currentGoldenWaxResealConfigPath is the runtime table behind the current
// client's golden-wax reseal cost. It is the same document the reference server
// reads for the reseal formula; only the golden-wax inputs are projected here
// (the soul item/index rows of that file are not used by this path).
const currentGoldenWaxResealConfigPath = "etc/reseal.etc"

// A malformed table must fail closed: the caller charges a real material from
// these numbers, so a missing or duplicated section, a broken triplet, an
// inverted or overlapping range, or an out-of-range limit rejects the request
// instead of pricing it from guessed data.
var errCurrentGoldenWaxPVFInvalid = errors.New("current golden wax reseal PVF is invalid")

// The three rarity-indexed sequences hold exactly seven entries: the current
// client's rarity space is 0..6.
const goldenWaxRarityCount = 7

// goldenWaxExactInt64Bound is 2^63. float64 cannot represent int64's maximum
// exactly, so anything at or beyond this bound is rejected before conversion.
const goldenWaxExactInt64Bound = 9223372036854775808.0

const (
	goldenWaxGradeSection    = "grade"
	goldenWaxRaritySection   = "rarity weight"
	goldenWaxExtendedSection = "extended rarity weight"
	goldenWaxGrowthSection   = "soul reseal constant"
	goldenWaxLimitSection    = "reseal count limit"
)

// loadCurrentGoldenWaxRules reads the active PVF's etc/reseal.etc and projects
// the golden-wax inputs of the reseal cost formula:
//
//	[grade]                  (minimum level, maximum level, weight) triplets
//	[reseal count limit]     (minimum level, maximum level, limit) triplets, limit 1..7
//	[rarity weight]          7 weights indexed by rarity
//	[extended rarity weight] 7 weights indexed by rarity, 0 = rarity unsupported
//	[soul reseal constant]   7 pow bases indexed by rarity
//
// Every required section has to be closed exactly once (one [name] marker and one
// matching [/name] marker behind it) and to hold nothing but numeric rows. Range
// triplets are integers, positive, not inverted and not overlapping, so the first
// matching row is unambiguous. Sequence entries are finite and non-negative; a
// zero in
// [extended rarity weight] is the legal "this rarity is not resealable" marker
// and is never forced to a value, because the cost function rejects only the
// rarities a request actually touches.
func loadCurrentGoldenWaxRules(source dnfpvf.Source) (dnfitemgrade.WaxRules, error) {
	if source == nil {
		return dnfitemgrade.WaxRules{}, errDungeonDropSourceRequired
	}
	text, err := source.ReadText(currentGoldenWaxResealConfigPath)
	if err != nil {
		return dnfitemgrade.WaxRules{}, fmt.Errorf("%w: read %s: %w", errCurrentGoldenWaxPVFInvalid, currentGoldenWaxResealConfigPath, err)
	}
	document, err := dnfpvf.Parse(currentGoldenWaxResealConfigPath, text)
	if err != nil {
		return dnfitemgrade.WaxRules{}, fmt.Errorf("%w: parse %s: %w", errCurrentGoldenWaxPVFInvalid, currentGoldenWaxResealConfigPath, err)
	}
	var rules dnfitemgrade.WaxRules
	if rules.Grades, err = goldenWaxRangeWeights(document, goldenWaxGradeSection, 0, 0); err != nil {
		return dnfitemgrade.WaxRules{}, err
	}
	if rules.Limits, err = goldenWaxRangeWeights(document, goldenWaxLimitSection, 1, 7); err != nil {
		return dnfitemgrade.WaxRules{}, err
	}
	if rules.Rarity, err = goldenWaxSequence(document, goldenWaxRaritySection); err != nil {
		return dnfitemgrade.WaxRules{}, err
	}
	if rules.Extended, err = goldenWaxSequence(document, goldenWaxExtendedSection); err != nil {
		return dnfitemgrade.WaxRules{}, err
	}
	if rules.Growth, err = goldenWaxSequence(document, goldenWaxGrowthSection); err != nil {
		return dnfitemgrade.WaxRules{}, err
	}
	return rules, nil
}

// goldenWaxSectionNumbers returns the numeric rows of a required section after
// strict lexical validation: exactly one [name] marker, exactly one matching
// [/name] marker behind it, and nothing but integer or float rows in between.
// Document.Numbers() skips identifiers and strings, so an extra token (a literal
// "nan", for example, which the parser keeps as an identifier) would otherwise be
// dropped instead of rejected.
func goldenWaxSectionNumbers(document *dnfpvf.Document, name string) ([]float64, error) {
	if document == nil {
		return nil, fmt.Errorf("%w: [%s] needs a parsed document", errCurrentGoldenWaxPVFInvalid, name)
	}
	closeMarker := "/" + name
	opened, closed, opens, closes := -1, -1, 0, 0
	for index, token := range document.Tokens {
		if token.Kind != dnfpvf.TokenSection {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(token.Value)) {
		case name:
			opens++
			if opened < 0 {
				opened = index
			}
		case closeMarker:
			closes++
			if closed < 0 {
				closed = index
			}
		}
	}
	if opens != 1 || closes != 1 {
		return nil, fmt.Errorf("%w: [%s] needs exactly one [%s] and one [/%s] marker, got %d and %d", errCurrentGoldenWaxPVFInvalid, name, name, name, opens, closes)
	}
	if closed < opened {
		return nil, fmt.Errorf("%w: [%s] closes before it opens", errCurrentGoldenWaxPVFInvalid, name)
	}
	// The span validated here is the range Numbers() reads: one open marker plus
	// one close marker means the section record ends exactly at the close marker,
	// and a nested section marker inside the span fails the row check below.
	rows := document.Tokens[opened+1 : closed]
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: [%s] holds no numbers", errCurrentGoldenWaxPVFInvalid, name)
	}
	for index, token := range rows {
		if token.Kind != dnfpvf.TokenInt && token.Kind != dnfpvf.TokenFloat {
			return nil, fmt.Errorf("%w: [%s] row %d is %q, want only integer or float rows", errCurrentGoldenWaxPVFInvalid, name, index+1, token.Raw)
		}
	}
	return document.Numbers(name), nil
}

// goldenWaxRangeWeights turns "minimum maximum weight" triplets into level
// ranges. minimumWeight/maximumWeight bound the weight column when
// maximumWeight > 0: [reseal count limit] carries the maximum number of reseals
// a level band allows, which the current client caps at seven.
func goldenWaxRangeWeights(document *dnfpvf.Document, name string, minimumWeight, maximumWeight int64) ([]dnfitemgrade.WaxRangeWeight, error) {
	values, err := goldenWaxSectionNumbers(document, name)
	if err != nil {
		return nil, err
	}
	if len(values)%3 != 0 {
		return nil, fmt.Errorf("%w: [%s] holds %d numbers, want whole 'minimum maximum weight' triplets", errCurrentGoldenWaxPVFInvalid, name, len(values))
	}
	rows := make([]dnfitemgrade.WaxRangeWeight, 0, len(values)/3)
	for index := 0; index < len(values); index += 3 {
		row := index/3 + 1
		minimum, ok := goldenWaxExactInt(values[index])
		if !ok {
			return nil, fmt.Errorf("%w: [%s] triplet %d minimum is not an integer", errCurrentGoldenWaxPVFInvalid, name, row)
		}
		maximum, ok := goldenWaxExactInt(values[index+1])
		if !ok {
			return nil, fmt.Errorf("%w: [%s] triplet %d maximum is not an integer", errCurrentGoldenWaxPVFInvalid, name, row)
		}
		weight, ok := goldenWaxExactInt(values[index+2])
		if !ok {
			return nil, fmt.Errorf("%w: [%s] triplet %d weight is not an integer", errCurrentGoldenWaxPVFInvalid, name, row)
		}
		if minimum <= 0 || weight <= 0 {
			return nil, fmt.Errorf("%w: [%s] triplet %d must be positive, got %d %d %d", errCurrentGoldenWaxPVFInvalid, name, row, minimum, maximum, weight)
		}
		if maximum < minimum {
			return nil, fmt.Errorf("%w: [%s] triplet %d range %d..%d is inverted", errCurrentGoldenWaxPVFInvalid, name, row, minimum, maximum)
		}
		if maximumWeight > 0 && (weight < minimumWeight || weight > maximumWeight) {
			return nil, fmt.Errorf("%w: [%s] triplet %d weight %d is outside %d..%d", errCurrentGoldenWaxPVFInvalid, name, row, weight, minimumWeight, maximumWeight)
		}
		if previous := len(rows); previous > 0 && minimum <= rows[previous-1].Maximum {
			return nil, fmt.Errorf("%w: [%s] triplet %d range %d..%d overlaps the range ending at %d", errCurrentGoldenWaxPVFInvalid, name, row, minimum, maximum, rows[previous-1].Maximum)
		}
		rows = append(rows, dnfitemgrade.WaxRangeWeight{Minimum: minimum, Maximum: maximum, Weight: weight})
	}
	return rows, nil
}

func goldenWaxSequence(document *dnfpvf.Document, name string) ([7]float64, error) {
	values, err := goldenWaxSectionNumbers(document, name)
	if err != nil {
		return [7]float64{}, err
	}
	return goldenWaxSequenceValues(name, values)
}

// goldenWaxSequenceValues validates one rarity-indexed weight sequence: exactly
// seven entries, each finite and non-negative. Zero stays legal (see the
// [extended rarity weight] contract above), so a table that deliberately leaves
// a rarity unsupported is accepted as a whole.
func goldenWaxSequenceValues(name string, values []float64) ([7]float64, error) {
	var result [7]float64
	if len(values) != goldenWaxRarityCount {
		return result, fmt.Errorf("%w: [%s] holds %d numbers, want %d", errCurrentGoldenWaxPVFInvalid, name, len(values), goldenWaxRarityCount)
	}
	for index, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return result, fmt.Errorf("%w: [%s] entry %d is %v, want a finite non-negative weight", errCurrentGoldenWaxPVFInvalid, name, index, value)
		}
		result[index] = value
	}
	return result, nil
}

// goldenWaxExactInt accepts only values that are exactly representable int64
// numbers, so a fractional, NaN or infinite token cannot silently become a range
// bound or a weight.
func goldenWaxExactInt(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < -goldenWaxExactInt64Bound || value >= goldenWaxExactInt64Bound {
		return 0, false
	}
	return int64(value), true
}
