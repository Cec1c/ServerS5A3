package dungeon

import (
	"errors"
	"testing"
)

func TestDespairHighestClearedDefaultsToZero(t *testing.T) {
	cases := map[string]map[string]int64{
		"nil map":       nil,
		"empty map":     {},
		"unrelated key": {"dungeon_settlement_receipt_resolved": 1},
		"explicit zero": {DespairHighestClearedStatKey: 0},
	}
	for name, stats := range cases {
		t.Run(name, func(t *testing.T) {
			highest, err := DespairHighestCleared(stats)
			if err != nil {
				t.Fatalf("highest=%d err=%v, want 0 and no error", highest, err)
			}
			if highest != 0 {
				t.Fatalf("highest=%d, want 0", highest)
			}
			next, err := DespairNextFloor(stats)
			if err != nil || next != 1 {
				t.Fatalf("next=%d err=%v, want 1 and no error", next, err)
			}
		})
	}
}

func TestDespairHighestClearedAcceptsFloorBounds(t *testing.T) {
	for _, value := range []int64{0, 1, 50, 100} {
		stats := map[string]int64{DespairHighestClearedStatKey: value}
		highest, err := DespairHighestCleared(stats)
		if err != nil || highest != int(value) {
			t.Fatalf("value=%d highest=%d err=%v", value, highest, err)
		}
	}
}

func TestDespairHighestClearedRejectsIllegalValues(t *testing.T) {
	for _, value := range []int64{-1, 101, 1000, -100} {
		stats := map[string]int64{DespairHighestClearedStatKey: value}
		if highest, err := DespairHighestCleared(stats); !errors.Is(err, ErrSettlementStateInvalid) {
			t.Fatalf("value=%d highest=%d err=%v, want ErrSettlementStateInvalid", value, highest, err)
		}
		if next, err := DespairNextFloor(stats); !errors.Is(err, ErrSettlementStateInvalid) {
			t.Fatalf("value=%d next=%d err=%v, want ErrSettlementStateInvalid", value, next, err)
		}
	}
}

func TestDespairNextFloorAdvancesByOneAndCapsAtHundred(t *testing.T) {
	cases := map[int64]int{0: 1, 1: 2, 98: 99, 99: 100, 100: 100}
	for highest, want := range cases {
		stats := map[string]int64{DespairHighestClearedStatKey: highest}
		next, err := DespairNextFloor(stats)
		if err != nil {
			t.Fatalf("highest=%d next=%d err=%v", highest, next, err)
		}
		if next != want {
			t.Fatalf("highest=%d next=%d, want %d", highest, next, want)
		}
	}
}

func TestApplyDespairClearAdvancesAndReplaysIdempotently(t *testing.T) {
	stats := map[string]int64{}
	for _, step := range []struct {
		floor int
		want  int64
	}{{1, 1}, {1, 1}, {2, 2}, {1, 2}, {2, 2}, {3, 3}} {
		if err := ApplyDespairClear(stats, step.floor); err != nil {
			t.Fatalf("floor=%d err=%v", step.floor, err)
		}
		if got := stats[DespairHighestClearedStatKey]; got != step.want {
			t.Fatalf("floor=%d highest=%d, want %d", step.floor, got, step.want)
		}
	}
	if len(stats) != 1 {
		t.Fatalf("stats=%v, want only the despair key", stats)
	}
}

func TestApplyDespairClearRejectsSkippedFloors(t *testing.T) {
	stats := map[string]int64{}
	if err := ApplyDespairClear(stats, 2); !errors.Is(err, ErrSettlementStateInvalid) {
		t.Fatalf("first clear of floor 2 err=%v, want ErrSettlementStateInvalid", err)
	}
	if len(stats) != 0 {
		t.Fatalf("rejected skip wrote stats=%v", stats)
	}
	stats[DespairHighestClearedStatKey] = 2
	if err := ApplyDespairClear(stats, 4); !errors.Is(err, ErrSettlementStateInvalid) {
		t.Fatalf("skip 2->4 err=%v, want ErrSettlementStateInvalid", err)
	}
	if err := ApplyDespairClear(stats, 3); err != nil {
		t.Fatalf("adjacent 2->3 err=%v, want no error", err)
	}
	if got := stats[DespairHighestClearedStatKey]; got != 3 {
		t.Fatalf("highest=%d, want 3", got)
	}
}

func TestApplyDespairClearRejectsIllegalFloors(t *testing.T) {
	for _, floor := range []int{0, -1, 101, 1000} {
		stats := map[string]int64{DespairHighestClearedStatKey: 50}
		if err := ApplyDespairClear(stats, floor); !errors.Is(err, ErrSettlementStateInvalid) {
			t.Fatalf("floor=%d err=%v, want ErrSettlementStateInvalid", floor, err)
		}
		if got := stats[DespairHighestClearedStatKey]; got != 50 {
			t.Fatalf("floor=%d highest=%d, want unchanged 50", floor, got)
		}
	}
}

func TestApplyDespairClearRejectsNilAndCorruptStats(t *testing.T) {
	if err := ApplyDespairClear(nil, 1); !errors.Is(err, ErrSettlementStateInvalid) {
		t.Fatalf("nil stats err=%v, want ErrSettlementStateInvalid", err)
	}
	for _, corrupt := range []int64{-1, 101} {
		stats := map[string]int64{DespairHighestClearedStatKey: corrupt}
		if err := ApplyDespairClear(stats, 1); !errors.Is(err, ErrSettlementStateInvalid) {
			t.Fatalf("corrupt=%d err=%v, want ErrSettlementStateInvalid", corrupt, err)
		}
		if got := stats[DespairHighestClearedStatKey]; got != corrupt {
			t.Fatalf("corrupt=%d highest=%d, want unchanged", corrupt, got)
		}
	}
}

func TestApplyDespairClearKeepsUnrelatedStats(t *testing.T) {
	stats := map[string]int64{
		DespairHighestClearedStatKey:          99,
		"dungeon_settlement_receipt_resolved": 1,
		"tower_of_grief_highest_cleared":      4,
		"dungeon_settlement_lucky_star_after": -7,
	}
	before := make(map[string]int64, len(stats))
	for key, value := range stats {
		before[key] = value
	}
	if err := ApplyDespairClear(stats, 100); err != nil {
		t.Fatalf("clear floor 100 err=%v, want no error", err)
	}
	if got := stats[DespairHighestClearedStatKey]; got != 100 {
		t.Fatalf("highest=%d, want 100", got)
	}
	for key, want := range before {
		if key == DespairHighestClearedStatKey {
			continue
		}
		if got := stats[key]; got != want {
			t.Fatalf("stat %q = %d, want unchanged %d", key, got, want)
		}
	}
	if len(stats) != len(before) {
		t.Fatalf("stats=%v, want the same key set as %v", stats, before)
	}
}
