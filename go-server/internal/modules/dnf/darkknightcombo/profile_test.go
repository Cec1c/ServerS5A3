package darkknightcombo

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestFilterLearnedPagesDoesNotUnlockAlternateTreeOrRestoreRefundedSkills(t *testing.T) {
	record := dnfrepo.SkillRecord{Skills: map[int64]dnfrepo.SkillState{118: {Level: 1, Enabled: true}, 8: {Level: 1, Enabled: true}}}
	pages := []Page{{Index: 0, Roots: []Root{{SkillID: 118, Children: []uint16{8, 46}}}}, {Index: 1, Roots: []Root{{SkillID: 118, Children: []uint16{46}}}}}
	filtered := FilterLearnedPages(record, pages)
	if len(filtered) != 1 || !equalUint16s(filtered[0].Roots[0].Children, []uint16{8}) {
		t.Fatalf("未解锁页或已洗技能被投影: %+v", filtered)
	}
	record.Trees = dnfrepo.SkillTreeState{Expanded: true, Active: 1, Other: dnfrepo.SkillProfile{Skills: record.Skills}}
	record.Skills = map[int64]dnfrepo.SkillState{118: {Level: 1, Enabled: true}, 46: {Level: 1, Enabled: true}}
	filtered = FilterLearnedPages(record, pages)
	if len(filtered) != 2 || !equalUint16s(filtered[0].Roots[0].Children, []uint16{8}) || !equalUint16s(filtered[1].Roots[0].Children, []uint16{46}) {
		t.Fatalf("两套独立加点被混用: %+v", filtered)
	}
	if len(pages[0].Roots[0].Children) != 2 {
		t.Fatal("投影修改了保存页")
	}
}
