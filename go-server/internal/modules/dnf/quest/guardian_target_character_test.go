package quest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guardianTargetCharacterCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList:                  "12758 `elven.qst`\n12782 `chaos.qst`\n12735 `elven_awakened.qst`\n10001 `job_only.qst`\n",
		"n_quest/elven.qst":          questCatalogTestDefinition("[side]", 15, 99, "[all]", "\n[name]\n`[盾牌]新的力量`\n[target character]\n`[knight]` 1 -1\n[pre required quest]\n12764\n"),
		"n_quest/chaos.qst":          questCatalogTestDefinition("[side]", 15, 99, "[all]", "\n[name]\n`[盾牌]新的力量`\n[target character]\n`[knight]` 2 -1\n[pre required quest]\n12764\n"),
		"n_quest/elven_awakened.qst": questCatalogTestDefinition("[side]", 50, 99, "[all]", "\n[target character]\n`[knight]` 1 1 `[knight]` 1 2\n"),
		"n_quest/job_only.qst":       questCatalogTestDefinition("[side]", 15, 99, "[all]", "\n[target character]\n`[knight]`\n"),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func assertGuardianShieldQuestTargets(t *testing.T, catalog *Catalog) {
	t.Helper()
	record := level15BranchRecord(12764)
	for _, grow := range []int{0, 1, 2, 3, 4, 0x11, 0x12, 0x13, 0x14, 0x21, 0x22, 0x23, 0x24} {
		t.Run(fmt.Sprintf("grow_%02x", grow), func(t *testing.T) {
			character := CharacterEligibility{Level: 85, Job: 12, GrowType: grow}
			listed := level15BranchListed(catalog, 85, 12, grow, record)
			for id, branch := range map[int64]int{12758: 1, 12782: 2} {
				want := grow&0x0f == branch
				if listed[id] != want {
					t.Errorf("守护者grow=%02x的盾牌可接列表含%d=%t，期望%t", grow, id, listed[id], want)
				}
				decision, err := catalog.DecideAccept(character, record, id)
				if (want && (err != nil || decision != AcceptNew)) || (!want && (decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable))) {
					t.Errorf("盾牌接取%d=%v err=%v，允许=%t", id, decision, err, want)
				}
			}
		})
	}
	for _, job := range []int{0, 9, 14} {
		listed := level15BranchListed(catalog, 85, job, 1, record)
		if listed[12758] || listed[12782] {
			t.Errorf("其它职业%d收到守护者盾牌任务", job)
		}
	}
}

func TestGuardianShieldQuestTargetsFilterOrdinarySideQuestBranches(t *testing.T) {
	catalog := guardianTargetCharacterCatalog(t)
	assertGuardianShieldQuestTargets(t, catalog)
	for _, grow := range []int{0, 1, 2, 3, 4, 0x11, 0x21} {
		listed := level15BranchListed(catalog, 85, 12, grow, level15BranchRecord(12764))
		if want := grow == 0x11 || grow == 0x21; listed[12735] != want {
			t.Errorf("非转职任务的一/二觉限定grow=%02x got=%t want=%t", grow, listed[12735], want)
		}
		if !listed[10001] {
			t.Errorf("只有职业标签的旧任务被误拒绝：grow=%02x", grow)
		}
	}
}

func TestGuardianShieldQuestTargetFixPreservesExistingTaskRecords(t *testing.T) {
	catalog := guardianTargetCharacterCatalog(t)
	record := level15BranchRecord(12764, 12758)
	record.States[12782] = dnfrepo.QuestState{Status: "active", TriggerType: 1}
	character := CharacterEligibility{Level: 85, Job: 12, GrowType: 3}
	before := dnfrepo.CloneQuest(record)
	if decision, err := catalog.DecideAccept(character, record, 12782); err != nil || decision != AcceptIdempotent {
		t.Fatalf("已有激活任务应保留幂等处理：%v %v", decision, err)
	}
	_ = catalog.QuestList(character, record)
	if record.States[12758].Status != before.States[12758].Status || record.States[12782].Status != before.States[12782].Status {
		t.Fatal("资格投影修改了玩家已有任务状态")
	}
}

func TestRealPVFGuardianShieldQuestTargetsRespectElvenChaosBranches(t *testing.T) {
	catalog := level15BranchRealPVFCatalog(t)
	for id, branch := range map[int64]int{12758: 1, 12782: 2} {
		definition, found := catalog.Find(id)
		if !found || definition.Name != "[盾牌]新的力量" || definition.JobChangeQuest != 0 || len(definition.TargetCharacterRules) != 1 || definition.TargetCharacterRules[0].FirstGrowType != branch {
			t.Fatalf("真实盾牌任务%d的定义发生变化：%+v", id, definition)
		}
	}
	assertGuardianShieldQuestTargets(t, catalog)
	definition, found := catalog.Find(3830)
	if !found || definition.Name != "为了前往寂静城" || definition.LevelMin != 85 {
		t.Fatalf("当前寂静城主线定义改变：%+v", definition)
	}
	character := CharacterEligibility{Level: 85, Job: 12, GrowType: 3}
	if mainStoryQuestAutoClearable(definition, character, 84) || !mainStoryQuestAutoClearable(definition, character, 85) {
		t.Fatal("寂静城主线应只在清理截止等级达到85时可被批量关闭")
	}
}
