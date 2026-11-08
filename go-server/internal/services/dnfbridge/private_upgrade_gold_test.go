package dnfbridge

import (
	"math"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func boolToPrivateGoldRemainder(remainder bool) int64 {
	if remainder {
		return 1
	}
	return 0
}

func TestPrivateGroupUpgradeGoldSeventh(t *testing.T) {
	for _, mode := range []string{"reinforce", "amplify"} {
		for _, tc := range []struct{ original, want int64 }{
			{0, 0}, {1, 1}, {6, 1}, {7, 1}, {8, 2},
			{84975, 12140}, {679800, 97115}, {math.MaxInt64, math.MaxInt64 / 7},
			{math.MaxInt64 - 1, math.MaxInt64 / 7},
		} {
			quote := alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: tc.original, ConversionItemID: 123, ConversionCount: 4}
			got := privateGroupUpgradeGoldQuote(mode, quote)
			want := quote
			want.GoldCost = tc.want
			if got != want {
				t.Fatalf("%s original=%d got=%+v want=%+v", mode, tc.original, got, want)
			}
		}
	}
	for _, quote := range []alignedcmd.UpgradeCostResolution{
		{Resolved: false, GoldCost: 700},
		{Resolved: true, GoldCost: -1},
		{Resolved: true, ConversionItemID: 123, ConversionCount: 7},
	} {
		if got := privateGroupUpgradeGoldQuote("reinforce", quote); got != quote {
			t.Fatalf("changed unresolved/error/material quote: %+v", got)
		}
	}
	quote := alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 700}
	if got := privateGroupUpgradeGoldQuote("other", quote); got != quote {
		t.Fatal("changed unrelated operation")
	}
}
