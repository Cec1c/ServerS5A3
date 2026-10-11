package gmtool

import (
	"sort"
	"strings"
	"testing"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P6b · 面板侧：**从 quest-clear 父任务发起整族重置**（P6 报告 §8-② 留下的缺口）。
//
// 背景：操作者在面板上重置一条主线时会点**父任务**那一行，而 quest clear 家族父任务
// 自己的 [main quest] 是 0（真档 104 个 quest clear 根全是这样）。P6 只在
// MainQuestID > 0 时接管，于是"反完成父任务"退化成 P5 的单条删除：父行没了、同一家族
// 里已持久化的子任务世代留在库里。
//
// 本文件钉死四条契约（开关沿用 DNFBRIDGE_GM_QUEST_CHAIN_RESET，**不新增开关、不新增路由**）：
//  1. 默认档（开关未设/显式 off）逐字节保持 P5 的单条删除（响应体 golden + 库内容断言）；
//  2. 开关 on + 父任务有受管子任务 → 整族重置，响应与子任务路径**同形**（键集逐字相同）；
//  3. 开关 on + childless / 悬空引用的 quest clear 父任务、普通任务 → 仍然只删那一行
//     （与开关 off 的响应体逐字节相同）—— 没有家族可重置时绝不编造；
//  4. 开关 on + 家族数据不完整（子任务指向目录外 / 重建不回来 / 父任务行不存在）→
//     具名拒绝 + 零写入。
//
// 基线取证：默认档的 golden 字符串与"开关 on/off 逐字节相同"这两条，都是从**改前树**
// （H:\86JP启动器\.dsh-tmp\p6b-baseline\go-server，P6 落定快照）上同一份探针跑出来的原文
// 对比得来的，见交付报告 §4。

// p6bParentQuestSource 在 P6 夹具（4010 家族 + 4013 链外）之上补齐"父任务入口"要用的格子：
//
//	4010 [epic] [quest clear] [int data] 4011 4012   有一条受管子任务的家族根
//	4011 [sub] [main quest] 4010                      受管子任务
//	4012 [sub] [main quest] 4010                      受管子任务
//	4013 [normal]（链外独立任务，证明"只动本族"）
//	4014 [epic] [quest clear] [int data] 4015         4015 不是受管子任务 → childless
//	4015 [normal]（不是无奖励 [sub]）
//	4016 [epic] [quest clear] [int data] 4999         4999 不在目录里 → 悬空 childless
//	4017 [epic] [quest clear] [int data] 4018 4998    4018 受管、4998 悬空 → 家族数据不完整
//	4018 [sub] [main quest] 4017
//	4019 [epic] [quest clear] [int data] 4020 4021    4021 等级窗口 95..99 → 重建不回来
//	4020 [sub] [main quest] 4019
//	4021 [sub] 95..99 [main quest] 4019
func p6bParentQuestSource() readonlyTestSource {
	source := p6QuestMutationSource()
	source[dnfquest.DefaultList] += "4014 `q4014.qst`\n4015 `q4015.qst`\n4016 `q4016.qst`\n" +
		"4017 `q4017.qst`\n4018 `q4018.qst`\n4019 `q4019.qst`\n4020 `q4020.qst`\n4021 `q4021.qst`\n"
	clearRoot := func(name, intData string) string {
		return "[grade]\n`[epic]` \n[type]\n`[quest clear]` \n[level]\n1 99 \n[job]\n`[all]` \n" +
			"[name]\n`" + name + "` \n[int data]\n" + intData + " \n"
	}
	subQuest := func(name, mainQuest string) string {
		return "[grade]\n`[sub]` \n[type]\n`[meet npc]` \n[level]\n1 99 \n[job]\n`[all]` \n" +
			"[name]\n`" + name + "` \n[main quest]\n" + mainQuest + " \n"
	}
	source["n_quest/q4014.qst"] = clearRoot("childless父任务", "4015")
	source["n_quest/q4015.qst"] = "[grade]\n`[normal]` \n[type]\n`[meet npc]` \n[level]\n1 99 \n[job]\n`[all]` \n" +
		"[name]\n`非受管子任务` \n"
	source["n_quest/q4016.qst"] = clearRoot("悬空父任务", "4999")
	source["n_quest/q4017.qst"] = clearRoot("数据不完整父任务", "4018 4998")
	source["n_quest/q4018.qst"] = subQuest("不完整家族子任务", "4017")
	source["n_quest/q4019.qst"] = clearRoot("资格不足父任务", "4020 4021")
	source["n_quest/q4020.qst"] = subQuest("可激活子任务", "4019")
	source["n_quest/q4021.qst"] = "[grade]\n`[sub]` \n[type]\n`[meet npc]` \n[level]\n95 99 \n[job]\n`[all]` \n" +
		"[name]\n`接不回来的子任务` \n[main quest]\n4019 \n"
	return source
}

// p6bParentFixtureQuests 是父任务入口的夹具：家族 4010/4011/4012 全部 completed
// （Progress 里还有一条 4011 的旧行），childless / 悬空 / 不完整家族的父任务与子任务、
// 以及链外任务 4013 也都是 completed。
func p6bParentFixtureQuests() *dnfrepo.QuestRecord {
	return &dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			4010: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4011: {Status: "completed", UpdatedAt: questMutationFixtureTime,
				Extra: map[string]string{"reward_state": "granted"}},
			4012: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4013: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4014: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4015: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4016: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4017: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4018: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4019: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4020: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			4021: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		},
		Progress: map[int64]dnfrepo.QuestState{
			4011: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		},
	}
}

// p6bParentRowFingerprint 把任务账投影成与时间无关的稳定文本（写成功时 UpdatedAt 是
// time.Now()，不能进"逐字节"比较）。
func p6bParentRowFingerprint(record dnfrepo.QuestRecord) string {
	rows := make([]string, 0, len(record.States)+len(record.Progress))
	appendRow := func(table string, questID int64, state dnfrepo.QuestState) {
		extra := make([]string, 0, len(state.Extra))
		for key, value := range state.Extra {
			extra = append(extra, key+"="+value)
		}
		sort.Strings(extra)
		rows = append(rows, table+" "+int64ListText([]int64{questID})+" status="+state.Status+
			" progress="+int64ListText([]int64{state.ProgressValue})+" extra="+strings.Join(extra, ","))
	}
	for questID, state := range record.States {
		appendRow("states", questID, state)
	}
	for questID, state := range record.Progress {
		appendRow("progress", questID, state)
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// p6bParentResponseKeys 返回响应体的键集（用来证明父/子两个入口的响应**同形**）。
func p6bParentResponseKeys(response map[string]any) []string {
	keys := make([]string, 0, len(response))
	for key := range response {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// 契约 1：默认档 = P5 的单条删除（逐字节）
// ---------------------------------------------------------------------------

// p6bParentOffGolden 是**改前树**（P6 落定快照）在开关未设时对 `uncomplete(4010)` 的
// 响应体原文：`removedStates=true`、`removedProgress=false`，**没有** chainReset。
// 它同时是 P5 时代的行为（P5 从未拦过 MainQuestID == 0 的任务）。
//
// 这是一条"逐字节"契约：P6b 之后默认档必须还是这一串。要改它必须拿出面板/真档证据，
// 不许顺手改期望值。
const p6bParentOffGolden = `{"changed":true,"characterId":1,"operation":"uncomplete",` +
	`"professionChanged":false,"quest":{"acceptable":true,"active":false,"cantGiveUp":false,` +
	`"collisions":[],"completed":false,"grade":"[epic]","jobChangeQuest":0,"levelMax":99,` +
	`"levelMin":1,"mainQuestId":0,"name":"主线父任务","path":"n_quest/q4010.qst",` +
	`"prerequisites":[],"professionQuest":false,"progressValue":0,"questId":4010,"rewardType":"",` +
	`"status":"available","type":"[quest clear]"},"questId":4010,"removedProgress":false,` +
	`"removedStates":true,"rewardGranted":false,"success":true}`

func TestP6bParentChainResetDefaultOffKeepsP5SingleRowDeleteByteForByte(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: p6bParentQuestSource(),
		quests: p6bParentFixtureQuests(),
	})

	status, response := harness.uncomplete(t, 4010)
	requireOK(t, status, response)
	if got := p6JSON(t, response); got != p6bParentOffGolden {
		t.Fatalf("默认档父任务反完成的响应体必须与 P5 逐字节相同:\ngot =%s\nwant=%s", got, p6bParentOffGolden)
	}
	// 半截操作的**证据**：父行没了，子任务行原样留在库里（这正是 P6b 要修的现象，
	// 默认档仍然如此 —— 开关没开就不许改行为）。
	record := harness.questRecord(t)
	if _, exists := record.States[4010]; exists {
		t.Fatalf("默认档必须删掉父行: %+v", record.States[4010])
	}
	fixture := p6bParentFixtureQuests()
	for questID, want := range fixture.States {
		if questID == 4010 {
			continue
		}
		got := record.States[questID]
		if got.Status != want.Status || got.ProgressValue != want.ProgressValue || len(got.Extra) != len(want.Extra) {
			t.Fatalf("默认档只允许删父行，任务 %d 却被改动:\ngot =%+v\nwant=%+v", questID, got, want)
		}
	}
	if _, exists := record.Progress[4011]; !exists {
		t.Fatalf("默认档不得动 Progress 里的家族旧行: %+v", record.Progress)
	}
	if _, hijacked := response["chainReset"]; hijacked {
		t.Fatalf("默认档不得出现 chainReset 键: %+v", response)
	}
	if harness.hasEvent("gm-quest-chain-reset") {
		t.Fatalf("默认档不得记链路重置事件: %v", harness.eventKinds())
	}
	if !harness.hasEvent("gm-quest-uncomplete") {
		t.Fatalf("默认档必须记 P5 的反完成事件: %v", harness.eventKinds())
	}
}

func TestP6bParentChainResetExplicitOffKeepsP5SingleRowDelete(t *testing.T) {
	t.Setenv(questChainResetEnv, "off")
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: p6bParentQuestSource(),
		quests: p6bParentFixtureQuests(),
	})
	_, response := harness.uncomplete(t, 4010)
	if got := p6JSON(t, response); got != p6bParentOffGolden {
		t.Fatalf("显式 off 必须与默认档逐字节同口径:\ngot =%s\nwant=%s", got, p6bParentOffGolden)
	}
	if _, exists := harness.questRecord(t).States[4010]; exists {
		t.Fatal("显式 off 下父行必须被删掉（P5 单条语义）")
	}
}

// ---------------------------------------------------------------------------
// 契约 2：开关 on + 父任务有家族 = 整族重置，且与子任务路径同形
// ---------------------------------------------------------------------------

func TestP6bParentChainResetSwitchOnResetsTheWholeFamilyFromTheParentRow(t *testing.T) {
	t.Setenv(questChainResetEnv, "on")
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: p6bParentQuestSource(),
		quests: p6bParentFixtureQuests(),
	})

	status, response := harness.uncomplete(t, 4010)
	requireOK(t, status, response)
	if response["chainReset"] != true || response["parentQuestId"] != float64(4010) {
		t.Fatalf("父任务入口必须如实回报 parentQuestId = 请求的任务自身: %+v", response)
	}
	if got := responseList(t, response, "removedQuestIds"); len(got) != 3 ||
		got[0] != float64(4010) || got[1] != float64(4011) || got[2] != float64(4012) {
		t.Fatalf("removedQuestIds = %v, want [4010 4011 4012]（升序）", got)
	}
	reactivated := responseList(t, response, "reactivatedQuestIds")
	if len(reactivated) != 3 || reactivated[0] != float64(4010) ||
		reactivated[1] != float64(4011) || reactivated[2] != float64(4012) {
		t.Fatalf("reactivatedQuestIds = %v, want [4010 4011 4012]（父在前、PVF 顺序）", reactivated)
	}
	// 整族被重建成 active；父不带 main_quest_id，子带父任务标记。
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
	// Progress 里的家族旧行必须被清掉；家族外的行一行不动。
	record := harness.questRecord(t)
	for _, questID := range []int64{4010, 4011, 4012} {
		if _, exists := record.Progress[questID]; exists {
			t.Fatalf("家族成员 %d 的 Progress 旧行没有被清掉: %+v", questID, record.Progress)
		}
	}
	for _, questID := range []int64{4013, 4014, 4015, 4016, 4017, 4018, 4019, 4020, 4021} {
		if state := record.States[questID]; state.Status != "completed" {
			t.Fatalf("家族外任务 %d 被改动: %+v", questID, state)
		}
		if _, exists := record.Progress[questID]; exists {
			t.Fatalf("家族外任务 %d 不得被写入 Progress: %+v", questID, record.Progress)
		}
	}
	// 具名审计事件（带 parent_quest_id，并标出这是父任务入口）。
	if !harness.hasEvent("gm-quest-chain-reset") {
		t.Fatalf("父任务入口必须记 gm-quest-chain-reset: %v", harness.eventKinds())
	}
	fields := harness.eventFields(t, "gm-quest-chain-reset")
	if fields["parent_quest_id"] != int64(4010) {
		t.Fatalf("审计事件必须带 parent_quest_id=4010: %+v", fields)
	}
	if fields["from_parent"] != true {
		t.Fatalf("审计事件必须标出这是父任务入口: %+v", fields)
	}

	// **同形**：同一个家族，从父任务点与从子任务点，响应键集与链路字段必须一致。
	childHarness := newQuestWriteHarness(t, questWriteFixture{
		source: p6bParentQuestSource(),
		quests: p6bParentFixtureQuests(),
	})
	childStatus, childResponse := childHarness.uncomplete(t, 4011)
	requireOK(t, childStatus, childResponse)
	if got, want := strings.Join(p6bParentResponseKeys(response), ","), strings.Join(p6bParentResponseKeys(childResponse), ","); got != want {
		t.Fatalf("父/子入口的响应键集必须同形:\nparent=%s\nchild =%s", got, want)
	}
	for _, key := range []string{"parentQuestId", "removedQuestIds", "reactivatedQuestIds",
		"operation", "chainReset", "changed", "rewardGranted", "professionChanged"} {
		if p6JSON(t, response[key]) != p6JSON(t, childResponse[key]) {
			t.Fatalf("父/子入口的 %s 必须一致:\nparent=%v\nchild =%v", key, response[key], childResponse[key])
		}
	}
	// 只有 questId 与 quest 摘要不同（那是"请求的是哪一行"的如实回报），库内容一致。
	if response["questId"] == childResponse["questId"] {
		t.Fatalf("父/子入口的 questId 必须各自如实回报: %+v", response)
	}
	if got, want := p6bParentRowFingerprint(record), p6bParentRowFingerprint(childHarness.questRecord(t)); got != want {
		t.Fatalf("父/子入口必须产出同一份库内容:\nparent=%s\nchild =%s", got, want)
	}
}

// ---------------------------------------------------------------------------
// 契约 3：没有家族可重置时，开关 on 也不改变单条语义
// ---------------------------------------------------------------------------

func TestP6bParentChainResetSwitchOnKeepsChildlessAndOrdinarySingleRowPath(t *testing.T) {
	// 三格：childless 的 quest clear 父任务、[int data] 全悬空的 quest clear 父任务、
	// 普通任务。开关 off 与 on 的响应必须逐字节相同，库内容也必须相同。
	for _, questID := range []int64{4014, 4016, 4013} {
		t.Setenv(questChainResetEnv, "off")
		off := newQuestWriteHarness(t, questWriteFixture{
			source: p6bParentQuestSource(), quests: p6bParentFixtureQuests(),
		})
		offStatus, offResponse := off.uncomplete(t, questID)
		requireOK(t, offStatus, offResponse)

		t.Setenv(questChainResetEnv, "on")
		on := newQuestWriteHarness(t, questWriteFixture{
			source: p6bParentQuestSource(), quests: p6bParentFixtureQuests(),
		})
		onStatus, onResponse := on.uncomplete(t, questID)
		requireOK(t, onStatus, onResponse)

		if got, want := p6JSON(t, onResponse), p6JSON(t, offResponse); got != want {
			t.Fatalf("任务 %d 没有可整族重置的家族，开关 on 不得改变单条语义:\non =%s\noff=%s", questID, got, want)
		}
		if _, hijacked := onResponse["chainReset"]; hijacked {
			t.Fatalf("任务 %d 不得被链路分支接管: %+v", questID, onResponse)
		}
		if onResponse["removedStates"] != true {
			t.Fatalf("任务 %d 必须保持 P5 的逐条语义: %+v", questID, onResponse)
		}
		if on.hasEvent("gm-quest-chain-reset") {
			t.Fatalf("任务 %d 不是家族重置，不得记链路事件: %v", questID, on.eventKinds())
		}
		if got, want := p6bParentRowFingerprint(on.questRecord(t)), p6bParentRowFingerprint(off.questRecord(t)); got != want {
			t.Fatalf("任务 %d 的开关 on/off 库内容必须一致:\non =%s\noff=%s", questID, got, want)
		}
		// 该任务自己的行必须真的被删掉（不是"拒绝了"）。
		if _, exists := on.questRecord(t).States[questID]; exists {
			t.Fatalf("任务 %d 的行必须被删掉", questID)
		}
		// 家族成员一行不动（childless/悬空父任务的 [int data] 成员不是受管子任务）。
		if questID == 4014 {
			if state := on.questRecord(t).States[4015]; state.Status != "completed" {
				t.Fatalf("4014 的引用任务 4015 被改动: %+v", state)
			}
		}
	}
	// 显式证明判据本身：这三格都不是"可整族重置的家族根"。
	probe := newQuestWriteHarness(t, questWriteFixture{pvf: nil, source: p6bParentQuestSource(),
		quests: p6bParentFixtureQuests()})
	catalog, err := probe.server.pvf.questCatalog(probe.server)
	if err != nil {
		t.Fatalf("load fixture quest catalog: %v", err)
	}
	for _, questID := range []int64{4014, 4016, 4013} {
		if family, ok := catalog.ManagedLinkedQuestChainFamily(questID); ok {
			t.Fatalf("任务 %d 不得被判定为家族根: %v", questID, family)
		}
	}
	if family, ok := catalog.ManagedLinkedQuestChainFamily(4010); !ok || len(family) != 3 {
		t.Fatalf("任务 4010 必须被判定为家族根: %v ok=%v", family, ok)
	}
}

// ---------------------------------------------------------------------------
// 契约 4：家族数据不完整 → 具名拒绝 + 零写入
// ---------------------------------------------------------------------------

func TestP6bParentChainResetSwitchOnRejectsIncompleteFamiliesWithZeroWrites(t *testing.T) {
	t.Setenv(questChainResetEnv, "on")

	cases := []struct {
		name    string
		questID int64
		quests  *dnfrepo.QuestRecord
		reason  string
		mention string
	}{
		{
			name: "父任务 [int data] 点名了目录外的子任务", questID: 4017,
			quests: p6bParentFixtureQuests(), reason: "chain_child_unresolved", mention: "4998",
		},
		{
			name: "家族成员按当前资格重建不回来", questID: 4019,
			quests: p6bParentFixtureQuests(), reason: "chain_incomplete_reactivation", mention: "4021",
		},
		{
			name: "父任务行不存在", questID: 4010,
			quests: &dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
				4011: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			}},
			reason: "chain_row_missing", mention: "4010",
		},
	}
	for _, testCase := range cases {
		harness := newQuestWriteHarness(t, questWriteFixture{
			source: p6bParentQuestSource(), quests: testCase.quests,
		})
		before := harness.questFingerprint(t)
		status, response := harness.uncomplete(t, testCase.questID)
		message := requireFailure(t, status, response)
		if response["reason"] != testCase.reason {
			t.Fatalf("%s: reason = %v, want %s（%+v）", testCase.name, response["reason"], testCase.reason, response)
		}
		if !strings.Contains(message, testCase.mention) || !strings.Contains(message, "本次未做任何写入") {
			t.Fatalf("%s: 拒绝文案必须点名 %s 且声明零写入: %q", testCase.name, testCase.mention, message)
		}
		harness.requireZeroWrite(t, before, testCase.name)
		if !harness.hasEvent("gm-quest-mutation-rejected") {
			t.Fatalf("%s: 必须记具名拒绝事件: %v", testCase.name, harness.eventKinds())
		}
		if harness.hasEvent("gm-quest-chain-reset") {
			t.Fatalf("%s: 拒绝路径不得记链路重置事件: %v", testCase.name, harness.eventKinds())
		}
	}
	// 新哨兵必须真的被面板翻译成具名 reason（不是落到 chain_reset_failed 兜底）。
	if rejection := questChainRejection(4014, dnfquest.ErrQuestChainNoManagedChildren); rejection.reason != "chain_no_managed_children" {
		t.Fatalf("ErrQuestChainNoManagedChildren 的面板 reason = %q", rejection.reason)
	}
}

// ---------------------------------------------------------------------------
// 真档 Script.pvf 端到端
// ---------------------------------------------------------------------------

// TestRealPVFQuestChainResetFromParentSwitchOnResetsTheWholeFamily 在真档上跑通"点父
// 任务那一行"的整条链路：真档家族 3146 -> 3157 / 3054（P6 已取证），前置任务 3145 已完成、
// 另一条家族 3971/3972 在账。
//
// 断言：
//   - 默认档：仍然是 P5 的单条删除（父行没了、3157/3054 留在库里）—— P6 §8-② 的缺口现象；
//   - 开关 on：整族重建（3146 + 3157 + 3054 全 active），链外 3145/3971/3972 逐字段不变；
//   - 与子任务入口（3054）产出同一份库内容与同一组链路字段；
//   - 缺行 → chain_row_missing 且零写入；真档 childless 家族根（6511）→ 仍走单条路径。
func TestRealPVFQuestChainResetFromParentSwitchOnResetsTheWholeFamily(t *testing.T) {
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
				6511: {Status: "completed", UpdatedAt: questMutationFixtureTime},
			},
		}
	}
	chainCharacter := dnfrepo.CharacterRecord{
		AccountID: "acc-1", Name: "tester", Job: "0", Level: 10,
		Stats: map[string]int64{"grow_type": 0},
	}

	// 默认档：P5 的单条删除（缺口现象，逐项取证）。
	offHarness := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: chainCharacter, quests: fixture()})
	offStatus, offResponse := offHarness.uncomplete(t, 3146)
	requireOK(t, offStatus, offResponse)
	if offResponse["removedStates"] != true {
		t.Fatalf("真档默认档必须保持 P5 单条语义: %+v", offResponse)
	}
	if _, hijacked := offResponse["chainReset"]; hijacked {
		t.Fatalf("真档默认档不得走链路重置: %+v", offResponse)
	}
	offRecord := offHarness.questRecord(t)
	if _, exists := offRecord.States[3146]; exists {
		t.Fatal("真档默认档必须删掉父行 3146")
	}
	for _, questID := range []int64{3157, 3054} {
		if state := offRecord.States[questID]; state.Status != "completed" {
			t.Fatalf("缺口现象取证：默认档把子任务 %d 留在库里（%+v）", questID, state)
		}
	}

	// 开关 on：点父任务 = 整族重置。
	t.Setenv(questChainResetEnv, "on")
	harness := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: chainCharacter, quests: fixture()})
	status, response := harness.uncomplete(t, 3146)
	if response["success"] != true {
		message, _ := response["error"].(string)
		before := harness.questFingerprint(t)
		harness.requireZeroWrite(t, before, "真档父任务链路重置失败路径")
		t.Fatalf("真档父任务链路重置失败: %q (%+v)", message, response)
	}
	requireOK(t, status, response)
	if response["chainReset"] != true || response["parentQuestId"] != float64(3146) {
		t.Fatalf("真档父任务 = %+v, want parentQuestId=3146", response)
	}
	reactivated := responseList(t, response, "reactivatedQuestIds")
	if len(reactivated) != 3 || reactivated[0] != float64(3146) {
		t.Fatalf("真档 reactivatedQuestIds = %v, want [3146 3157 3054]", reactivated)
	}
	parentState, found := harness.questState(t, 3146)
	if !found || parentState.Status != "active" || parentState.Extra["pvf_path"] == "" {
		t.Fatalf("真档父任务 3146 状态 = %+v found=%v", parentState, found)
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
	if fields := harness.eventFields(t, "gm-quest-chain-reset"); fields["parent_quest_id"] != int64(3146) || fields["from_parent"] != true {
		t.Fatalf("真档审计事件字段不对: %+v", fields)
	}

	// 与子任务入口（3054）逐项对照：同一家族、同一份库内容。
	childHarness := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: chainCharacter, quests: fixture()})
	childStatus, childResponse := childHarness.uncomplete(t, 3054)
	requireOK(t, childStatus, childResponse)
	for _, key := range []string{"parentQuestId", "removedQuestIds", "reactivatedQuestIds"} {
		if p6JSON(t, response[key]) != p6JSON(t, childResponse[key]) {
			t.Fatalf("真档父/子入口的 %s 必须一致:\nparent=%v\nchild =%v", key, response[key], childResponse[key])
		}
	}
	if got, want := p6bParentRowFingerprint(harness.questRecord(t)), p6bParentRowFingerprint(childHarness.questRecord(t)); got != want {
		t.Fatalf("真档父/子入口必须产出同一份库内容:\nparent=%s\nchild =%s", got, want)
	}

	// 具名拒绝（真档）：父任务行不在任务账里。
	emptyHarness := newQuestWriteHarness(t, questWriteFixture{
		pvf: source, character: chainCharacter,
		quests: &dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			3971: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		}},
	})
	emptyBefore := emptyHarness.questFingerprint(t)
	_, response = emptyHarness.uncomplete(t, 3146)
	if response["reason"] != "chain_row_missing" {
		t.Fatalf("真档缺父任务行必须具名拒绝: %+v", response)
	}
	emptyHarness.requireZeroWrite(t, emptyBefore, "真档父任务链路重置遇到缺失的行")

	// 真档 childless 家族根：6511 的 [int data] 指向目录外、没有任何受管子任务，
	// 开关 on 时**仍然只删它这一行**（没有家族可重置，绝不编造）。
	childlessHarness := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: chainCharacter, quests: fixture()})
	childlessStatus, childlessResponse := childlessHarness.uncomplete(t, 6511)
	requireOK(t, childlessStatus, childlessResponse)
	if _, hijacked := childlessResponse["chainReset"]; hijacked {
		t.Fatalf("真档 6511（无受管子任务）不得被链路分支接管: %+v", childlessResponse)
	}
	if childlessResponse["removedStates"] != true {
		t.Fatalf("真档 6511 必须保持单条语义: %+v", childlessResponse)
	}
	if _, exists := childlessHarness.questRecord(t).States[6511]; exists {
		t.Fatal("真档 6511 的行必须被删掉")
	}
}
