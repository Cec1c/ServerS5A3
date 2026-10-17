package dungeon

import (
	"errors"
	"reflect"
	"testing"
)

func TestGriefHighestClearedDefaultsToZero(t *testing.T) {
	cases := map[string]map[string]int64{
		"nil map":       nil,
		"empty map":     {},
		"unrelated key": {"dungeon_settlement_receipt_resolved": 1},
		"explicit zero": {GriefHighestClearedStatKey: 0},
	}
	for name, stats := range cases {
		t.Run(name, func(t *testing.T) {
			highest, err := GriefHighestCleared(stats)
			if err != nil {
				t.Fatalf("highest=%d err=%v, want 0 and no error", highest, err)
			}
			if highest != 0 {
				t.Fatalf("highest=%d, want 0", highest)
			}
			next, err := GriefNextFloor(stats)
			if err != nil || next != 1 {
				t.Fatalf("next=%d err=%v, want 1 and no error", next, err)
			}
		})
	}
}

func TestGriefHighestClearedAcceptsFloorBounds(t *testing.T) {
	for _, value := range []int64{0, 1, 50, 100} {
		stats := map[string]int64{GriefHighestClearedStatKey: value}
		highest, err := GriefHighestCleared(stats)
		if err != nil || highest != int(value) {
			t.Fatalf("value=%d highest=%d err=%v", value, highest, err)
		}
	}
}

func TestGriefHighestClearedRejectsIllegalValues(t *testing.T) {
	for _, value := range []int64{-1, 101, 1000, -100} {
		stats := map[string]int64{GriefHighestClearedStatKey: value}
		if highest, err := GriefHighestCleared(stats); !errors.Is(err, ErrSettlementStateInvalid) {
			t.Fatalf("value=%d highest=%d err=%v, want ErrSettlementStateInvalid", value, highest, err)
		}
		if next, err := GriefNextFloor(stats); !errors.Is(err, ErrSettlementStateInvalid) {
			t.Fatalf("value=%d next=%d err=%v, want ErrSettlementStateInvalid", value, next, err)
		}
	}
}

func TestGriefNextFloorAdvancesByOneAndRestartsAfterHundred(t *testing.T) {
	cases := map[int64]int{0: 1, 1: 2, 98: 99, 99: 100, 100: 1}
	for highest, want := range cases {
		stats := map[string]int64{GriefHighestClearedStatKey: highest}
		next, err := GriefNextFloor(stats)
		if err != nil {
			t.Fatalf("highest=%d next=%d err=%v", highest, next, err)
		}
		if next != want {
			t.Fatalf("highest=%d next=%d, want %d", highest, next, want)
		}
	}
}

func TestApplyGriefClearAdvancesThroughAllHundredFloors(t *testing.T) {
	stats := map[string]int64{
		"dungeon_settlement_receipt_resolved": 1,
		"unrelated":                           -7,
	}
	for floor := 1; floor <= 100; floor++ {
		next, err := GriefNextFloor(stats)
		if err != nil {
			t.Fatalf("floor=%d next err=%v", floor, err)
		}
		if next != floor {
			t.Fatalf("floor=%d next=%d, want the same floor", floor, next)
		}
		if err := ApplyGriefClear(stats, floor); err != nil {
			t.Fatalf("floor=%d clear err=%v", floor, err)
		}
		if got := stats[GriefHighestClearedStatKey]; got != int64(floor) {
			t.Fatalf("floor=%d highest=%d, want %d", floor, got, floor)
		}
		if err := ApplyGriefClear(stats, floor); err != nil {
			t.Fatalf("floor=%d replay err=%v", floor, err)
		}
		wantNext := floor%100 + 1
		if next, err := GriefNextFloor(stats); err != nil || next != wantNext {
			t.Fatalf("floor=%d next=%d err=%v, want %d", floor, next, err, wantNext)
		}
	}
	if stats["dungeon_settlement_receipt_resolved"] != 1 || stats["unrelated"] != -7 {
		t.Fatalf("unrelated stats changed: %v", stats)
	}
}

func TestApplyGriefClearReplaysAndRejectsSkippedFloorsWithoutMutation(t *testing.T) {
	stats := map[string]int64{}
	assertGriefStateUnchanged(t, stats, func() error { return ApplyGriefClear(stats, 2) })

	stats[GriefHighestClearedStatKey] = 2
	assertGriefStateUnchanged(t, stats, func() error { return ApplyGriefClear(stats, 4) })
	if err := ApplyGriefClear(stats, 3); err != nil {
		t.Fatalf("adjacent 2->3 err=%v, want no error", err)
	}
	if got := stats[GriefHighestClearedStatKey]; got != 3 {
		t.Fatalf("highest=%d, want 3", got)
	}

	for _, floor := range []int{1, 2, 3} {
		assertGriefReplayUnchanged(t, stats, func() error { return ApplyGriefClear(stats, floor) })
	}
	stats[GriefHighestClearedStatKey] = 100
	assertGriefReplayUnchanged(t, stats, func() error { return ApplyGriefClear(stats, 100) })
	assertGriefStateUnchanged(t, stats, func() error { return ApplyGriefClear(stats, 2) })
	if err := ApplyGriefClear(stats, 1); err != nil || stats[GriefHighestClearedStatKey] != 1 {
		t.Fatalf("new circuit floor one failed: %v %v", stats, err)
	}
}

func TestApplyGriefClearRejectsNilIllegalFloorsAndCorruptStateWithoutMutation(t *testing.T) {
	if err := ApplyGriefClear(nil, 1); !errors.Is(err, ErrSettlementStateInvalid) {
		t.Fatalf("nil stats err=%v, want ErrSettlementStateInvalid", err)
	}

	for _, floor := range []int{0, -1, 101, 1000} {
		stats := map[string]int64{GriefHighestClearedStatKey: 50, "unrelated": 9}
		assertGriefStateUnchanged(t, stats, func() error { return ApplyGriefClear(stats, floor) })
	}
	for _, corrupt := range []int64{-1, 101} {
		stats := map[string]int64{GriefHighestClearedStatKey: corrupt, "unrelated": 9}
		assertGriefStateUnchanged(t, stats, func() error { return ApplyGriefClear(stats, 1) })
	}
}

func TestApplyGriefClearKeepsBothTowerProgressIndependent(t *testing.T) {
	stats := map[string]int64{
		GriefHighestClearedStatKey:            2,
		DespairHighestClearedStatKey:          4,
		"dungeon_settlement_receipt_resolved": 1,
	}
	if err := ApplyGriefClear(stats, 3); err != nil {
		t.Fatalf("grief clear 3 err=%v", err)
	}
	if stats[GriefHighestClearedStatKey] != 3 || stats[DespairHighestClearedStatKey] != 4 {
		t.Fatalf("after grief clear stats=%v, want grief=3 despair=4", stats)
	}
	if err := ApplyDespairClear(stats, 5); err != nil {
		t.Fatalf("despair clear 5 err=%v", err)
	}
	if stats[GriefHighestClearedStatKey] != 3 || stats[DespairHighestClearedStatKey] != 5 {
		t.Fatalf("after despair clear stats=%v, want grief=3 despair=5", stats)
	}
	if stats["dungeon_settlement_receipt_resolved"] != 1 {
		t.Fatalf("unrelated stat changed: %v", stats)
	}
}

func assertGriefStateUnchanged(t *testing.T, stats map[string]int64, apply func() error) {
	t.Helper()
	before := make(map[string]int64, len(stats))
	for key, value := range stats {
		before[key] = value
	}
	if err := apply(); !errors.Is(err, ErrSettlementStateInvalid) {
		t.Fatalf("err=%v, want ErrSettlementStateInvalid", err)
	}
	if !reflect.DeepEqual(stats, before) {
		t.Fatalf("stats=%v, want unchanged %v", stats, before)
	}
}

func assertGriefReplayUnchanged(t *testing.T, stats map[string]int64, apply func() error) {
	t.Helper()
	before := make(map[string]int64, len(stats))
	for key, value := range stats {
		before[key] = value
	}
	if err := apply(); err != nil {
		t.Fatalf("replay err=%v, want no error", err)
	}
	if !reflect.DeepEqual(stats, before) {
		t.Fatalf("stats=%v, want unchanged %v", stats, before)
	}
}
