package quest

import (
	"errors"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func TestSeekerPercentMalformedLevelCannotUseLegacyDefault(t *testing.T) {
	for _, tc := range []struct {
		name, section string
		legacyMin     int
	}{
		{"missing", "", 1},
		{"empty", "[level]\n", 1},
		{"text", "[level]\n`bad`\n", 1},
		{"mixed", "[level]\n`bad` 1 99\n", 1},
		{"extra", "[level]\n1 99 100\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := dnfpvf.Parse("n_quest/source.qst", "[grade]\n`[epic]`\n"+tc.section+"[type]\n`[meet npc]`\n[int data]\n1\n")
			if err != nil {
				t.Fatal(err)
			}
			d := parseDefinition(dnfpvf.ListEntry{ID: 1, Path: "n_quest/source.qst"}, doc)
			if d.LevelMin != tc.legacyMin {
				t.Fatalf("legacy minimum changed: got %d want %d", d.LevelMin, tc.legacyMin)
			}
			if _, err := finishMainQuestExperiencePercent(finishOwnerProgression(t), d, 15); !errors.Is(err, ErrFinishMainQuestExperiencePercentLevelInvalid) {
				t.Fatalf("malformed level accepted: %+v err=%v", d, err)
			}
		})
	}
}
