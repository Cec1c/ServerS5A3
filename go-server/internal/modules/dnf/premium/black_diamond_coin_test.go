package premium

import (
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func blackDiamondTestAccount(now time.Time) dnfrepo.AccountRecord {
	return dnfrepo.AccountRecord{AccountID: "account", Metadata: map[string]string{
		MetadataKey(TypeBlackDiamond): strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
	}}
}

func blackDiamondTestCharacter() *dnfrepo.CharacterRecord {
	return &dnfrepo.CharacterRecord{CharacterID: "7", AccountID: "account", Stats: map[string]int64{"keep": 9}}
}

func TestAccumulateBlackDiamondFatigueThresholdAndDailyCap(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	account := blackDiamondTestAccount(now)

	for _, tc := range []struct {
		name       string
		first      int64
		second     int64
		wantCredit int64
		wantSpent  int64
	}{
		{name: "149", first: 149, wantCredit: 0, wantSpent: 149},
		{name: "150", first: 150, wantCredit: 1, wantSpent: 150},
		{name: "151", first: 151, wantCredit: 1, wantSpent: 151},
		{name: "300 in one call", first: 300, wantCredit: 1, wantSpent: 300},
		{name: "cross by chunk", first: 149, second: 1, wantCredit: 1, wantSpent: 150},
		{name: "300 in two calls", first: 150, second: 150, wantCredit: 1, wantSpent: 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			character := blackDiamondTestCharacter()
			if err := AccumulateBlackDiamondFatigue(character, account, tc.first, now); err != nil {
				t.Fatal(err)
			}
			if tc.second > 0 {
				if err := AccumulateBlackDiamondFatigue(character, account, tc.second, now); err != nil {
					t.Fatal(err)
				}
			}
			if got := character.Stats[BlackDiamondFatigueCreditedTotalKey]; got != tc.wantCredit {
				t.Fatalf("credited=%d want=%d", got, tc.wantCredit)
			}
			if got := character.Stats[BlackDiamondFatigueSpentKey]; got != tc.wantSpent {
				t.Fatalf("spent=%d want=%d", got, tc.wantSpent)
			}
			if got := character.Stats["keep"]; got != 9 {
				t.Fatalf("unrelated stat changed: %d", got)
			}
		})
	}
}

func TestAccumulateBlackDiamondFatigueResetsOnlyAt0600AndKeepsPending(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	before := time.Date(2026, time.November, 14, 5, 59, 59, 0, zone)
	atBoundary := time.Date(2026, time.November, 14, 6, 0, 0, 0, zone)
	account := dnfrepo.AccountRecord{AccountID: "account", Metadata: map[string]string{
		MetadataKey(TypeBlackDiamond): "2000000000",
	}}
	character := blackDiamondTestCharacter()

	if err := AccumulateBlackDiamondFatigue(character, account, 149, before); err != nil {
		t.Fatal(err)
	}
	if err := AccumulateBlackDiamondFatigue(character, account, 1, before); err != nil {
		t.Fatal(err)
	}
	if character.Stats[BlackDiamondFatigueCreditedTotalKey] != 1 {
		t.Fatal("threshold before rollover did not credit")
	}
	character.Stats[BlackDiamondFatigueDeliveredTotalKey] = 0
	character.Stats["other_day_counter"] = 5

	if err := AccumulateBlackDiamondFatigue(character, account, 150, atBoundary); err != nil {
		t.Fatal(err)
	}
	if got := character.Stats[BlackDiamondFatigueSpentKey]; got != 150 {
		t.Fatalf("new-day spent=%d want=150", got)
	}
	if got := character.Stats[BlackDiamondFatigueCreditedTotalKey]; got != 2 {
		t.Fatalf("credited total=%d want=2", got)
	}
	if got := character.Stats[BlackDiamondFatigueDeliveredTotalKey]; got != 0 {
		t.Fatalf("pending across rollover was lost: delivered=%d", got)
	}
	if got := character.Stats["other_day_counter"]; got != 5 {
		t.Fatalf("rollover cleared unrelated stats: %d", got)
	}
	if got := character.Stats[BlackDiamondFatigueDayKey]; got != ServiceDayID(atBoundary) {
		t.Fatalf("day=%d want=%d", got, ServiceDayID(atBoundary))
	}
}

func TestAccumulateBlackDiamondFatigueExpiredAndOwnershipDoNotWrite(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	character := blackDiamondTestCharacter()
	before := map[string]int64{"keep": 9}
	for _, account := range []dnfrepo.AccountRecord{
		{AccountID: "account"},
		{AccountID: "account", Metadata: map[string]string{MetadataKey(TypeBlackDiamond): strconv.FormatInt(now.Unix()-1, 10)}},
	} {
		character.Stats = before
		if err := AccumulateBlackDiamondFatigue(character, account, 500, now); err != nil {
			t.Fatal(err)
		}
		if len(character.Stats) != 1 || character.Stats["keep"] != 9 {
			t.Fatalf("inactive black diamond wrote stats: %+v", character.Stats)
		}
	}

	account := blackDiamondTestAccount(now)
	foreign := dnfrepo.AccountRecord{AccountID: "foreign", Metadata: account.Metadata}
	if err := AccumulateBlackDiamondFatigue(character, foreign, 1, now); !errors.Is(err, ErrBlackDiamondFatigueInvalid) {
		t.Fatalf("owner mismatch error=%v", err)
	}
	if len(character.Stats) != 1 || character.Stats["keep"] != 9 {
		t.Fatalf("owner mismatch wrote stats: %+v", character.Stats)
	}
}

func TestAccumulateBlackDiamondFatigueRejectsNegativeAndOverflowState(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	account := dnfrepo.AccountRecord{AccountID: "account", Metadata: map[string]string{
		MetadataKey(TypeBlackDiamond): "2000000000",
	}}
	character := blackDiamondTestCharacter()
	if err := AccumulateBlackDiamondFatigue(character, account, -1, now); !errors.Is(err, ErrBlackDiamondFatigueInvalid) {
		t.Fatalf("negative consumed error=%v", err)
	}

	for _, tc := range []struct {
		name  string
		stats map[string]int64
	}{
		{name: "negative spent", stats: map[string]int64{BlackDiamondFatigueSpentKey: -1}},
		{name: "negative credited", stats: map[string]int64{BlackDiamondFatigueCreditedTotalKey: -1}},
		{name: "negative delivered", stats: map[string]int64{BlackDiamondFatigueDeliveredTotalKey: -1}},
		{name: "delivered above credited", stats: map[string]int64{BlackDiamondFatigueCreditedTotalKey: 1, BlackDiamondFatigueDeliveredTotalKey: 2}},
		{name: "spent overflow", stats: map[string]int64{
			BlackDiamondFatigueDayKey: ServiceDayID(now), BlackDiamondFatigueSpentKey: math.MaxInt64,
		}},
		{name: "credited overflow at threshold", stats: map[string]int64{
			BlackDiamondFatigueDayKey: ServiceDayID(now), BlackDiamondFatigueSpentKey: 149,
			BlackDiamondFatigueCreditedTotalKey: math.MaxInt64,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			character.Stats = tc.stats
			if err := AccumulateBlackDiamondFatigue(character, account, 1, now); !errors.Is(err, ErrBlackDiamondFatigueState) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAccumulateBlackDiamondFatigueReplayNeedsCallerReceipt(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	account := dnfrepo.AccountRecord{AccountID: "account", Metadata: map[string]string{
		MetadataKey(TypeBlackDiamond): "2000000000",
	}}
	character := blackDiamondTestCharacter()
	for i := 0; i < 2; i++ {
		if err := AccumulateBlackDiamondFatigue(character, account, 151, now); err != nil {
			t.Fatal(err)
		}
	}
	// The helper cannot reconstruct a fatigue receipt from a bare amount. A
	// duplicate call inflates spent but the one-per-day credit cap still holds;
	// callers must use their durable receipt to avoid even that inflation.
	if got := character.Stats[BlackDiamondFatigueSpentKey]; got != 302 {
		t.Fatalf("spent after duplicate calls=%d want=302", got)
	}
	if got := character.Stats[BlackDiamondFatigueCreditedTotalKey]; got != 1 {
		t.Fatalf("daily credit cap failed: %d", got)
	}
}
