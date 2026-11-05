package adventuregroup

import (
	"math"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const experienceTestDocument = "[max adventure level]\n3\n[adventure exp rate]\n0.3\n[adventure exp table]\n1 `0` 2 `100` 3 `200`\n[/adventure exp table]\n"

func TestAccountExperienceUsesModernRateLevelCostsAndRemainder(t *testing.T) {
	doc, err := pvf.Parse(AdventureSystem2018Path, experienceTestDocument)
	if err != nil {
		t.Fatal(err)
	}
	config, err := parseExperienceConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	table := &Tables{runtime: RuntimeConfig{Experience: config}}
	for _, tc := range []struct {
		earned   uint64
		level    int
		progress uint64
	}{
		{0, 1, 0}, {1, 1, 0}, {3, 1, 0}, {4, 1, 1},
		{333, 1, 99}, {334, 2, 0}, {337, 2, 1},
		{999, 2, 199}, {1000, 3, 0}, {math.MaxUint64, 3, 0},
	} {
		got, err := table.CalculateAccountExperience(tc.earned)
		if err != nil || got.ManageLevel != tc.level || got.TotalPoint != tc.progress {
			t.Fatalf("earned=%d got=%+v err=%v want level=%d progress=%d", tc.earned, got, err, tc.level, tc.progress)
		}
	}
	clone := table.Runtime()
	clone.Experience.Costs[1] = 1
	if table.runtime.Experience.Costs[1] != 100 {
		t.Fatal("clone aliases experience table")
	}
}

func TestAccountExperienceRejectsMalformedModernTable(t *testing.T) {
	for _, text := range []string{
		"[max adventure level]\n3\n",
		experienceTestDocument + "[adventure exp rate]\n0.5\n",
		strings.Replace(experienceTestDocument, "0.3", "0", 1),
		strings.Replace(experienceTestDocument, "0.3", "-0.3", 1),
		strings.Replace(experienceTestDocument, "0.3", "`0.3`", 1),
		strings.Replace(experienceTestDocument, "1 `0`", "1 `1`", 1),
		strings.Replace(experienceTestDocument, "2 `100`", "2 `0`", 1),
		strings.Replace(experienceTestDocument, "2 `100`", "3 `100`", 1),
		strings.Replace(experienceTestDocument, "3 `200`", "", 1),
		strings.Replace(experienceTestDocument, "3 `200`", "3 `18446744073709551616`", 1),
	} {
		doc, err := pvf.Parse(AdventureSystem2018Path, text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseExperienceConfig(doc); err == nil {
			t.Fatalf("accepted malformed table: %q", text)
		}
	}
	doc, _ := pvf.Parse(AdventureSystem2018Path, "[unrelated]\n1\n")
	config, err := parseExperienceConfig(doc)
	if err != nil || len(config.Costs) != 0 {
		t.Fatal("legacy fallback changed", config, err)
	}
}

func TestAccountExperienceHonorsEnabledCapWithReservedCosts(t *testing.T) {
	text := strings.Replace(experienceTestDocument, "3 `200`", "3 `200` 4 `300` 5 `400`", 1)
	doc, _ := pvf.Parse(AdventureSystem2018Path, text)
	config, err := parseExperienceConfig(doc)
	if err != nil || len(config.Costs) != 3 {
		t.Fatal(config, err)
	}
	table := &Tables{runtime: RuntimeConfig{Experience: config}}
	got, err := table.CalculateAccountExperience(math.MaxUint64)
	if err != nil || got.ManageLevel != 3 || got.TotalPoint != 0 {
		t.Fatal(got, err)
	}
	for _, invalid := range []string{"4 `0` 5 `400`", "4 `300` 6 `400`", "4 `300` 5"} {
		doc, _ := pvf.Parse(AdventureSystem2018Path, strings.Replace(text, "4 `300` 5 `400`", invalid, 1))
		if _, err := parseExperienceConfig(doc); err == nil {
			t.Fatal("invalid reserved costs accepted", invalid)
		}
	}
}
