package gmtool

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P6 · GM 面板侧的接线测试（J12：职业任务分支 + 主线链路重置）。
//
// 本文件只新增用例，**不改 P5 的任何既有测试函数**（派工单硬性要求 6）。
//
// 四条契约：
//  1. 两个开关默认档 = P5 的字节（B/D）；
//  2. 开关只认显式 on（A）；
//  3. 开关 on 时链路重置只动本族、具名拒绝一切不完整（C）；
//  4. 开关 on 不影响与链路/职业无关的既有路径（C 的最后一节）。

// p6QuestMutationSource 在 P5 夹具目录之上补一条干净的主线链路家族：
//
//	4010 [epic] [quest clear] [int data] 4011 4012   父任务
//	4011 [sub] [main quest] 4010                      被管理的链接子任务
//	4012 [sub] [main quest] 4010                      被管理的链接子任务
//	4013 [normal] 独立任务（链外，用来证明"只动本族"）
//
// P5 自己的夹具（3970/3971/3972/4000/4001/4002/4003）原样保留，默认档用例继续用它。
func p6QuestMutationSource() readonlyTestSource {
	source := questMutationSource()
	source[dnfquest.DefaultList] += "4010 `q4010.qst`\n4011 `q4011.qst`\n4012 `q4012.qst`\n4013 `q4013.qst`\n"
	source["n_quest/q4010.qst"] = "[grade]\n`[epic]` \n[type]\n`[quest clear]` \n[level]\n1 99 \n[job]\n`[all]` \n" +
		"[name]\n`主线父任务` \n[int data]\n4011 4012 \n"
	source["n_quest/q4011.qst"] = p6ChainChildDocument("链接子任务一")
	source["n_quest/q4012.qst"] = p6ChainChildDocument("链接子任务二")
	source["n_quest/q4013.qst"] = "[grade]\n`[normal]` \n[type]\n`[meet npc]` \n[level]\n1 99 \n[job]\n`[all]` \n" +
		"[name]\n`链外独立任务` \n"
	return source
}

func p6ChainChildDocument(name string) string {
	return "[grade]\n`[sub]` \n[type]\n`[meet npc]` \n[level]\n1 99 \n[job]\n`[all]` \n" +
		"[name]\n`" + name + "` \n[main quest]\n4010 \n"
}

// p6ChainFixtureQuests 是链路夹具：整族 completed（父 4010 + 子 4011/4012），
// 链外 4013 也 completed，另外在 Progress 里放一条家族的旧行（验证双删）。
func p6ChainFixtureQuests() *dnfrepo.QuestRecord {
	return &dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			4010: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4011: {Status: "completed", UpdatedAt: questMutationFixtureTime,
				Extra: map[string]string{"reward_state": "granted"}},
			4012: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4013: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		},
		Progress: map[int64]dnfrepo.QuestState{
			4011: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		},
	}
}

func p6JSON(t *testing.T, payload any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// 开关解析
// ---------------------------------------------------------------------------

func TestQuestChainResetFromEnvOnlyLiteralOn(t *testing.T) {
	if QuestChainResetFromEnv("") {
		t.Fatal("空值必须是 off（主线链路重置默认关闭）")
	}
	for _, value := range []string{"on", "ON", " On "} {
		if !QuestChainResetFromEnv(value) {
			t.Fatalf("%q 必须启用", value)
		}
	}
	// 安全开关只认显式 on，与 QuestProfessionWriteFromEnv / RequireOfflineFromEnv 同一约定。
	for _, value := range []string{"true", "1", "yes", "enable", "off", "0", "no", "onn"} {
		if QuestChainResetFromEnv(value) {
			t.Fatalf("%q 不得启用链路重置（只认显式 on）", value)
		}
	}
}

// ---------------------------------------------------------------------------
// 契约 1：默认档（开关未设）= P5 的字节
// ---------------------------------------------------------------------------

// TestQuestUncompleteChainResetDefaultOffKeepsP5RejectionByteForByte 钉死默认档的响应体
// 与 reason：与 P5 交付时的那条拒绝**逐字节相同**（reason / mainQuestId / 文案全在内）。
func TestQuestUncompleteChainResetDefaultOffKeepsP5RejectionByteForByte(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: p6QuestMutationSource(),
		quests: p6ChainFixtureQuests(),
	})
	before := harness.questFingerprint(t)

	// 默认档（DNFBRIDGE_GM_QUEST_CHAIN_RESET 未设置）：即使这一族在 PVF 里完全可重置，
	// 也必须走 P5 的拒绝。
	status, response := harness.uncomplete(t, 4011)
	if status != http.StatusOK {
		t.Fatalf("拒绝必须是 HTTP 200（计划书 §5.2 C10），实际 %d", status)
	}
	golden := map[string]any{
		"success":     false,
		"characterId": float64(1),
		"questId":     float64(4011),
		"reason":      "linked_chain_reset_not_ported",
		"mainQuestId": float64(4010),
		"error": "任务 4011 属于主线链路（[main quest id] = 4010）：上游会重置整条链路（PlanLinkedQuestChainReset），" +
			"本仓尚未移植（P6）。只删本任务会留下不一致的子任务，因此拒绝；本次未做任何写入。",
	}
	if got, want := p6JSON(t, response), p6JSON(t, golden); got != want {
		t.Fatalf("默认档链路拒绝的响应体必须与 P5 逐字节相同:\ngot =%s\nwant=%s", got, want)
	}
	harness.requireZeroWrite(t, before, "默认档反完成主线链路任务")
	if fields := harness.eventFields(t, "gm-quest-mutation-rejected"); fields["reason"] != "linked_chain_reset_not_ported" {
		t.Fatalf("拒绝事件必须如实记录 reason: %+v", fields)
	}
	// 开关名不允许出现在默认档响应里（默认档文案一个字都不改）。
	if strings.Contains(response["error"].(string), questChainResetEnv) {
		t.Fatalf("默认档文案不得提到新开关: %q", response["error"])
	}
}

// TestQuestChainResetSwitchOffExplicitValueKeepsP5Rejection 显式 off 也用同一条拒绝。
func TestQuestChainResetSwitchOffExplicitValueKeepsP5Rejection(t *testing.T) {
	t.Setenv(questChainResetEnv, "off")
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: p6QuestMutationSource(),
		quests: p6ChainFixtureQuests(),
	})
	before := harness.questFingerprint(t)
	_, response := harness.uncomplete(t, 4011)
	if response["reason"] != "linked_chain_reset_not_ported" {
		t.Fatalf("显式 off 必须与默认档同口径: %+v", response)
	}
	harness.requireZeroWrite(t, before, "显式 off 反完成主线链路任务")
}

// TestQuestForceCompleteProfessionSwitchDefaultOffKeepsP5Rejection 钉死职业分支默认档：
// 拒绝 + 文案 + reason 与 P5 一致（P5 的既有用例已覆盖，这里额外按开关名与零写入复钉）。
func TestQuestForceCompleteProfessionSwitchDefaultOffKeepsP5Rejection(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)
	_, response := harness.complete(t, 4000)
	if response["reason"] != "profession_write_disabled" {
		t.Fatalf("职业任务默认档必须走 profession_write_disabled: %+v", response)
	}
	golden := "任务 4000 是职业任务（[reward type] = [grow type]）：职业任务写入默认关闭，需显式 " +
		questProfessionWriteEnv + "=on 才允许；本次未做任何写入。批量完成始终排除职业任务。"
	if response["error"] != golden {
		t.Fatalf("默认档职业拒绝文案变了:\ngot =%q\nwant=%q", response["error"], golden)
	}
	harness.requireZeroWrite(t, before, "默认档完成职业任务")
}

// ---------------------------------------------------------------------------
// 契约 3：开关 on 时链路重置只动本族 + 具名拒绝
// ---------------------------------------------------------------------------

func TestQuestUncompleteChainResetSwitchOnResetsTheWholeFamily(t *testing.T) {
	t.Setenv(questChainResetEnv, "on")
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: p6QuestMutationSource(),
		quests: p6ChainFixtureQuests(),
	})

	status, response := harness.uncomplete(t, 4011)
	requireOK(t, status, response)
	if response["chainReset"] != true || response["parentQuestId"] != float64(4010) {
		t.Fatalf("链路重置必须如实回报父任务: %+v", response)
	}
	if got := responseList(t, response, "removedQuestIds"); len(got) != 3 {
		t.Fatalf("removedQuestIds = %v, want 3 个（父+两个子）", got)
	}
	reactivated := responseList(t, response, "reactivatedQuestIds")
	if len(reactivated) != 3 || reactivated[0] != float64(4010) ||
		reactivated[1] != float64(4011) || reactivated[2] != float64(4012) {
		t.Fatalf("reactivatedQuestIds = %v, want [4010 4011 4012]（父在前、PVF 顺序）", reactivated)
	}
	// 重新激活的一代：父 active（无 main_quest_id 标记）、子 active（带父任务标记）。
	parentState, found := harness.questState(t, 4010)
	if !found || parentState.Status != "active" {
		t.Fatalf("父任务 4010 状态 = %+v found=%v", parentState, found)
	}
	if _, marked := parentState.Extra["main_quest_id"]; marked {
		t.Fatalf("父任务不得带 main_quest_id: %+v", parentState.Extra)
	}
	for _, questID := range []int64{4011, 4012} {
		state, found := harness.questState(t, questID)
		if !found || state.Status != "active" {
			t.Fatalf("子任务 %d 状态 = %+v found=%v", questID, state, found)
		}
		if state.Extra["main_quest_id"] != "4010" || state.Extra["auto_activated_by_main_quest"] != "true" {
			t.Fatalf("子任务 %d 缺少父任务标记: %+v", questID, state.Extra)
		}
	}
	// Progress 里的旧行必须被清掉。
	record := harness.questRecord(t)
	for _, questID := range []int64{4010, 4011, 4012} {
		if _, exists := record.Progress[questID]; exists {
			t.Fatalf("家族成员 %d 的 Progress 旧行没有被清掉: %+v", questID, record.Progress)
		}
	}
	// 链外任务一个字节都不许动。
	outside := record.States[4013]
	if outside.Status != "completed" {
		t.Fatalf("链外任务 4013 被改动: %+v", outside)
	}
	if _, exists := record.Progress[4013]; exists {
		t.Fatalf("链外任务不得被写入 Progress: %+v", record.Progress)
	}
	if !harness.hasEvent("gm-quest-chain-reset") {
		t.Fatalf("链路重置必须记具名事件，实际 %v", harness.eventKinds())
	}
}

func TestQuestUncompleteChainResetSwitchOnRejectsWithNamedReasons(t *testing.T) {
	t.Setenv(questChainResetEnv, "on")
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: p6QuestMutationSource(),
		quests: p6ChainFixtureQuests(),
	})

	// 链外任务（没有 [main quest]）在开关 on 时不再走"链路"分支，而是走 P5 的普通
	// 反完成路径 —— 这里用 4013（completed）验证那条路径**没有被 P6 改掉**。
	status, response := harness.uncomplete(t, 4013)
	requireOK(t, status, response)
	if _, hijacked := response["chainReset"]; hijacked {
		t.Fatalf("非链路任务不得被链路分支接管: %+v", response)
	}
	if response["removedStates"] != true || response["removedProgress"] != false {
		t.Fatalf("非链路任务必须保持 P5 的逐条语义: %+v", response)
	}

	// 请求的任务行不存在（开关 on，任务属于链路）：必须具名拒绝 + 零写入。
	missing := newQuestWriteHarness(t, questWriteFixture{
		source: p6QuestMutationSource(),
		quests: &dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			4013: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		}},
	})
	before := missing.questFingerprint(t)
	_, response = missing.uncomplete(t, 4011)
	message := requireFailure(t, http.StatusOK, response)
	if response["reason"] != "chain_row_missing" || !strings.Contains(message, "4011") {
		t.Fatalf("任务行不存在必须具名拒绝: %+v", response)
	}
	missing.requireZeroWrite(t, before, "链路重置遇到缺失的任务行")

	// 父任务类型不是 quest clear：4001 的 [main quest] 指向 3970（quest clear），
	// 而 4002 的 [main quest] 不存在 —— 用 4001 之外的构造太贵，这里直接用 P5 夹具
	// 的既有事实：3970 是 quest clear 父任务、4001 不是被管理子任务（[epic] 而不是
	// 无奖励 [sub]），开关 on 必须具名拒绝而不是"只删本任务"。
	notManaged := newQuestWriteHarness(t, questWriteFixture{
		source: p6QuestMutationSource(),
		quests: questMutationFixtureQuests(),
	})
	before = notManaged.questFingerprint(t)
	_, response = notManaged.uncomplete(t, 4001)
	message = requireFailure(t, http.StatusOK, response)
	if response["reason"] != "chain_requested_not_managed" || !strings.Contains(message, "3970") {
		t.Fatalf("非被管理子任务必须具名拒绝: %+v", response)
	}
	notManaged.requireZeroWrite(t, before, "开关 on 反完成非被管理子任务")
}

// ---------------------------------------------------------------------------
// 契约 4：真档 Script.pvf 上的端到端
// ---------------------------------------------------------------------------

// TestRealPVFQuestChainResetSwitchOnResetsTheWholeFamily 在真档上跑通面板侧全链路：
// 真档家族 3146 -> 3157 / 3054（catalog_real_pvf_test.go 与 clear_map 链路已确证），
// 前置任务 3145 已完成、链外 3971 家族原样保留。
func TestRealPVFQuestChainResetSwitchOnResetsTheWholeFamily(t *testing.T) {
	source := loadRealPVFSource(t)
	fixture := func() *dnfrepo.QuestRecord {
		return &dnfrepo.QuestRecord{
			States: map[int64]dnfrepo.QuestState{
				3145: {Status: "completed", UpdatedAt: questMutationFixtureTime},
				3146: {Status: "completed", UpdatedAt: questMutationFixtureTime},
				3157: {Status: "completed", UpdatedAt: questMutationFixtureTime},
				3054: {Status: "completed", UpdatedAt: questMutationFixtureTime},
				3971: {Status: "completed", UpdatedAt: questMutationFixtureTime},
				3972: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			},
		}
	}
	chainCharacter := dnfrepo.CharacterRecord{
		AccountID: "acc-1", Name: "tester", Job: "0", Level: 10,
		Stats: map[string]int64{"grow_type": 0},
	}

	// 默认档：真档上同样必须是 P5 的拒绝 + 零写入。
	offHarness := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: chainCharacter, quests: fixture()})
	before := offHarness.questFingerprint(t)
	_, response := offHarness.uncomplete(t, 3054)
	if response["reason"] != "linked_chain_reset_not_ported" {
		t.Fatalf("真档默认档必须保持 P5 拒绝: %+v", response)
	}
	offHarness.requireZeroWrite(t, before, "真档默认档链路重置")

	// 开关 on：整族重建。
	t.Setenv(questChainResetEnv, "on")
	harness := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: chainCharacter, quests: fixture()})
	status, response := harness.uncomplete(t, 3054)
	if response["success"] != true {
		message, _ := response["error"].(string)
		t.Fatalf("真档链路重置失败: %q (%+v)", message, response)
	}
	requireOK(t, status, response)
	if response["chainReset"] != true || response["parentQuestId"] != float64(3146) {
		t.Fatalf("真档父任务 = %+v, want 3146", response)
	}
	reactivated := responseList(t, response, "reactivatedQuestIds")
	if len(reactivated) != 3 || reactivated[0] != float64(3146) {
		t.Fatalf("真档 reactivatedQuestIds = %v, want [3146 3157 3054]", reactivated)
	}
	for _, questID := range []int64{3157, 3054} {
		state, found := harness.questState(t, questID)
		if !found || state.Status != "active" {
			t.Fatalf("真档子任务 %d 状态 = %+v found=%v", questID, state, found)
		}
		if state.Extra["main_quest_id"] != "3146" || state.Extra["auto_activated_by_main_quest"] != "true" {
			t.Fatalf("真档子任务 %d 缺少父任务标记: %+v", questID, state.Extra)
		}
	}
	parentState, found := harness.questState(t, 3146)
	if !found || parentState.Status != "active" || parentState.Extra["pvf_path"] == "" {
		t.Fatalf("真档父任务 3146 状态 = %+v found=%v", parentState, found)
	}
	// 链外任务（前置任务 3145 与另一条家族 3971/3972）一个字节都不许动。
	for _, questID := range []int64{3145, 3971, 3972} {
		state, found := harness.questState(t, questID)
		if !found || state.Status != "completed" {
			t.Fatalf("真档链外任务 %d 被改动: %+v found=%v", questID, state, found)
		}
		if !state.UpdatedAt.Equal(questMutationFixtureTime) {
			t.Fatalf("真档链外任务 %d 的 UpdatedAt 被改写: %v", questID, state.UpdatedAt)
		}
	}
	// 具名拒绝（真档）：任务不在这个角色的任务账里。
	emptyHarness := newQuestWriteHarness(t, questWriteFixture{
		pvf: source, character: chainCharacter,
		quests: &dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			3971: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		}},
	})
	emptyBefore := emptyHarness.questFingerprint(t)
	_, response = emptyHarness.uncomplete(t, 3054)
	if response["reason"] != "chain_row_missing" {
		t.Fatalf("真档缺行必须具名拒绝: %+v", response)
	}
	emptyHarness.requireZeroWrite(t, emptyBefore, "真档链路重置遇到缺失的任务行")
}

// TestRealPVFProfessionQuestCompleteResetsTheProfessionLedger 覆盖职业分支的**账重置**：
// 在真档里挑一个 grow_type=0 能承接的转职任务，先往任务账里塞一条一次觉醒行与一条
// 专家职业行，再强制完成转职任务，断言：
//
//	职业跃迁照旧（grow_type 前进、技能账同事务）、一次觉醒行被清掉、
//	专家职业（JobChangeQuest 20）一行不动、链外普通任务一行不动。
func TestRealPVFProfessionQuestCompleteResetsTheProfessionLedger(t *testing.T) {
	source := loadRealPVFSource(t)
	harnessProbe := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: realPVFCharacter()})
	profiles, _, err := harnessProbe.server.pvf.professionResources(harnessProbe.server)
	if err != nil {
		t.Fatalf("load real profession resources: %v", err)
	}
	catalog, err := harnessProbe.server.pvf.questCatalog(harnessProbe.server)
	if err != nil {
		t.Fatalf("load real quest catalog: %v", err)
	}

	// 真档里的三类取样：转职任务（可承接）、一次觉醒任务、专家职业任务、普通任务。
	var (
		classQuestID int64
		classJob     byte
		classGrow    byte
		awakeningID  int64
		expertID     int64
		ordinaryID   int64
	)
	for questID := int64(1); questID <= math.MaxUint16; questID++ {
		definition, found := catalog.Find(questID)
		if !found {
			continue
		}
		if awakeningID == 0 && definition.JobChangeQuest == 2 {
			awakeningID = definition.ID
		}
		if expertID == 0 && definition.JobChangeQuest == 20 {
			expertID = definition.ID
		}
		if ordinaryID == 0 && definition.JobChangeQuest == 0 && definition.MainQuestID == 0 {
			ordinaryID = definition.ID
		}
		if classQuestID != 0 || !IsProfessionQuestReward(definition.RewardType) {
			continue
		}
		request, parseErr := dnfprofession.ParseReward(definition.JobChangeQuest, definition.RewardType, definition.RewardIntData)
		if parseErr != nil || request.Kind != dnfprofession.KindClassChange {
			continue
		}
		for job := byte(0); job < 16; job++ {
			transition, planErr := profiles.PlanTransition(job, 0, request)
			if planErr != nil {
				continue
			}
			classQuestID, classJob, classGrow = questID, job, transition.NewGrowType
			break
		}
	}
	if classQuestID == 0 || awakeningID == 0 || expertID == 0 || ordinaryID == 0 {
		t.Skipf("真档前提不成立: class=%d awakening=%d expert=%d ordinary=%d",
			classQuestID, awakeningID, expertID, ordinaryID)
	}
	t.Logf("real profession ledger fixture: class=%d (job=%d -> grow_type=%d) awakening=%d expert=%d ordinary=%d",
		classQuestID, classJob, classGrow, awakeningID, expertID, ordinaryID)

	harness := newQuestWriteHarness(t, questWriteFixture{
		pvf: source,
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: strconv.Itoa(int(classJob)), Level: 90,
			Stats: map[string]int64{"grow_type": 0},
		},
		skills: &dnfrepo.SkillRecord{Points: dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100}},
		quests: &dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			awakeningID: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			expertID:    {Status: "completed", UpdatedAt: questMutationFixtureTime},
			ordinaryID:  {Status: "active", ProgressValue: 1, UpdatedAt: questMutationFixtureTime},
		}},
	})
	t.Setenv(questProfessionWriteEnv, "on")
	before := harness.questFingerprint(t)

	status, response := harness.complete(t, classQuestID)
	if response["success"] != true {
		message, _ := response["error"].(string)
		harness.requireZeroWrite(t, before, "真档职业任务完成失败路径")
		t.Fatalf("真档转职完成失败（已确认零写入）: %q (%+v)", message, response)
	}
	requireOK(t, status, response)
	if response["professionChanged"] != true || response["rewardGranted"] != false {
		t.Fatalf("职业跃迁结果字段不对: %+v", response)
	}
	resetIDs := responseList(t, response, "professionResetQuestIds")
	foundAwakening := false
	for _, raw := range resetIDs {
		if raw == float64(awakeningID) {
			foundAwakening = true
		}
		if raw == float64(expertID) {
			t.Fatalf("专家职业任务 %d 绝不允许被职业账重置删掉: %v", expertID, resetIDs)
		}
	}
	if !foundAwakening {
		t.Fatalf("professionResetQuestIds = %v, want 含一次觉醒任务 %d", resetIDs, awakeningID)
	}

	character, found, err := harness.group.Character.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("reload character: found=%v err=%v", found, err)
	}
	if int64(growTypeValue(character)) != int64(classGrow) {
		t.Fatalf("角色 grow_type = %d, want %d", growTypeValue(character), classGrow)
	}
	record := harness.questRecord(t)
	if _, exists := record.States[awakeningID]; exists {
		t.Fatalf("一次觉醒任务行 %d 必须被清掉: %+v", awakeningID, record.States)
	}
	expertState, exists := record.States[expertID]
	if !exists || expertState.Status != "completed" || !expertState.UpdatedAt.Equal(questMutationFixtureTime) {
		t.Fatalf("专家职业任务行 %d 必须原样保留: %+v exists=%v", expertID, expertState, exists)
	}
	ordinaryState, exists := record.States[ordinaryID]
	if !exists || ordinaryState.Status != "active" || ordinaryState.ProgressValue != 1 {
		t.Fatalf("普通任务行 %d 必须原样保留: %+v exists=%v", ordinaryID, ordinaryState, exists)
	}
	classState, exists := record.States[classQuestID]
	if !exists || classState.Status != questCompletedLiteral || classState.Extra[questRewardsGrantedKey] != "false" {
		t.Fatalf("职业任务完成行 = %+v exists=%v", classState, exists)
	}
}
