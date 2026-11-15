package secretshop

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

type deterministicRoll struct {
	values []uint32
	bounds []uint32
	index  int
}

func (r *deterministicRoll) next(bound uint32) (uint32, error) {
	r.bounds = append(r.bounds, bound)
	if r.index >= len(r.values) {
		return 0, fmt.Errorf("unexpected roll %d", r.index)
	}
	value := r.values[r.index]
	r.index++
	return value, nil
}

func TestPickNPCUsesSelectedNeutralColumnAndBoundsRolls(t *testing.T) {
	cat := loadSyntheticCatalog(t)
	tests := []struct {
		members int
		draw    uint32
		want    uint32
	}{
		{0, 0, 1000},
		{0, 9464, 1000},
		{0, 9465, 1002},
		{0, 9495, 1003},
		{0, 9995, 1004},
		{0, 9999, 1004},
		{4, 0, 1000},
		{4, 9385, 1002},
		{4, 9455, 1003},
		{4, 9995, 1004},
	}
	for _, test := range tests {
		trace := &deterministicRoll{values: []uint32{test.draw}}
		got, err := PickNPC(cat, 7146, 89, test.members, trace.next)
		if err != nil || got != test.want {
			t.Fatalf("members=%d draw=%d: got=%d want=%d err=%v", test.members, test.draw, got, test.want, err)
		}
		if !reflect.DeepEqual(trace.bounds, []uint32{10000}) {
			t.Fatalf("members=%d draw=%d bounds=%v", test.members, test.draw, trace.bounds)
		}
	}

	trace := &deterministicRoll{values: []uint32{10000}}
	if _, err := PickNPC(cat, 7146, 89, 0, trace.next); !errors.Is(err, ErrInvalidRoll) {
		t.Fatalf("out-of-bound roll error %v", err)
	}
	if _, err := PickNPC(cat, 7146, 89, 5, trace.next); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("invalid column error %v", err)
	}
	if _, err := PickNPC(cat, 7146, 89, 0, nil); !errors.Is(err, ErrInvalidRoll) {
		t.Fatalf("nil roll error %v", err)
	}
	rollErr := errors.New("roll source failed")
	if _, err := PickNPC(cat, 7146, 89, 0, func(uint32) (uint32, error) { return 0, rollErr }); !errors.Is(err, rollErr) {
		t.Fatalf("roll source error %v", err)
	}
	if _, err := PickNPC(nil, 7146, 89, 0, trace.next); !errors.Is(err, ErrSelection) {
		t.Fatalf("nil catalog error %v", err)
	}

	overflow := &Catalog{dungeonWeights: map[uint32][]NPCWeight{
		7146: {
			{NPCID: 1002, Weights: [5]uint32{math.MaxUint32, 0, 0, 0, 0}},
			{NPCID: 1003, Weights: [5]uint32{1, 0, 0, 0, 0}},
		},
	}}
	if _, err := PickNPC(overflow, 7146, 89, 0, trace.next); !errors.Is(err, ErrWeightOverflow) {
		t.Fatalf("overflowing NPC weights error %v", err)
	}
	zero := &Catalog{dungeonWeights: map[uint32][]NPCWeight{
		7146: {{NPCID: 1000, Weights: [5]uint32{}}},
	}}
	if _, err := PickNPC(zero, 7146, 89, 0, trace.next); !errors.Is(err, ErrSelectionExhausted) {
		t.Fatalf("zero NPC weights error %v", err)
	}
}

func TestRollOffersSelectsNormalAndExtraKeepsCountsAndAdjustsPrices(t *testing.T) {
	cat := loadSyntheticCatalog(t)
	trace := &deterministicRoll{values: []uint32{0, 21, 0, 0, 0, 20, 10, 20}}
	got, err := RollOffers(cat, 1002, 7146, 89, true, nil, trace.next)
	if err != nil {
		t.Fatal(err)
	}
	want := []Selected{
		{Candidate: Candidate{ItemID: 200, CostMode: 0, Price: 63, CostItemID: 0, Count: 1, Weight: 12}},
		{Candidate: Candidate{ItemID: 201, CostMode: 1, Price: 104, CostItemID: 4, Count: 5, Weight: 22}},
		{Candidate: Candidate{ItemID: 210, CostMode: 0, Price: 80, CostItemID: 0, Count: 1, Weight: 13}, Extra: true},
		{Candidate: Candidate{ItemID: 211, CostMode: 1, Price: 115, CostItemID: 5, Count: 6, Weight: 23}, Extra: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("offers=%+v want=%+v", got, want)
	}
	if !reflect.DeepEqual(trace.bounds, []uint32{66, 54, 36, 23, 21, 21, 21, 21}) {
		t.Fatalf("offer roll bounds=%v", trace.bounds)
	}

	trace = &deterministicRoll{values: []uint32{0, 21, 0, 20}}
	got, err = RollOffers(cat, 1002, 7146, 89, false, nil, trace.next)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want[:2]) || !reflect.DeepEqual(trace.bounds, []uint32{66, 54, 21, 21}) {
		t.Fatalf("non-black offers=%+v bounds=%v", got, trace.bounds)
	}

	calls := 0
	trace = &deterministicRoll{values: []uint32{0, 0, 0, 0, 10, 10, 10, 10}}
	got, err = RollOffers(cat, 1002, 7146, 89, true, func(candidate Candidate) bool {
		calls++
		return candidate.ItemID != 201
	}, trace.next)
	if err != nil {
		t.Fatal(err)
	}
	want = []Selected{
		{Candidate: Candidate{ItemID: 200, CostMode: 0, Price: 70, Count: 1, Weight: 12}},
		{Candidate: Candidate{ItemID: 202, CostMode: 0, Price: 145, Count: 6, Weight: 32}},
		{Candidate: Candidate{ItemID: 210, CostMode: 0, Price: 80, Count: 1, Weight: 13}, Extra: true},
		{Candidate: Candidate{ItemID: 211, CostMode: 1, Price: 105, CostItemID: 5, Count: 6, Weight: 23}, Extra: true},
	}
	if !reflect.DeepEqual(got, want) || calls != 5 {
		t.Fatalf("filtered offers=%+v calls=%d", got, calls)
	}
	if !reflect.DeepEqual(trace.bounds, []uint32{44, 32, 36, 23, 21, 21, 21, 21}) {
		t.Fatalf("filtered offer roll bounds=%v", trace.bounds)
	}
}

func selectionTestCatalog(normal, extra Pool) *Catalog {
	return &Catalog{
		StackablePercent:      200,
		ExtraStackablePercent: 100,
		pools: map[uint32]*npcPools{
			1002: {
				level:       make(map[uint32]Pool),
				cash:        make(map[uint32]Pool),
				dungeon:     map[uint32]Pool{7146: normal},
				dungeonCash: map[uint32]Pool{7146: extra},
			},
		},
	}
}

func TestRollOffersDeduplicatesItemIDsAndEnforcesBudgets(t *testing.T) {
	normalA := Candidate{ItemID: 9001, Price: 111, Count: 1, Weight: 1}
	extraA := Candidate{ItemID: 9001, Price: 999, Count: 9, Weight: 99}
	extraC := Candidate{ItemID: 9003, Price: 333, Count: 7, Weight: 1}
	cat := selectionTestCatalog(
		Pool{SelectionCount: 1, Candidates: []Candidate{normalA}},
		Pool{SelectionCount: 1, Candidates: []Candidate{extraA, extraC}},
	)
	trace := &deterministicRoll{values: []uint32{0, 0}}
	got, err := RollOffers(cat, 1002, 7146, 89, true, nil, trace.next)
	if err != nil {
		t.Fatal(err)
	}
	want := []Selected{
		{Candidate: normalA},
		{Candidate: extraC, Extra: true},
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(trace.bounds, []uint32{1, 1}) {
		t.Fatalf("deduplicated offers=%+v bounds=%v", got, trace.bounds)
	}
	if got[0].Count != normalA.Count || got[1].Count != extraC.Count || got[0].Extra || !got[1].Extra {
		t.Fatalf("candidate values or source flags changed: %+v", got)
	}

	exhausted := selectionTestCatalog(
		Pool{SelectionCount: 1, Candidates: []Candidate{normalA}},
		Pool{SelectionCount: 1, Candidates: []Candidate{extraA}},
	)
	trace = &deterministicRoll{values: []uint32{0}}
	if _, err := RollOffers(exhausted, 1002, 7146, 89, true, nil, trace.next); !errors.Is(err, ErrSelectionExhausted) {
		t.Fatalf("duplicate-only extra pool error %v", err)
	}

	tooMany := selectionTestCatalog(
		Pool{SelectionCount: 5, Candidates: []Candidate{normalA}},
		Pool{SelectionCount: 4, Candidates: []Candidate{extraC}},
	)
	trace = &deterministicRoll{}
	if _, err := RollOffers(tooMany, 1002, 7146, 89, true, nil, trace.next); !errors.Is(err, ErrSelection) {
		t.Fatalf("nine-row request error %v", err)
	}
	if len(trace.bounds) != 0 {
		t.Fatalf("nine-row request consumed rolls: %v", trace.bounds)
	}

	overflow := selectionTestCatalog(
		Pool{SelectionCount: 1, Candidates: []Candidate{
			{ItemID: 9100, Price: 100, Weight: math.MaxUint32},
			{ItemID: 9101, Price: 100, Weight: 1},
		}},
		Pool{},
	)
	if _, err := RollOffers(overflow, 1002, 7146, 89, false, nil, trace.next); !errors.Is(err, ErrWeightOverflow) {
		t.Fatalf("overflowing offer weights error %v", err)
	}

	empty := selectionTestCatalog(
		Pool{SelectionCount: 1},
		Pool{},
	)
	if _, err := RollOffers(empty, 1002, 7146, 89, false, nil, trace.next); !errors.Is(err, ErrSelectionExhausted) {
		t.Fatalf("empty pool error %v", err)
	}

	for _, price := range []int64{-1, 0, math.MaxInt32 + 1} {
		badPrice := selectionTestCatalog(
			Pool{SelectionCount: 1, Candidates: []Candidate{{ItemID: 9200, Price: price, Weight: 1}}},
			Pool{},
		)
		if _, err := RollOffers(badPrice, 1002, 7146, 89, false, nil, trace.next); !errors.Is(err, ErrSelection) {
			t.Fatalf("invalid candidate price %d error %v", price, err)
		}
	}

	rollErr := errors.New("offer roll failed")
	if _, err := RollOffers(cat, 1002, 7146, 89, true, nil, func(uint32) (uint32, error) { return 0, rollErr }); !errors.Is(err, rollErr) {
		t.Fatalf("offer roll source error %v", err)
	}
	if _, err := RollOffers(cat, 1002, 7146, 89, true, nil, func(bound uint32) (uint32, error) { return bound, nil }); !errors.Is(err, ErrInvalidRoll) {
		t.Fatalf("offer out-of-bound error %v", err)
	}

	got, err = RollOffers(loadSyntheticCatalog(t), DefaultNPCID, 7146, 89, true, nil, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("default NPC offers=%+v err=%v", got, err)
	}
}

func TestAdjustPriceInclusivePercentDeviationAndOverflow(t *testing.T) {
	cat := loadSyntheticCatalog(t)
	tests := []struct {
		price int64
		draw  uint32
		want  int64
	}{
		{1000, 0, 900},
		{1000, 10, 1000},
		{1000, 20, 1100},
		{105, 0, 95},
		{1, 0, 1},
	}
	for _, test := range tests {
		trace := &deterministicRoll{values: []uint32{test.draw}}
		got, err := AdjustPrice(cat, test.price, trace.next)
		if err != nil || got != test.want {
			t.Fatalf("price=%d draw=%d: got=%d want=%d err=%v", test.price, test.draw, got, test.want, err)
		}
		if !reflect.DeepEqual(trace.bounds, []uint32{21}) {
			t.Fatalf("price=%d draw=%d bounds=%v", test.price, test.draw, trace.bounds)
		}
	}

	if _, err := AdjustPrice(cat, 0, nil); !errors.Is(err, ErrPriceAdjustment) {
		t.Fatalf("zero price error %v", err)
	}
	if got, err := AdjustPrice(&Catalog{}, 123, nil); err != nil || got != 123 {
		t.Fatalf("zero percent price got=%d err=%v", got, err)
	}
	if _, err := AdjustPrice(cat, -1, nil); !errors.Is(err, ErrPriceAdjustment) {
		t.Fatalf("negative price error %v", err)
	}
	if _, err := AdjustPrice(&Catalog{PriceRandomPercent: -1}, 100, nil); !errors.Is(err, ErrPriceAdjustment) {
		t.Fatalf("negative percent error %v", err)
	}
	if _, err := AdjustPrice(&Catalog{PriceRandomPercent: 100}, 100, nil); !errors.Is(err, ErrPriceAdjustment) {
		t.Fatalf("100 percent error %v", err)
	}
	if _, err := AdjustPrice(cat, math.MaxInt32+1, nil); !errors.Is(err, ErrPriceAdjustment) {
		t.Fatalf("above-int32 price error %v", err)
	}
	trace := &deterministicRoll{values: []uint32{10}}
	if got, err := AdjustPrice(cat, math.MaxInt32, trace.next); err != nil || got != math.MaxInt32 {
		t.Fatalf("maximum unchanged price got=%d err=%v", got, err)
	}
	trace = &deterministicRoll{values: []uint32{20}}
	if _, err := AdjustPrice(&Catalog{PriceRandomPercent: 10}, math.MaxInt32, trace.next); !errors.Is(err, ErrPriceAdjustment) {
		t.Fatalf("overflowing adjusted price error %v", err)
	}
	trace = &deterministicRoll{values: []uint32{21}}
	if _, err := AdjustPrice(cat, 1000, trace.next); !errors.Is(err, ErrInvalidRoll) {
		t.Fatalf("out-of-bound price roll error %v", err)
	}
	rollErr := errors.New("price roll failed")
	if _, err := AdjustPrice(cat, 1000, func(uint32) (uint32, error) { return 0, rollErr }); !errors.Is(err, rollErr) {
		t.Fatalf("price roll source error %v", err)
	}
}
