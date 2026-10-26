package aikenupgrade

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const setCatalogSample = `
[point info]

[part set index]
1
[min point]
150
[max point]
600
[min part count]
3
[max part count]
6
[max point table]
@D@ 3 105 @D@ 4 175 @D@ 5 280 @D@ 6 420 @C@ 3 120 @C@ 4 200 @C@ 5 320 @C@ 6 480 @B@ 3 135 @B@ 4 225 @B@ 5 360 @B@ 6 540 @A@ 3 150 @A@ 4 250 @A@ 5 400 @A@ 6 600
[/max point table]

[material rate by point]
10161537 2.20
[/material rate by point]

[discount enchant rate]
0.90
[enchant max count]
4
[/point info]

[spend grade point]
1 50 2 60 3 70 11 150
[/spend grade point]

[group]
[key]
@1@
[indexes]
1 2 3
[/indexes]
[condition]
@part count@ 4
[/condition]
[/group]

[group]
[key]
@7@
[indexes]
11
[/indexes]
[condition]
@part count@ 6
[/condition]
[/group]

[set item name]
@A@ @Set A@
[set item name]
@B@ @Set B@
[set item name]
@C@ @Set C@
[set item name]
@D@ @Set D@
[group name]
@1@ @Damage@
[group name]
@7@ @Skill Level@
`

func setCatalogText(raw string) string {
	return strings.ReplaceAll(raw, "@", "`")
}

func sampleSetCatalogText() string {
	return setCatalogText(setCatalogSample)
}

func mustParseSetCatalog(t *testing.T, text string) *SetCatalog {
	t.Helper()
	catalog, err := ParseSetCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestParseSetCatalogRealShape(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	rule, ok := catalog.Points[1]
	if !ok {
		t.Fatal("part set index 1 is missing")
	}
	if rule.MinPoint != 150 || rule.MaxPoint != 600 || rule.MinPartCount != 3 || rule.MaxPartCount != 6 {
		t.Fatalf("point bounds = %+v", rule)
	}
	if rule.EnchantMax != 4 || rule.DiscountRate != float32(0.90) {
		t.Fatalf("discount inputs = %+v", rule)
	}
	if len(rule.Materials) != 1 || rule.Materials[0] != (MaterialRate{ItemID: 10161537, Rate: 2.20}) {
		t.Fatalf("materials = %+v", rule.Materials)
	}
	for grade, want := range map[int][4]int64{
		0: {105, 175, 280, 420},
		1: {120, 200, 320, 480},
		2: {135, 225, 360, 540},
		3: {150, 250, 400, 600},
	} {
		for offset, points := range want {
			got, ok := catalog.MaxPoint(1, grade, 3+offset)
			if !ok || got != points {
				t.Fatalf("MaxPoint(1,%d,%d) = %d,%v want %d", grade, 3+offset, got, ok, points)
			}
		}
	}
	if catalog.Spend[1] != 50 || catalog.Spend[11] != 150 {
		t.Fatalf("spend points = %+v", catalog.Spend)
	}
	if len(catalog.Groups) != 2 {
		t.Fatalf("groups = %+v", catalog.Groups)
	}
	if catalog.Groups[0].Key != 1 || catalog.Groups[0].ConditionPartCount != 4 ||
		!reflect.DeepEqual(catalog.Groups[0].Indexes, []byte{1, 2, 3}) {
		t.Fatalf("group 1 = %+v", catalog.Groups[0])
	}
	if catalog.Groups[1].Key != 7 || catalog.Groups[1].ConditionPartCount != 6 ||
		!reflect.DeepEqual(catalog.Groups[1].Indexes, []byte{11}) {
		t.Fatalf("group 7 = %+v", catalog.Groups[1])
	}
	if catalog.SetNames["A"] != "Set A" || catalog.GroupNames[7] != "Skill Level" {
		t.Fatalf("names = %+v %+v", catalog.SetNames, catalog.GroupNames)
	}
}

func TestParseSetCatalogTokensMatchesText(t *testing.T) {
	text := sampleSetCatalogText()
	fromText, err := ParseSetCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pvf.Parse(setCatalogPath, text)
	if err != nil {
		t.Fatal(err)
	}
	fromTokens, err := ParseSetCatalogTokens(doc.Tokens)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromText, fromTokens) {
		t.Fatalf("token parse differs\ntext:   %#v\ntokens: %#v", fromText, fromTokens)
	}
}

func TestSelectGradeMatchesUnsignedAverage(t *testing.T) {
	tests := []struct {
		name   string
		grades []byte
		want   int
		count  int
	}{
		{name: "empty sentinel", want: 4},
		{name: "all grades", grades: []byte{0, 1, 2, 3}, want: 1, count: 4},
		{name: "floor", grades: []byte{0, 0, 3}, want: 1, count: 3},
		{name: "filter four and above", grades: []byte{4, 7, 255}, want: 4},
		{name: "mixed filter", grades: []byte{3, 4, 3, 9}, want: 3, count: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			grade, count := SelectGrade(tc.grades)
			if grade != tc.want || count != tc.count {
				t.Fatalf("SelectGrade(%v) = %d,%d want %d,%d", tc.grades, grade, count, tc.want, tc.count)
			}
		})
	}
}

func TestOptionCostMatchesProvenFormula(t *testing.T) {
	tests := []struct {
		base, remaining, want int64
	}{
		{base: 50, remaining: 150, want: 54},
		{base: 60, remaining: 96, want: 62},
		{base: 150, remaining: 150, want: 150},
		{base: 70, remaining: 75, want: 70},
		{base: 0, remaining: 100, want: 0},
		{base: 50, remaining: 0, want: 0},
	}
	for _, tc := range tests {
		if got := OptionCost(tc.base, tc.remaining); got != tc.want {
			t.Fatalf("OptionCost(%d,%d) = %d want %d", tc.base, tc.remaining, got, tc.want)
		}
	}
}

func TestPlanCostSequentialAndFloat32Materials(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	plan, err := catalog.PlanCost(1, 3, 3, []byte{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Available != 150 || plan.Spent != 116 || plan.Remaining != 34 {
		t.Fatalf("plan totals = %+v", plan)
	}
	wantSteps := []CostStep{
		{Option: 1, Base: 50, Before: 150, Cost: 54, After: 96},
		{Option: 2, Base: 60, Before: 96, Cost: 62, After: 34},
	}
	if !reflect.DeepEqual(plan.Steps, wantSteps) {
		t.Fatalf("steps = %+v want %+v", plan.Steps, wantSteps)
	}
	wantMaterials := []MaterialCost{{ItemID: 10161537, Count: 255}}
	if !reflect.DeepEqual(plan.Materials, wantMaterials) {
		t.Fatalf("materials = %+v want %+v", plan.Materials, wantMaterials)
	}
}

func TestPlanCostInsufficientStopsBeforeMaterials(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	plan, err := catalog.PlanCost(1, 3, 3, []byte{3, 11})
	if !errors.Is(err, ErrInsufficientSetPoints) {
		t.Fatalf("error = %v want ErrInsufficientSetPoints", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Cost != 75 || plan.Steps[0].After != 75 || plan.Remaining != -65 {
		t.Fatalf("partial plan = %+v", plan)
	}
	if len(plan.Materials) != 0 {
		t.Fatalf("insufficient plan produced materials: %+v", plan.Materials)
	}
}

func TestPlanCostUnknownOptionFailsClosed(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	if _, err := catalog.PlanCost(1, 3, 3, []byte{99}); !errors.Is(err, ErrSetCatalog) {
		t.Fatalf("error = %v want ErrSetCatalog", err)
	}
}

func TestPlanCostRejectsZeroRemainingBeforeNextOption(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	plan, err := catalog.PlanCost(1, 3, 3, []byte{11, 2})
	if !errors.Is(err, ErrInsufficientSetPoints) {
		t.Fatalf("error = %v want ErrInsufficientSetPoints", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Cost != 150 || plan.Steps[0].After != 0 {
		t.Fatalf("steps = %+v", plan.Steps)
	}
}

func TestPlanCostRejectsMutableCatalogOverflow(t *testing.T) {
	mutations := []struct {
		name string
		bind func(*SetCatalog)
	}{
		{
			name: "max point above int32",
			bind: func(catalog *SetCatalog) {
				rule := catalog.Points[1]
				rule.MaxPoints[MaxPointKey{Grade: 3, PartCount: 3}] = setCatalogMaxInt32 + 1
				catalog.Points[1] = rule
			},
		},
		{
			name: "material rate overflow",
			bind: func(catalog *SetCatalog) {
				rule := catalog.Points[1]
				rule.Materials[0].Rate = 1e20
				catalog.Points[1] = rule
			},
		},
		{
			name: "empty materials",
			bind: func(catalog *SetCatalog) {
				rule := catalog.Points[1]
				rule.Materials = nil
				catalog.Points[1] = rule
			},
		},
		{
			name: "zero material item",
			bind: func(catalog *SetCatalog) {
				rule := catalog.Points[1]
				rule.Materials[0].ItemID = 0
				catalog.Points[1] = rule
			},
		},
		{
			name: "spend base above int32",
			bind: func(catalog *SetCatalog) {
				catalog.Spend[1] = setCatalogMaxInt32
			},
		},
		{
			name: "zero spend base",
			bind: func(catalog *SetCatalog) {
				catalog.Spend[1] = 0
			},
		},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			catalog := mustParseSetCatalog(t, sampleSetCatalogText())
			tc.bind(catalog)
			if _, err := catalog.PlanCost(1, 3, 3, []byte{1}); !errors.Is(err, ErrSetCatalog) {
				t.Fatalf("error = %v want ErrSetCatalog", err)
			}
		})
	}
}

func TestOptionCostRejectsInt32Overflow(t *testing.T) {
	for _, tc := range []struct{ base, remaining int64 }{
		{base: setCatalogMaxInt32 - 1000, remaining: 1},
		{base: 1, remaining: setCatalogMaxInt32 - 999},
		{base: setCatalogMaxInt32, remaining: 1},
	} {
		if got := OptionCost(tc.base, tc.remaining); got != 0 {
			t.Fatalf("OptionCost(%d,%d) = %d want 0", tc.base, tc.remaining, got)
		}
	}
}

func TestMaterialConversionSaturatesAtInt32(t *testing.T) {
	if got := MaterialCounts(setCatalogMaxInt32, []MaterialRate{{ItemID: 1, Rate: 2}}); got[0].Count != setCatalogMaxInt32 {
		t.Fatalf("positive overflow = %+v", got)
	}
	if got := MaterialCounts(setCatalogMaxInt32, []MaterialRate{{ItemID: 1, Rate: 1e30}}); got[0].Count != setCatalogMaxInt32 {
		t.Fatalf("infinite product = %+v", got)
	}
	if got := MaterialCounts(10, []MaterialRate{{ItemID: 1, Rate: float32(math.NaN())}}); got[0].Count != 0 {
		t.Fatalf("NaN product = %+v", got)
	}
	if got := ApplyDiscountRate([]MaterialCost{{ItemID: 1, Count: setCatalogMaxInt32}}, 1); got[0].Count != setCatalogMaxInt32 {
		t.Fatalf("discount overflow = %+v", got)
	}
}

func TestPlanSelectionValidatesSelectionBeforeCost(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	catalog.Groups[0].Indexes = []byte{1, 3}
	catalog.Groups = append(catalog.Groups, OptionGroup{Key: 8, Indexes: []byte{2}})
	grade, plan, err := catalog.PlanSelection(1, []byte{3, 3, 3, 3}, []byte{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if grade != 3 || plan.Available != 250 || plan.Spent != 126 || plan.Remaining != 124 {
		t.Fatalf("selection = grade %d plan %+v", grade, plan)
	}
	wantSteps := []CostStep{
		{Option: 1, Base: 50, Before: 250, Cost: 59, After: 191},
		{Option: 2, Base: 60, Before: 191, Cost: 67, After: 124},
	}
	if !reflect.DeepEqual(plan.Steps, wantSteps) {
		t.Fatalf("steps = %+v want %+v", plan.Steps, wantSteps)
	}
	if !reflect.DeepEqual(plan.Materials, []MaterialCost{{ItemID: 10161537, Count: 277}}) {
		t.Fatalf("materials = %+v", plan.Materials)
	}
}

func TestPlanSelectionGroupConditionUsesMinimumPartCount(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	grade, plan, err := catalog.PlanSelection(1, []byte{3, 3, 3, 2, 2}, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if grade != 2 || plan.Available != 360 {
		t.Fatalf("selection = grade %d plan %+v", grade, plan)
	}
}

func TestPlanSelectionRejectsInvalidBusinessSelections(t *testing.T) {
	catalog := mustParseSetCatalog(t, sampleSetCatalogText())
	tests := []struct {
		name    string
		grades  []byte
		options []byte
	}{
		{name: "grade above three", grades: []byte{4, 4, 4, 4}, options: []byte{1}},
		{name: "too few parts", grades: []byte{3, 3}, options: []byte{1}},
		{name: "too many parts", grades: []byte{3, 3, 3, 3, 3, 3, 3}, options: []byte{1}},
		{name: "no options", grades: []byte{3, 3, 3, 3}, options: nil},
		{name: "too many options", grades: []byte{3, 3, 3, 3}, options: []byte{1, 2, 3, 1, 2}},
		{name: "zero option", grades: []byte{3, 3, 3, 3}, options: []byte{0}},
		{name: "duplicate option", grades: []byte{3, 3, 3, 3}, options: []byte{1, 1}},
		{name: "same group twice", grades: []byte{3, 3, 3, 3}, options: []byte{1, 2}},
		{name: "unknown option", grades: []byte{3, 3, 3, 3}, options: []byte{99}},
		{name: "group condition unmet", grades: []byte{3, 3, 3, 3}, options: []byte{11}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := catalog.PlanSelection(1, tc.grades, tc.options); !errors.Is(err, ErrSetSelection) {
				t.Fatalf("error = %v want ErrSetSelection", err)
			}
		})
	}
}

func TestMaterialCountsUsesFloat32Truncation(t *testing.T) {
	rates := []MaterialRate{{ItemID: 7, Rate: 2.20}}
	for _, tc := range []struct {
		points int64
		want   int64
	}{
		{points: 1, want: 2},
		{points: 3, want: 6},
		{points: 5, want: 11},
		{points: 116, want: 255},
	} {
		got := MaterialCounts(tc.points, rates)
		if len(got) != 1 || got[0].Count != tc.want {
			t.Fatalf("MaterialCounts(%d) = %+v want %d", tc.points, got, tc.want)
		}
	}
}

func TestApplyDiscountRateClampsAndTruncatesEachRow(t *testing.T) {
	base := []MaterialCost{{ItemID: 1, Count: 255}, {ItemID: 2, Count: 77}}
	got := ApplyDiscountRate(base, float32(0.90))
	want := []MaterialCost{{ItemID: 1, Count: 229}, {ItemID: 2, Count: 69}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discounted = %+v want %+v", got, want)
	}
	if got := ApplyDiscountRate(base, 0); !reflect.DeepEqual(got, []MaterialCost{{ItemID: 1}, {ItemID: 2}}) {
		t.Fatalf("zero rate = %+v", got)
	}
	if got := ApplyDiscountRate(base, 1.5); !reflect.DeepEqual(got, base) {
		t.Fatalf("clamped rate = %+v", got)
	}
	if got := ApplyDiscountRate(base, float32(math.NaN())); !reflect.DeepEqual(got, []MaterialCost{{ItemID: 1}, {ItemID: 2}}) {
		t.Fatalf("NaN rate = %+v", got)
	}
}

func TestNetMaterialsUsesFirstDeductionAndZeroFloor(t *testing.T) {
	base := []MaterialCost{
		{ItemID: 10, Count: 100},
		{ItemID: 20, Count: 5},
		{ItemID: 10, Count: 2},
	}
	deduction := []MaterialCost{
		{ItemID: 10, Count: 30},
		{ItemID: 10, Count: 1000},
		{ItemID: 20, Count: 9},
	}
	want := []MaterialCost{
		{ItemID: 10, Count: 70},
		{ItemID: 20, Count: 0},
		{ItemID: 10, Count: 0},
	}
	if got := NetMaterials(base, deduction); !reflect.DeepEqual(got, want) {
		t.Fatalf("net = %+v want %+v", got, want)
	}
}

func TestParseSetCatalogRejectsMalformedInputs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "duplicate max point", raw: strings.Replace(setCatalogSample, "@D@ 3 105", "@D@ 3 105 @D@ 3 105", 1)},
		{name: "unknown grade", raw: strings.Replace(setCatalogSample, "@D@ 3 105", "@E@ 3 105", 1)},
		{name: "duplicate spend", raw: strings.Replace(setCatalogSample, "1 50 2 60", "1 50 1 60", 1)},
		{name: "spend above int32", raw: strings.Replace(setCatalogSample, "1 50", "1 9999999999", 1)},
		{name: "max point above int32", raw: strings.Replace(setCatalogSample, "@A@ 3 150", "@A@ 3 9999999999", 1)},
		{name: "material rate float32 overflow", raw: strings.Replace(setCatalogSample, "2.20", "1e300", 1)},
		{name: "discount rate float32 overflow", raw: strings.Replace(setCatalogSample, "0.90", "1e300", 1)},
		{name: "zero group index", raw: strings.Replace(setCatalogSample, "1 2 3", "0 2 3", 1)},
		{name: "missing spend", raw: strings.Replace(setCatalogSample, "[spend grade point]\n1 50 2 60 3 70 11 150\n[/spend grade point]", "", 1)},
		{name: "unknown trailing", raw: setCatalogSample + "\n[unknown]\n1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSetCatalog(setCatalogText(tc.raw)); !errors.Is(err, ErrSetCatalog) {
				t.Fatalf("error = %v want ErrSetCatalog", err)
			}
		})
	}
}
