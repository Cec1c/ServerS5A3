package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFBranchSkillsNeedThePersistedGrowType 是本轮真档用例：
//
// 线上现场（2026-09-25，角色 2 菲八番茄 = job 0 剑魂，Lv40）在权威列
// `dnf_characters.grow_type` 被 A/B 实验写成 0 之后，每一次加点都被服务端拒绝
// （`skill grow type rule rejected mutation: skill=27 grow=0`），而它身上**已经学过**
// 剑魂分支技能。本用例用真档 `Script.pvf` 把这条链条钉死：
//
//  1. 分支技能（27 武器奥义 / 33 光剑掌握 / 110 流心:狂）在 grow_type=1（剑魂）下可学；
//  2. 同一个技能在 grow_type=0（未转职）下不可学（`[growtype maximum level][0] = 0`）；
//  3. 通用技能（46 上挑）在两种方向下都可学 ⇒ "未转职"不是一刀切封禁；
//  4. 角色 2 的**实际已学技能表**在 grow_type=0 下会产生非空的
//     "方向不可学"清单（= 门禁拒绝的直接证据），在 grow_type=1 下为空。
//
// opt-in（真档约 100 MB，日常套件不依赖它）：
//
//	$env:DNFBRIDGE_REAL_PVF_SMOKE = "runtime\data\dnf\Script.pvf"
func TestRealScriptPVFBranchSkillsNeedThePersistedGrowType(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf branch-grow-type probe")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}

	// 1 + 2：分支技能两个方向上的可学性。
	branchSkills := []struct {
		id   uint16
		name string
	}{
		{id: 27, name: "武器奥义"},
		{id: 33, name: "光剑掌握"},
		{id: 110, name: "流心:狂"},
	}
	for _, target := range branchSkills {
		definition, ok := catalog.Find(0, target.id)
		if !ok {
			t.Fatalf("job0 skill %d (%s) is missing from the archive", target.id, target.name)
		}
		t.Logf("job0 skill %d %s: fitness=%v second=%v max=%d growtype_max=%v second_growtype_max=%v",
			target.id, target.name, definition.GrowTypes, definition.SecondGrowTypes,
			definition.MaximumLevel, definition.GrowtypeMaximumLevels, definition.SecondGrowtypeMaximumLevels)

		transferred := definition.MaxLearnableLevel(1, 90)
		if !definition.SupportsCharacterGrowth(1) || transferred <= 0 {
			t.Fatalf("%s must be learnable for the 剑魂 direction (grow_type=1 at Lv90), got cap=%d fitness=%v",
				target.name, transferred, definition.GrowTypes)
		}
		untransferred := definition.MaxLearnableLevel(0, 90)
		if definition.SupportsCharacterGrowth(0) && untransferred > 0 {
			t.Fatalf("%s must NOT be learnable while grow_type=0 (untransferred), got cap=%d fitness=%v growtype_max=%v",
				target.name, untransferred, definition.GrowTypes, definition.GrowtypeMaximumLevels)
		}
	}

	// 3：通用技能两个方向都可学（否则"未转职"会把全部技能封死，判据就没有区分度）。
	common, ok := catalog.Find(0, 46)
	if !ok {
		t.Fatal("job0 skill 46 (上挑) is missing from the archive")
	}
	if cap := common.MaxLearnableLevel(0, 90); cap <= 0 {
		t.Fatalf("上挑 must stay learnable while untransferred, got cap=%d", cap)
	}
	if cap := common.MaxLearnableLevel(1, 90); cap <= 0 {
		t.Fatalf("上挑 must stay learnable for 剑魂, got cap=%d", cap)
	}

	// 4：用线上角色 2 的**真实已学技能表**复现现场拒绝。
	// 数据来源：香港库 `dnf_skill_states`（character_id=2，2026-09-25 取证）。
	learned := map[int64]dnfrepo.SkillState{}
	for _, skillID := range []int64{1, 3, 4, 5, 7, 8, 9, 11, 16, 17, 25, 27, 33, 37, 38, 46, 49, 67, 68, 72, 73, 98, 105, 107, 108, 109, 110, 169, 174, 179, 186, 190, 511} {
		learned[skillID] = dnfrepo.SkillState{Level: 1, Enabled: true}
	}
	atZero := dnfskill.SkillsWithUnavailableDirection(catalog, 0, 0, learned)
	atOne := dnfskill.SkillsWithUnavailableDirection(catalog, 0, 1, learned)
	t.Logf("character 2 learned=%d unavailable at grow_type=0: %v; at grow_type=1: %v",
		len(learned), atZero, atOne)
	if len(atZero) == 0 {
		t.Fatal("the live character-2 skill set must contradict grow_type=0; the incident would not reproduce")
	}
	if len(atOne) != 0 {
		t.Fatalf("the live character-2 skill set must be fully available for grow_type=1, got %v", atOne)
	}
	for _, must := range []uint16{27, 33, 110} {
		found := false
		for _, id := range atZero {
			if id == must {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("skill %d must be reported as direction-unavailable at grow_type=0, got %v", must, atZero)
		}
	}
}
