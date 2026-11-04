package drop

import (
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func TestParseDungeonMaterialRules(t *testing.T) {
	block := func(body string) string {
		return "[" + DungeonMaterialSection + "]\n" + body + "\n[/" + DungeonMaterialSection + "]"
	}
	valid := "310 10163136 0 0 0 100000\n7539 10163136 10000 10000 10000 100000"
	for _, tc := range []struct {
		name, text string
		invalid    bool
	}{
		{"absent", "[independent drop]\n[/independent drop]", false},
		{"valid", block(valid), false},
		{"disabled", block("310 10163136 0 0 0 0"), false},
		{"certain", block("310 10163136 1000000 1000000 1000000 1000000"), false},
		{"empty", block(""), true},
		{"truncated", block("310 10163136 0 0 100000"), true},
		{"fraction", block("310 10163136 0.1 0 0 100000"), true},
		{"negative", block("310 10163136 -1 0 0 100000"), true},
		{"overflow", block("310 10163136 0 0 0 1000001"), true},
		{"item_overflow", block("310 4294967296 0 0 0 1"), true},
		{"zero_dungeon", block("0 10163136 0 0 0 1"), true},
		{"duplicate_row", block(valid + "\n310 10163136 0 0 0 100000"), true},
		{"duplicate_section", block(valid) + "\n" + block(valid), true},
		{"unclosed", "[" + DungeonMaterialSection + "]\n" + valid, true},
		{"orphan_close", "[/" + DungeonMaterialSection + "]", true},
		{"nested", block("[list]\n" + valid + "\n[/list]"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := dnfpvf.Parse("etc/independent_drop.etc", tc.text)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseDungeonMaterialRules(doc)
			if (err != nil) != tc.invalid {
				t.Fatalf("rules=%v err=%v", got, err)
			}
			if err != nil && !strings.Contains(err.Error(), DungeonMaterialSection) {
				t.Fatal(err)
			}
			if tc.name == "valid" && (len(got) != 2 || got[310][0].Probability != [4]int{0, 0, 0, 100000} || got[7539][0].Probability != [4]int{10000, 10000, 10000, 100000}) {
				t.Fatal(got)
			}
		})
	}
}
