package dungeon

import (
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestDimensionHiddenThursdayResetPreservesProbabilityAndOtherCharacters(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	before := time.Date(2026, 10, 29, 5, 59, 59, 0, zone)
	boundary := before.Add(time.Second)
	char := dnfrepo.CharacterRecord{CharacterID: "19", Stats: map[string]int64{
		dimensionHiddenSequenceKey: 9, dimensionHiddenPityKey: 7,
		dimensionHiddenWeekKey: DimensionHiddenWeekID(before), dimensionHiddenUsedKey: 2,
	}}
	a, _ := DimensionHiddenSnapshot(char, before)
	b, _ := DimensionHiddenSnapshot(char, boundary)
	if b.WeeklyAppearances != 0 || a.WeeklyAppearances != 2 || b.EntriesSinceEncounter != 7 || b.Sequence != 9 {
		t.Fatalf("before=%+v after=%+v", a, b)
	}
	if DimensionHiddenWeekID(before.Add(-24*time.Hour)) != DimensionHiddenWeekID(before) {
		t.Fatal("Wednesday reset")
	}
	if DimensionHiddenWeekID(boundary.Add(6*24*time.Hour)) != DimensionHiddenWeekID(boundary) {
		t.Fatal("week ended early")
	}
	for _, year := range []int{2025, 2026, 2027} {
		day := time.Date(year, 1, 1, 6, 0, 0, 0, zone)
		for day.Weekday() != time.Thursday {
			day = day.Add(24 * time.Hour)
		}
		if DimensionHiddenWeekID(day) == DimensionHiddenWeekID(day.Add(-time.Second)) {
			t.Fatalf("year %d boundary missing", year)
		}
	}
	other, _ := DimensionHiddenSnapshot(dnfrepo.CharacterRecord{CharacterID: "20"}, before)
	if other != (DimensionHiddenState{}) {
		t.Fatalf("other character inherited %+v", other)
	}
	if char.Stats[dimensionHiddenUsedKey] != 2 {
		t.Fatal("read mutated persisted old week")
	}
}

func TestDimensionEntranceProjectionUsesUnspentExtraAndServiceDay(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		used, granted int64
		want          DimensionEntranceCount
	}{
		{0, 0, DimensionEntranceCount{0, 0, 2}},
		{4, 0, DimensionEntranceCount{4, 0, 2}},
		{5, 3, DimensionEntranceCount{5, 3, 2}},
		{7, 5, DimensionEntranceCount{5, 3, 2}},
		{10, 5, DimensionEntranceCount{5, 0, 2}},
	} {
		char := dnfrepo.CharacterRecord{Stats: map[string]int64{
			dimensionGateDayKey: premium.ServiceDayID(now), dimensionGateUsedKey: tc.used, dimensionGateExtraKey: tc.granted,
			dimensionHiddenWeekKey: DimensionHiddenWeekID(now), dimensionHiddenUsedKey: 2,
		}}
		got, err := DimensionGateEntranceProjection(char, now, 5, 5, 2)
		if err != nil || got != tc.want {
			t.Fatalf("used=%d granted=%d got=%+v err=%v", tc.used, tc.granted, got, err)
		}
		got, err = DimensionGateEntranceProjection(char, now.Add(24*time.Hour), 5, 5, 2)
		if err != nil || got != (DimensionEntranceCount{0, 0, 2}) {
			t.Fatalf("daily reset wiped weekly or retained daily: %+v %v", got, err)
		}
	}
}
