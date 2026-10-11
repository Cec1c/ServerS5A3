package gmtool

import (
	"context"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是现场报障（运维实测 2026-09-29 05:0x「gm工具开启耳环槽后 任务依然存在」）
// 的回归判据。
//
// 现场事实（线上库 dnf_quest_states，逐行原文见 _dist/earring-slot-quest.md）：
//
//	character 4 (`t3`, Lv90, ex_equip_slot_stat=16)  states 2635 active  progress_value=0
//
// 而真档 Script.pvf 的耳环链是
//
//	2635 `n_quest/new_quest/MetroCenter/system_90_earring_01.qst`
//	     [name] `穿戴耳环 (1/2)` [grade] `[side]` [type] `[hunt monster]`
//	     [attribute] `slot expansion`，没有 [pre required quest]
//	2636 `n_quest/new_quest/MetroCenter/system_90_earring_02.qst`
//	     [name] `穿戴耳环 (2/2)` [pre required quest] 2635
//	     [reward type] `[slot expansion]` [reward int data] 2
//
// 旧实现（只认 `[reward type] [slot expansion]` 的任务）只会动 2636，2635 一行都不碰，
// 于是那条外传任务永远留在任务手册里。

// 现场复现：位**已经被旧工具打开**（0x10）、任务手册里挂着 active 的 2635。
// 再点一次「开启耳环槽」必须把 2635 收掉 —— 这正是"位已经开过"的角色唯一的修法
// （面板上这个按钮不再因为"已开启"而禁用，见 wwwroot/js/character.js）。
func TestHandleUnlockEarringSlotRepairsStuckOuterQuestOnAlreadyUnlockedCharacter(t *testing.T) {
	harness := newSlotTestHarness(t, 90, map[string]int64{extraEquipmentSlotStatColumn: 16}, dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			2635: {Status: "active", ProgressValue: 0, Extra: map[string]string{"quest_type": "[hunt monster]"}},
		},
	})
	response := harness.unlock(t, `{"slots":"earring"}`)
	if success, _ := response["success"].(bool); !success {
		t.Fatalf("unlock failed: %+v", response)
	}
	if got := response["questChain"]; got != true {
		t.Fatalf("questChain = %#v, want true (default tier)", got)
	}
	if got := response["questSync"]; got != true {
		t.Fatalf("questSync = %#v, want true (chain tier implies the quest write)", got)
	}
	if got := stringSliceFromResponse(t, response, "alreadyUnlocked"); !reflect.DeepEqual(got, []string{"earring"}) {
		t.Fatalf("alreadyUnlocked = %v, want [earring]", got)
	}
	if got := stringSliceFromResponse(t, response, "unlockedGroups"); len(got) != 0 {
		t.Fatalf("unlockedGroups = %v, want empty (bit was already on)", got)
	}
	if got := response["exEquipSlotStat"]; got != float64(16) {
		t.Fatalf("exEquipSlotStat = %v, want 16 (unchanged)", got)
	}
	if got := int64SliceFromResponse(t, response, "completedQuestIds"); !reflect.DeepEqual(got, []int64{2635}) {
		t.Fatalf("completedQuestIds = %v, want [2635] (the stuck 外传 quest)", got)
	}
	if got := int64SliceFromResponse(t, response, "createdQuestIds"); !reflect.DeepEqual(got, []int64{2636}) {
		t.Fatalf("createdQuestIds = %v, want [2636]", got)
	}
	states := harness.questStates(t)
	if state := states[2635]; !questStatusCompleted(state.Status) || state.ProgressValue != 0 {
		t.Fatalf("2635 row after repair = %+v, want completed/0", state)
	}
	if state := states[2636]; !questStatusCompleted(state.Status) {
		t.Fatalf("2636 row after repair = %+v, want completed", state)
	}
	// 位一个 bit 都不许动：这条路径只补任务面。
	if stat := harness.slotStat(t); stat != 16 {
		t.Fatalf("repair path changed ex_equip_slot_stat = %#x, want unchanged 0x10", stat)
	}
}

// 前置链闭包：只按 `[pre required quest]` 反向走，**不**沿任何其它字段扩散 ——
// 奖励任务的后继（例如真档 2711 `system_90_earring_03.qst`，它发一件耳环）不在集合里。
func TestSlotExpansionQuestChainExcludesSuccessors(t *testing.T) {
	source := slotTestSource{
		dnfquest.DefaultList: "2636 `earring.qst`\n" +
			"2635 `earring_side.qst`\n" +
			"2711 `earring_reward.qst`\n",
		"n_quest/earring.qst":      slotExpansionQuestWithPrerequisite("[side]", 2, 2635),
		"n_quest/earring_side.qst": slotChainPrerequisiteQuest(),
		// 2711 的 [pre required quest] 是 2636：它是**后继**，反向闭包不许把它收进来。
		"n_quest/earring_reward.qst": "[grade]\n`[side]`\n[pre required quest]\n2636\n[/pre required quest]\n" +
			"[type]\n`[seeking]`\n[level]\n1 99\n[job]\n`[all]`\n" +
			"[reward type]\n`[item]`\n[reward int data]\n100390006 1\n",
	}
	catalog := slotChainTestCatalog(t, source)
	if got := catalog.SlotExpansionQuestChainIDs(2636); !reflect.DeepEqual(got, []int64{2635, 2636}) {
		t.Fatalf("chain(2636) = %v, want [2635 2636]", got)
	}
}

// 传递闭包：a -> b -> c 时三条都在集合里（b 自己是普通 [item] 任务，不许当成
// "另一条槽位授权链"而止步）。
func TestSlotExpansionQuestChainWalksTransitivePrerequisites(t *testing.T) {
	source := slotTestSource{
		dnfquest.DefaultList: "7000 `a.qst`\n7001 `b.qst`\n7002 `c.qst`\n",
		"n_quest/a.qst":      slotExpansionQuestWithPrerequisite("[side]", 2, 7001),
		"n_quest/b.qst": "[grade]\n`[side]`\n[pre required quest]\n7002\n[/pre required quest]\n" +
			"[type]\n`[seeking]`\n[level]\n1 99\n[job]\n`[all]`\n" +
			"[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/c.qst": slotChainPrerequisiteQuest(),
	}
	catalog := slotChainTestCatalog(t, source)
	if got := catalog.SlotExpansionQuestChainIDs(7000); !reflect.DeepEqual(got, []int64{7000, 7001, 7002}) {
		t.Fatalf("chain(7000) = %v, want [7000 7001 7002]", got)
	}
}

// 不跨越到别的槽位授权链：祖先是另一条 `[reward type] [slot expansion]` 任务时
// 只收它自己，不再往它的前置走。
func TestSlotExpansionQuestChainStopsAtAnotherSlotGrantQuest(t *testing.T) {
	source := slotTestSource{
		dnfquest.DefaultList:       "2636 `earring.qst`\n649 `support.qst`\n674 `support_side.qst`\n",
		"n_quest/earring.qst":      slotExpansionQuestWithPrerequisite("[side]", 2, 649),
		"n_quest/support.qst":      slotExpansionQuestWithPrerequisite("[common unique]", 0, 674),
		"n_quest/support_side.qst": slotChainPrerequisiteQuest(),
	}
	catalog := slotChainTestCatalog(t, source)
	if got := catalog.SlotExpansionQuestChainIDs(2636); !reflect.DeepEqual(got, []int64{649, 2636}) {
		t.Fatalf("chain(2636) = %v, want [649 2636] (stop at the other slot's grant quest)", got)
	}
	// 反过来也成立：开左槽不许把耳环链带走。
	if got := catalog.SlotExpansionQuestChainIDs(649); !reflect.DeepEqual(got, []int64{649, 674}) {
		t.Fatalf("chain(649) = %v, want [649 674]", got)
	}
}

// `[pre required quest]` 点名了目录里不存在的 id：**跳过**它，不写一行 PVF 里没有的
// 任务，也不因为缺口而整体失败。
func TestSlotExpansionQuestChainSkipsUnknownPrerequisite(t *testing.T) {
	source := slotTestSource{
		dnfquest.DefaultList: "2636 `earring.qst`\n",
		"n_quest/earring.qst": "[grade]\n`[side]`\n[pre required quest]\n999999\n[/pre required quest]\n" +
			"[type]\n`[seeking]`\n[level]\n1 99\n[job]\n`[all]`\n" +
			"[reward type]\n`[slot expansion]`\n[reward int data]\n2\n",
	}
	catalog := slotChainTestCatalog(t, source)
	if got := catalog.SlotExpansionQuestChainIDs(2636); !reflect.DeepEqual(got, []int64{2636}) {
		t.Fatalf("chain(2636) = %v, want [2636] (unknown prerequisites are skipped)", got)
	}
}

// 环：a -> b -> a。闭包必须终止且不含重复。
func TestSlotExpansionQuestChainTerminatesOnPrerequisiteCycle(t *testing.T) {
	source := slotTestSource{
		dnfquest.DefaultList: "8000 `a.qst`\n8001 `b.qst`\n",
		"n_quest/a.qst":      slotExpansionQuestWithPrerequisite("[side]", 2, 8001),
		"n_quest/b.qst": "[grade]\n`[side]`\n[pre required quest]\n8000\n[/pre required quest]\n" +
			"[type]\n`[seeking]`\n[level]\n1 99\n[job]\n`[all]`\n" +
			"[reward type]\n`[item]`\n[reward int data]\n0 0\n",
	}
	catalog := slotChainTestCatalog(t, source)
	if got := catalog.SlotExpansionQuestChainIDs(8000); !reflect.DeepEqual(got, []int64{8000, 8001}) {
		t.Fatalf("chain(8000) = %v, want [8000 8001]", got)
	}
}

// 未收录 / 非法 id 一律返回 nil，绝不返回"猜出来的"集合。
func TestSlotExpansionQuestChainRejectsUnknownStart(t *testing.T) {
	catalog := slotChainTestCatalog(t, slotTestPVF())
	for _, questID := range []int64{0, -1, 999999} {
		if got := catalog.SlotExpansionQuestChainIDs(questID); got != nil {
			t.Fatalf("chain(%d) = %v, want nil", questID, got)
		}
	}
}

// 真档取证（需要 DNFBRIDGE_REAL_PVF_SMOKE 指向运行时 Script.pvf）：
//
//	2636 `穿戴耳环 (2/2)` -> 2635 `穿戴耳环 (1/2)`
//	 649 `穿戴辅助装备的资格 (2/2)` -> 674 `... (1/2)`
//	 650 `佩戴魔法石的资格 (2/2)`   -> 675 `... (1/2)`
//
// 三条链的**名字/类型/等级门槛**也在这里钉死，证明 2635 就是现场截图里那条外传任务。
func TestRealPVFSlotExpansionChainsMatchTheReportedQuest(t *testing.T) {
	source := loadRealPVFSource(t)
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatalf("build real PVF index: %v", err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatalf("load real quest catalog: %v", err)
	}
	for _, testCase := range []struct {
		questID int64
		want    []int64
	}{
		{questID: 2636, want: []int64{2635, 2636}},
		{questID: 649, want: []int64{649, 674}},
		{questID: 650, want: []int64{650, 675}},
	} {
		if got := catalog.SlotExpansionQuestChainIDs(testCase.questID); !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("chain(%d) = %v, want %v", testCase.questID, got, testCase.want)
		}
	}
	reported, found := catalog.Find(2635)
	if !found {
		t.Fatal("2635 must exist in the runtime PVF")
	}
	if reported.Name != "穿戴耳环 (1/2)" || reported.Grade != "[side]" || reported.Type != "[hunt monster]" {
		t.Fatalf("2635 = name%q grade%q type%q, want 穿戴耳环 (1/2) / [side] / [hunt monster]",
			reported.Name, reported.Grade, reported.Type)
	}
	// 目录里的路径键是**小写**的（dnfpvf 索引统一小写），quest.lst 原文是
	// `new_quest/MetroCenter/system_90_earring_01.qst` —— 两者是同一个条目。
	if reported.Path != "n_quest/new_quest/metrocenter/system_90_earring_01.qst" {
		t.Fatalf("2635 path = %q", reported.Path)
	}
	grant, found := catalog.Find(2636)
	if !found {
		t.Fatal("2636 must exist in the runtime PVF")
	}
	if grant.Name != "穿戴耳环 (2/2)" || len(grant.PreRequiredGroups) != 1 || len(grant.PreRequiredGroups[0]) != 1 ||
		grant.PreRequiredGroups[0][0] != 2635 {
		t.Fatalf("2636 = name%q preRequired=%v, want 穿戴耳环 (2/2) / [[2635]]", grant.Name, grant.PreRequiredGroups)
	}
	plan, err := catalog.PlanFinishReward(dnfquest.CharacterEligibility{Level: 90}, 2636, 0, false)
	if err != nil {
		t.Fatalf("PlanFinishReward(2636): %v", err)
	}
	if !plan.HasSlotExpansion || plan.SlotExpansionIndex != 2 || plan.SlotExpansionBit != dnfquest.ExEquipSlotEarring {
		t.Fatalf("2636 reward = has=%t index=%d bit=%#x, want earring 0x10", plan.HasSlotExpansion, plan.SlotExpansionIndex, plan.SlotExpansionBit)
	}
	// 后继 2711 `穿戴耳环`（发一件耳环）必须留在可接列表里：反向闭包不许把它收进来。
	if got := catalog.SlotExpansionQuestChainIDs(2636); len(got) != 2 {
		t.Fatalf("chain(2636) = %v, want exactly two ids (no 2711 successor)", got)
	}
}

// 运维验收面：GM 面板的「环境」页必须列出这条开关，默认档 Enabled=true /
// DefaultWhenUnset="on"，置回退字面量后 Enabled=false 且把原值如实回显。
func TestEnvironmentReportListsEquipSlotQuestChainSwitch(t *testing.T) {
	t.Setenv(equipSlotQuestChainEnv, "")
	server := &Server{cfg: Config{Logger: func(string, ...any) {}}}
	entry, found := envSwitchEntry(server.envSwitchReport(), equipSlotQuestChainEnv)
	if !found {
		t.Fatalf("环境报告里没有 %s", equipSlotQuestChainEnv)
	}
	if !entry.Enabled || entry.DefaultWhenUnset != "on" {
		t.Fatalf("默认档条目 = %+v, want enabled/on", entry)
	}
	t.Setenv(equipSlotQuestChainEnv, equipSlotQuestChainLegacy)
	entry, found = envSwitchEntry(server.envSwitchReport(), equipSlotQuestChainEnv)
	if !found || entry.Enabled || entry.Value != equipSlotQuestChainLegacy {
		t.Fatalf("回退档条目 = %+v (found=%t)", entry, found)
	}
}

func envSwitchEntry(entries []envSwitch, name string) (envSwitch, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return envSwitch{}, false
}

func slotChainTestCatalog(t *testing.T, source slotTestSource) *dnfquest.Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatalf("build test PVF index: %v", err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatalf("load test quest catalog: %v", err)
	}
	return catalog
}
