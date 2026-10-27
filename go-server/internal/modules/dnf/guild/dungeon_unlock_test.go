package guild

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Structural fixture derived from the matched catalog's six unlock rules;
// reward tables are outside this evaluator's scope.
func dungeonUnlockFixture() string {
	var out strings.Builder
	for _, row := range []struct {
		id, gain, threshold int
		requires            string
	}{
		{8500, 150, 50, ""}, {8504, 75, 50, "8500"}, {8507, 50, 50, "8500"},
		{8505, 50, 50, "8504"}, {8503, 75, 50, "8507"}, {8508, 30, 100, "8503 8505"},
	} {
		fmt.Fprintf(&out, "[dungeon]\n[dungeon index]\n%d\n[gauge limit]\n10000\n[gauge on clear]\n%d\n[conquest gauge ratio]\n%d\n", row.id, row.gain, row.threshold)
		if row.requires != "" {
			fmt.Fprintf(&out, "[required dungeon conquest]\n%s\n[/required dungeon conquest]\n", row.requires)
		}
		out.WriteString("[reward]\n[gauge ratio]\n100\n[item]\n123 1\n[/gauge ratio]\n[/reward]\n[/dungeon]\n")
	}
	return out.String()
}

func checkDungeonUnlockRules(t *testing.T, text string) {
	t.Helper()
	rules, err := ParseDungeonUnlockRules(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		id     uint32
		counts map[uint32]uint32
		want   bool
	}{
		{"first", 8500, nil, true}, {"unknown", 9000, nil, false}, {"branch empty", 8504, nil, false},
		{"branch 33", 8504, map[uint32]uint32{8500: 33}, false}, {"branch 34", 8504, map[uint32]uint32{8500: 34}, true},
		{"other branch 34", 8507, map[uint32]uint32{8500: 34}, true},
		{"final missing second", 8508, map[uint32]uint32{8503: 67}, false},
		{"final second below", 8508, map[uint32]uint32{8503: 67, 8505: 99}, false},
		{"final first below", 8508, map[uint32]uint32{8503: 66, 8505: 100}, false},
		{"final both", 8508, map[uint32]uint32{8503: 67, 8505: 100}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rules.Allows(tc.id, tc.counts)
			if err != nil || got != tc.want {
				t.Fatalf("Allows=%v,%v want %v", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ id, count, ratio uint32 }{{8500, 1, 1}, {8500, 33, 49}, {8500, 34, 51}, {8500, 67, 100}, {8504, 66, 49}, {8504, 67, 50}, {8507, 99, 49}, {8507, 100, 50}, {8508, 333, 99}, {8508, 334, 100}} {
		t.Run(fmt.Sprintf("ratio-%d-%d", tc.id, tc.count), func(t *testing.T) {
			got, err := rules.PurificationRatio(tc.id, tc.count)
			if err != nil || got != tc.ratio {
				t.Fatalf("ratio=%v,%v want %d", got, err, tc.ratio)
			}
		})
	}
	if _, err = rules.PurificationRatio(8500, ^uint32(0)); err == nil {
		t.Fatal("accepted native signed multiplication overflow")
	}
	if _, err = rules.PurificationRatio(9999, 1); err == nil {
		t.Fatal("accepted unknown rule")
	}
	var absent *DungeonUnlockRules
	if open, err := absent.Allows(8500, nil); open || err != nil {
		t.Fatalf("missing catalog opened: %v %v", open, err)
	}
}

func TestDungeonUnlockRulesNativeBoundaries(t *testing.T) {
	checkDungeonUnlockRules(t, dungeonUnlockFixture())
}

func TestDungeonUnlockRulesNativeConversionLimits(t *testing.T) {
	text := strings.Replace(dungeonUnlockFixture(), "[gauge limit]\n10000", "[gauge limit]\n0", 1)
	rules, err := ParseDungeonUnlockRules(text)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rules.PurificationRatio(8500, 0); err != nil || got != 100 {
		t.Fatalf("zero limit=%d,%v", got, err)
	}
	text = strings.Replace(dungeonUnlockFixture(), "[gauge limit]\n10000", "[gauge limit]\n1", 1)
	rules, err = ParseDungeonUnlockRules(text)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rules.PurificationRatio(8500, 1000000); err == nil {
		t.Fatal("accepted native float to signed-int overflow")
	}
}

func TestDungeonUnlockRulesExtractedPVF(t *testing.T) {
	path := os.Getenv("GUILD_DUNGEON_EXTRACTED_ETC")
	if path == "" {
		t.Skip("set GUILD_DUNGEON_EXTRACTED_ETC to PVF-CLI extracted catalog 0")
	}
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	checkDungeonUnlockRules(t, string(text))
}

func TestDungeonUnlockRulesRejectMalformedCatalog(t *testing.T) {
	source := dungeonUnlockFixture()
	for name, text := range map[string]string{
		"empty": "", "unterminated": strings.Replace(source, "[/dungeon]", "", 1),
		"duplicate id":         strings.Replace(source, "8504\n[gauge limit]", "8500\n[gauge limit]", 1),
		"missing field":        strings.Replace(source, "[gauge limit]\n10000\n", "", 1),
		"unknown dependency":   strings.Replace(source, "8503 8505", "9999 8505", 1),
		"duplicate dependency": strings.Replace(source, "8503 8505", "8503 8503", 1),
		"cycle":                strings.Replace(source, "[dungeon index]\n8500", "[required dungeon conquest]\n8508\n[/required dungeon conquest]\n[dungeon index]\n8500", 1),
		"negative":             strings.Replace(source, "[gauge on clear]\n150", "[gauge on clear]\n-1", 1),
		"fractional":           strings.Replace(source, "[gauge on clear]\n150", "[gauge on clear]\n1.5", 1),
		"duplicate field":      strings.Replace(source, "[gauge on clear]\n150", "[gauge on clear]\n150\n[gauge on clear]\n150", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDungeonUnlockRules(text); err == nil {
				t.Fatal("accepted malformed rules")
			}
		})
	}
}
