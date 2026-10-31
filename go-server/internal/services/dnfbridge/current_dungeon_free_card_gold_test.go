package dnfbridge

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestCurrentDungeonFreeCardGoldAmountLevel90Examples(t *testing.T) {
	t.Parallel()

	reference := currentDungeonGoldReference{Level: 90, Base: 1438, VariancePct: 0}
	tests := []struct {
		name        string
		partyRate   float64
		partyCount  int
		wantAmount  int64
		wantFormula string
	}{
		{name: "solo", partyRate: 1, partyCount: 1, wantAmount: 7247, wantFormula: "trunc(1438*175/1000*12*2*1*1.2/1)=7247"},
		{name: "party_two", partyRate: 1.2, partyCount: 2, wantAmount: 4348, wantFormula: "trunc(1438*175/1000*12*2*1.2*1.2/2)=4348"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rng := newCurrentDungeonDropLCG(20261029)
			got, err := currentDungeonFreeCardGoldAmount(
				rng,
				reference,
				20, 0, 1,
				2, test.partyRate, 0.2,
				test.partyCount,
			)
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", test.wantFormula, err)
			}
			if got != test.wantAmount {
				t.Fatalf("amount=%d, want %d from %s", got, test.wantAmount, test.wantFormula)
			}
		})
	}
}

func TestCurrentDungeonFreeCardGoldAmountZeroKillsReturnsZeroWithoutDrawing(t *testing.T) {
	t.Parallel()

	rng := newCurrentDungeonDropLCG(37)
	got, err := currentDungeonFreeCardGoldAmount(
		rng,
		currentDungeonGoldReference{Level: 90, Base: 1438, VariancePct: 9},
		0, 0, 0,
		2, 1.2, 0.2,
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("zero-kill amount=%d, want 0", got)
	}
	if seed := rng.Seed(); seed != 37 {
		t.Fatalf("zero-kill calculation consumed rng: seed=%d", seed)
	}
}

func TestCurrentDungeonFreeCardGoldAmountTruncatedZeroDoesNotDraw(t *testing.T) {
	rng := newCurrentDungeonDropLCG(37)
	got, err := currentDungeonFreeCardGoldAmount(rng,
		currentDungeonGoldReference{Base: 1, VariancePct: 9},
		1, 0, 0, 1, 1, 0, 1)
	if err != nil || got != 0 || rng.Seed() != 37 {
		t.Fatalf("truncated zero must preserve RNG: amount=%d seed=%d err=%v", got, rng.Seed(), err)
	}
}

func TestCurrentDungeonFreeCardGoldAmountRejectsWeightedCountOverflow(t *testing.T) {
	t.Parallel()

	got, err := currentDungeonFreeCardGoldAmount(
		newCurrentDungeonDropLCG(1),
		currentDungeonGoldReference{Level: 90, Base: 1438, VariancePct: 0},
		math.MaxInt64, 1, 0,
		1, 1, 0,
		1,
	)
	if got != 0 {
		t.Fatalf("overflow amount=%d, want 0", got)
	}
	if !errors.Is(err, errCurrentDungeonFreeCardGoldInputInvalid) {
		t.Fatalf("overflow error=%v, want invalid-input error", err)
	}
}

func TestCurrentDungeonFreeCardGoldAmountVarianceIsSeededAndBounded(t *testing.T) {
	t.Parallel()

	reference := currentDungeonGoldReference{Level: 90, Base: 1438, VariancePct: 9}
	tests := []struct {
		seed      uint32
		want      int64
		wantState uint32
	}{
		{seed: 12345, want: 6594, wantState: 3596950572}, // delta -9% of 7247
		{seed: 99, want: 7247, wantState: 1470374366},    // delta 0%
		{seed: 0, want: 7464, wantState: 2802067423},     // delta +3%
	}
	for _, test := range tests {
		test := test
		t.Run(strconv.FormatUint(uint64(test.seed), 10), func(t *testing.T) {
			t.Parallel()
			rng := newCurrentDungeonDropLCG(test.seed)
			got, err := currentDungeonFreeCardGoldAmount(
				rng,
				reference,
				20, 0, 1,
				2, 1, 0.2,
				1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("seed=%d amount=%d, want %d", test.seed, got, test.want)
			}
			if rng.Seed() != test.wantState {
				t.Fatalf("seed=%d next state=%d, want %d", test.seed, rng.Seed(), test.wantState)
			}
			if got < currentDungeonFreeCardGoldBoundForTest(7247, 9, -1) || got > currentDungeonFreeCardGoldBoundForTest(7247, 9, 1) {
				t.Fatalf("seed=%d amount=%d outside deterministic variance bounds", test.seed, got)
			}
		})
	}
}

func currentDungeonFreeCardGoldBoundForTest(base int64, variancePct, direction int64) int64 {
	return base * (100 + direction*variancePct) / 100
}

func TestCurrentDungeonFreeCardGoldAmountTruncatesOnlyAfterProportions(t *testing.T) {
	t.Parallel()

	// Truncating base*175/1000 first yields 251 and produces 7228 for this
	// fixture. The required full-expression truncation produces 7247.
	got, err := currentDungeonFreeCardGoldAmount(
		newCurrentDungeonDropLCG(5),
		currentDungeonGoldReference{Level: 90, Base: 1438, VariancePct: 0},
		20, 0, 1,
		2, 1, 0.2,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != 7247 {
		t.Fatalf("amount=%d, want full-expression truncation 7247", got)
	}
}

func TestCurrentDungeonFreeCardGoldAmountRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	validReference := currentDungeonGoldReference{Level: 90, Base: 1438, VariancePct: 0}
	tests := []struct {
		name       string
		rng        *currentDungeonDropLCG
		reference  currentDungeonGoldReference
		normal     int64
		champion   int64
		boss       int64
		difficulty float64
		partyRate  float64
		rankBonus  float64
		partyCount int
		wantError  bool
	}{
		{name: "negative_normal", rng: newCurrentDungeonDropLCG(1), reference: validReference, normal: -1, difficulty: 1, partyRate: 1, partyCount: 1, wantError: true},
		{name: "zero_party", rng: newCurrentDungeonDropLCG(1), reference: validReference, difficulty: 1, partyRate: 1, partyCount: 0, wantError: true},
		{name: "nan_difficulty", rng: newCurrentDungeonDropLCG(1), reference: validReference, normal: 1, difficulty: math.NaN(), partyRate: 1, partyCount: 1, wantError: true},
		{name: "infinite_party", rng: newCurrentDungeonDropLCG(1), reference: validReference, normal: 1, difficulty: 1, partyRate: math.Inf(1), partyCount: 1, wantError: true},
		{name: "negative_rank", rng: newCurrentDungeonDropLCG(1), reference: validReference, normal: 1, difficulty: 1, partyRate: 1, rankBonus: -0.1, partyCount: 1, wantError: true},
		{name: "nil_rng_with_variance", rng: nil, reference: currentDungeonGoldReference{Level: 90, Base: 1438, VariancePct: 1}, normal: 1, difficulty: 1, partyRate: 1, partyCount: 1, wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := currentDungeonFreeCardGoldAmount(test.rng, test.reference, test.normal, test.champion, test.boss, test.difficulty, test.partyRate, test.rankBonus, test.partyCount)
			if test.wantError && !errors.Is(err, errCurrentDungeonFreeCardGoldInputInvalid) {
				t.Fatalf("amount=%d error=%v, want invalid-input error", got, err)
			}
		})
	}
}
