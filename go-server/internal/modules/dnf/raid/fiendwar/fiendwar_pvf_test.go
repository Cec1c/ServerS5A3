package fiendwar

import (
	"os"
	"slices"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const fiendWarRecruitmentDocument = `[FIENDWAR MEMBER MAX]
8
[WEEKLY CLEAR COUNT]
2
[FIENDWAR START MINIMUM MEMBER]
1
[ENTRY COST]
490007844 1
[/ENTRY COST]
[DISCONN MEMBER WAIT TIME]
300
[RETRY]
1
[RETRY ITEM]
490007844 1
[PHASE]
[MEMBER MAX]
4
[CONDITION FOR RANK]
[DEAD COUNT]
0 1 0
[DEAD COUNT]
2 5 1
[DEAD COUNT]
6 9 2
[DEAD COUNT]
10 14 3
[/CONDITION FOR RANK]
[/PHASE]`

func TestFiendWarRecruitmentConfig(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"native globals", fiendWarRecruitmentDocument, true},
		{"invalid weekly limit", strings.Replace(fiendWarRecruitmentDocument, "CLEAR COUNT]\n2", "CLEAR COUNT]\n0", 1), false},
		{"missing maximum", strings.Replace(fiendWarRecruitmentDocument, "[FIENDWAR MEMBER MAX]", "[OTHER]", 1), false},
		{"minimum above capacity", strings.Replace(fiendWarRecruitmentDocument, "MINIMUM MEMBER]\n1", "MINIMUM MEMBER]\n9", 1), false},
		{"invalid ticket", strings.Replace(fiendWarRecruitmentDocument, "490007844 1", "0 1", 1), false},
		{"missing rank block", strings.ReplaceAll(fiendWarRecruitmentDocument, "CONDITION FOR RANK", "OTHER"), false},
		{"empty rank block", strings.ReplaceAll(fiendWarRecruitmentDocument, "DEAD COUNT", "OTHER"), false},
		{"invalid death range", strings.Replace(fiendWarRecruitmentDocument, "6 9 2", "9 6 2", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := dnfpvf.Parse(FiendWarPVFPath, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			config, err := ParseFiendWarConfig(doc)
			if (err == nil) != tc.valid {
				t.Fatalf("config=%+v err=%v", config, err)
			}
			if tc.valid && (config.MemberMax != 8 || config.StartMinimumMembers != 1 || config.EntryCost.ItemID != 490007844 || config.EntryCost.Count != 1 || config.DisconnectMemberWaitSeconds != 300 || !config.Retry || config.RetryItem.ItemID != 490007844 || !slices.Equal(config.DeadCount, []FiendWarDeadCountRule{{0, 1, 0}, {2, 5, 1}, {6, 9, 2}, {10, 14, 3}})) {
				t.Fatalf("config=%+v", config)
			}
		})
	}
}

func TestFiendWarDeadCountTierUsesInclusivePVFRanges(t *testing.T) {
	config := &FiendWarConfig{DeadCount: []FiendWarDeadCountRule{{Min: 0, Max: 1, Tier: 0}, {Min: 2, Max: 5, Tier: 1}}}
	for count, want := range map[int]int{0: 0, 1: 0, 2: 1, 5: 1} {
		got, ok := FiendWarDeadCountTier(config, count)
		if !ok || got != want {
			t.Fatalf("count=%d tier=%d ok=%t want=%d", count, got, ok, want)
		}
	}
	if _, ok := FiendWarDeadCountTier(config, 6); ok {
		t.Fatal("out-of-range death count returned a tier")
	}
}

func TestRealScriptPVFFiendWarRecruitment(t *testing.T) {
	path := os.Getenv("DNF_FIENDWAR_REAL_PVF")
	if path == "" {
		t.Skip("DNF_FIENDWAR_REAL_PVF not set")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadFiendWarConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	if config.MemberMax != 8 || config.StartMinimumMembers != 1 || config.EntryCost.ItemID != 490007844 || config.EntryCost.Count != 1 {
		t.Fatalf("actual PVF=%+v", config)
	}
}
