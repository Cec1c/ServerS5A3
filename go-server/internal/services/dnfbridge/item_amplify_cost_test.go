package dnfbridge

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"strings"
	"testing"
)

func amplifyCostFixture(t *testing.T, extra string) *currentUpgradeTable {
	t.Helper()
	text := "[cost]\n0 101 66000\n[type]\n2 0 0.7 0.7 0.7 0.7 0.7 0.7 0.7 0.7 0.7 0.7 0.7\n[cost weights by rarity]\n0.25 0.75 1.5 2.5 4 2 3\n" + extra
	doc, err := dnfpvf.Parse(currentAmplifyUpgradePVFPath, text)
	if err != nil {
		t.Fatal(err)
	}
	return parseCurrentUpgradeTable(doc, text)
}

func TestNativeAmplifyCostAbsentLevelWeightsAndRounding(t *testing.T) {
	table := amplifyCostFixture(t, "")
	for _, tc := range []struct {
		grade, rarity, kind, level int
		want                       int64
	}{
		{2, 4, 12, 0, 528000}, {2, 4, 12, 18, 528000}, {2, 4, 12, 30, 528000},
		{2, 4, 14, 10, 184800}, {2, 2, 25, 0, 69300}, {1, 3, 14, 0, 178},
		// Kind24 aliases weapon's type weight, but uses nonweapon level weights.
		{2, 4, 24, 0, 528000},
	} {
		got, err := nativeAmplifyGoldCost(table, tc.grade, tc.rarity, tc.kind, tc.level)
		if err != nil || got != tc.want {
			t.Fatalf("%+v got=%d err=%v", tc, got, err)
		}
	}
	table = amplifyCostFixture(t, "[cost weight by upgrade level]\n0 2 3\n18 4 5\n")
	for _, tc := range []struct {
		kind, level int
		want        int64
	}{{12, 0, 1056000}, {24, 0, 1584000}, {14, 0, 554400}, {12, 1, 528000}, {12, 30, 2112000}} {
		got, err := nativeAmplifyGoldCost(table, 2, 4, tc.kind, tc.level)
		if err != nil || got != tc.want {
			t.Fatalf("%+v cost=%d err=%v", tc, got, err)
		}
	}
	// Existing reinforcement still rejects missing weights, as before.
	if _, err := nativeUpgradeGoldCost(amplifyCostFixture(t, ""), 2, 4, 12, 0); err == nil {
		t.Fatal("reinforcement missing-weight policy changed")
	}
}

func TestNativeAmplifyCostRejectsMalformedTablesAndMetadata(t *testing.T) {
	for _, body := range []string{"", "0 1", "0 1 1 0 2 2", "0.5 1 1", "256 1 1", "0 -1 1", "0 `invalid` 1"} {
		table := amplifyCostFixture(t, "[cost weight by upgrade level]\n"+body+"\n")
		if _, err := nativeAmplifyGoldCost(table, 2, 4, 12, 0); err == nil {
			t.Fatalf("accepted malformed declared weights %q", body)
		}
	}
	for _, replace := range []struct{ from, to string }{{"0 101 66000", "0 101.5 66000"}, {"0 101 66000", "0 `bad` 66000"}, {"0 101 66000", "0 101 2147483648"}, {"0.25 0.75", "0.25 `bad`"}} {
		text := "[cost]\n0 101 66000\n[type]\n2\n[cost weights by rarity]\n0.25 0.75\n"
		text = strings.ReplaceAll(text, replace.from, replace.to)
		doc, err := dnfpvf.Parse(currentAmplifyUpgradePVFPath, text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := nativeAmplifyGoldCost(parseCurrentUpgradeTable(doc, text), 1, 0, 12, 0); err == nil {
			t.Fatalf("accepted malformed table %+v", replace)
		}
	}
	table := amplifyCostFixture(t, "")
	for _, args := range [][4]int{{-1, 4, 12, 0}, {3, 4, 12, 0}, {2, 7, 12, 0}, {2, 4, 11, 0}, {2, 4, 26, 0}, {2, 4, 12, -1}} {
		if _, err := nativeAmplifyGoldCost(table, args[0], args[1], args[2], args[3]); err == nil {
			t.Fatalf("accepted metadata %v", args)
		}
	}
	table.Costs[2] = 2147483647
	if _, err := nativeAmplifyGoldCost(table, 2, 4, 12, 0); err == nil {
		t.Fatal("accepted overflowing quote")
	}
}
