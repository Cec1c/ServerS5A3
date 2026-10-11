package skill

import (
	"context"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// directionAuditTable 造一个 job 0 的三技能目录：
//
//	27  武器奥义   —— 剑魂分支专属：`[skill fitness growtype] 1`、`[growtype maximum level] 0 30 0…`
//	46  上挑       —— 全分支通用：fitness 0..5
//	174 基础精通   —— 全分支通用（passive）
func directionAuditTable(t *testing.T) *Table {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), textSource{
		"skill/skilllist.lst": "0 `job0.lst`\n",
		"skill/job0.lst": "27 `job0/weaponmastery.skl`\n" +
			"46 `job0/upperslash.skl`\n" +
			"174 `job0/basicattackup.skl`\n",
		"skill/job0/weaponmastery.skl": `
[type]
` + "`[passive]`" + `
[required level]
10
[maximum level]
30
[skill fitness growtype]
1
[/skill fitness growtype]
[growtype maximum level]
0 30 0 0 0 0
[purchase cost]
20
[/purchase cost]
`,
		"skill/job0/upperslash.skl": `
[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
20
[skill fitness growtype]
0 1 2 3 4 5
[/skill fitness growtype]
[growtype maximum level]
10 10 10 10 10 10
[purchase cost]
20
[/purchase cost]
`,
		"skill/job0/basicattackup.skl": `
[type]
` + "`[passive]`" + `
[required level]
1
[maximum level]
100
[skill fitness growtype]
0 1 2 3 4 5
[/skill fitness growtype]
[growtype maximum level]
1 1 1 1 1 1
[purchase cost]
0
[/purchase cost]
`,
	}, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := Load(context.Background(), index, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return table
}

// TestSkillsWithUnavailableDirectionNamesTheBranchConflict 是本轮现场（角色 2
// 菲八番茄）的纯函数回归：一个**已经学过剑魂技能**的角色，如果权威 grow_type 掉回 0
// （未转职），这些技能必须被点名为"当前方向学不了"—— 这就是门禁拒绝它每一次加点的
// 原因，也是"数据与已持久化技能自相矛盾"的判据。
func TestSkillsWithUnavailableDirectionNamesTheBranchConflict(t *testing.T) {
	table := directionAuditTable(t)
	learned := map[int64]dnfrepo.SkillState{
		27:  {Level: 11, Enabled: true},
		46:  {Level: 1, Enabled: true},
		174: {Level: 1, Enabled: true},
	}

	// grow_type=1（剑魂）：没有冲突。
	if got := SkillsWithUnavailableDirection(table, 0, 1, learned); len(got) != 0 {
		t.Fatalf("transferred branch must have no unavailable learned skill, got %v", got)
	}

	// grow_type=0（未转职）：只有分支技能 27 被点名，通用技能不受影响。
	got := SkillsWithUnavailableDirection(table, 0, 0, learned)
	if len(got) != 1 || got[0] != 27 {
		t.Fatalf("untransferred direction unavailable = %v, want [27]", got)
	}

	// 一键装觉醒段（0x11 / 0x21）仍属分支 1，不得误报。
	for _, packed := range []int{0x11, 0x21} {
		if got := SkillsWithUnavailableDirection(table, 0, packed, learned); len(got) != 0 {
			t.Fatalf("grow_type=%#x must keep branch 1 skills available, got %v", packed, got)
		}
	}

	// 空输入 / nil 目录必须安全返回。
	if got := SkillsWithUnavailableDirection(nil, 0, 0, learned); got != nil {
		t.Fatalf("nil table = %v, want nil", got)
	}
	if got := SkillsWithUnavailableDirection(table, 0, 0, nil); got != nil {
		t.Fatalf("no learned skills = %v, want nil", got)
	}
}

// TestSkillsWithUnavailableDirectionIgnoresLevelRules 与 RepairLevelCaps 的
// result.Unavailable 划清界限：本函数只看**方向**，不把"等级还不够"算成不可学。
func TestSkillsWithUnavailableDirectionIgnoresLevelRules(t *testing.T) {
	table := directionAuditTable(t)
	definition, ok := table.Find(0, 27)
	if !ok {
		t.Fatal("job0 skill 27 missing")
	}
	if definition.RequiredLevel == 0 {
		t.Fatal("fixture must declare a required level above 1 so the level rule is visible")
	}
	// 等级 1 时 `1+(1-10)/interval` 已经 <=0，但方向仍合法 ⇒ 不报冲突。
	learned := map[int64]dnfrepo.SkillState{27: {Level: 3, Enabled: true}}
	if got := SkillsWithUnavailableDirection(table, 0, 1, learned); len(got) != 0 {
		t.Fatalf("level rules must not be reported as a direction conflict, got %v", got)
	}
}
