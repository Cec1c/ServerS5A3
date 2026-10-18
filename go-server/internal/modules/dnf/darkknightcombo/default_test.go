package darkknightcombo

import (
	"context"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

type comboTestSource map[string]string

func (source comboTestSource) ReadText(name string) (string, error) { return source[name], nil }

func TestBuildDefaultPagesUsesOnlyPVFLearnedSkillsAndDynamicRoots(t *testing.T) {
	source := comboTestSource{
		"skill/skilllist.lst":                 "9 `demonicswordman/skilllist.lst`\n",
		"skill/demonicswordman/skilllist.lst": "1 `attack.skl`\n100 `comboset1.skl`\n101 `comboset2.skl`\n",
		"skill/demonicswordman/attack.skl":    "[name] `attack`\n[type] `active`",
		"skill/demonicswordman/comboset1.skl": "[name] `combo 1`\n[combo set]\n1 2 3\n[/combo set]",
		"skill/demonicswordman/comboset2.skl": "[name] `combo 2`\n[combo set]\n3 4\n[/combo set]",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.SkillRecord{
		CharacterID: "77",
		Skills: map[int64]dnfrepo.SkillState{
			1: {Level: 1, Enabled: true}, 2: {Level: 1, Enabled: true},
			100: {Level: 1, Enabled: true}, 101: {Level: 1, Enabled: true},
		},
		Layouts: map[int]dnfrepo.SkillLayout{0: {0: 100, 1: 1, 2: 101, 3: 2}},
	}
	pages, err := BuildDefaultPages(source, catalog, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || len(pages[0].Roots) != 2 {
		t.Fatalf("pages=%+v", pages)
	}
	if got := pages[0].Roots[0].Children; !equalUint16s(got, []uint16{1, 2}) {
		t.Fatalf("first children=%v", got)
	}
	if got := pages[0].Roots[1].Children; len(got) != 0 {
		t.Fatalf("second children=%v", got)
	}
}

func equalUint16s(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
