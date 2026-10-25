package dungeon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDimensionHiddenAdmissionsCommitWithDailyAllowanceAndRejectStalePlan(t *testing.T) {
	repos, owner, cmd := dimensionGateFixture(t)
	ctx := context.Background()
	for i, hidden := range []bool{false, true, true, true, false} {
		cmd.Hidden = &DimensionHiddenAdmission{ExpectedSequence: int64(i), Encountered: hidden, WeeklyRewardLimit: 2}
		result, err := owner.ConsumeDimensionGateEntry(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if result.HiddenHighReward != (i == 1 || i == 2) {
			t.Fatalf("entry %d high reward=%t", i, result.HiddenHighReward)
		}
		state, err := DimensionHiddenSnapshot(result.Character, cmd.UpdatedAt)
		if err != nil || state.Sequence != int64(i+1) {
			t.Fatalf("state=%+v err=%v", state, err)
		}
		before, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
		if _, err := owner.ConsumeDimensionGateEntry(ctx, cmd); err == nil {
			t.Fatal("stale frozen entry accepted")
		}
		after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("rejected stale plan changed state")
		}
	}
	stored, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	state, err := DimensionHiddenSnapshot(stored, cmd.UpdatedAt)
	if err != nil || state.WeeklyAppearances != 2 || state.EntriesSinceEncounter != 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	// An exhausted daily budget cannot burn the highest reward or probability.
	cmd.Hidden = &DimensionHiddenAdmission{ExpectedSequence: 5, Encountered: true, WeeklyRewardLimit: 2}
	if _, err := owner.ConsumeDimensionGateEntry(ctx, cmd); err == nil {
		t.Fatal("sixth entry accepted")
	}
	after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	if !reflect.DeepEqual(stored, after) {
		t.Fatal("denied daily entry changed hidden state")
	}
}

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
