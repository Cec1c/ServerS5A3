package gmtool

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// 批次 3 项 ⑥：任务完成页补齐 —— 「已接任务批量完成」的服务端测试台
//
// 证据强度对齐 P5/P6 的任务写面测试台（api_quest_mutations_test.go）：
//
//	1. 写面断言一律**读库比对**（不看响应自述）；
//	2. 拒绝路径断言**零写入**，且用的是改名那批的**事务计数包装**
//	   （installRenameCounters → renameSettlementCounter）：被拒绝的请求连
//	   写事务都不许开；
//	3. 前端接线按 TestRenameFrontendIsWired / TestQuestMutationFrontendAssetsAreEmbedded
//	   的静态检查口径钉死（内嵌资产 + 路由注册表交叉核对）。
//
// 对应实现：internal/services/gmtool/api_quest_active_batch.go
// 上游参考：Temp\gm-upstream-v260910\Services\GmService.Quests.cs
//   - CompleteVisibleQuestBatch :1297-1344（逐条 skipped + 跳过每日任务）
//   - ForceCompleteQuest        :1213-1273（不发奖励）
//   - IsDailyQuestGrade         :365-370
// ---------------------------------------------------------------------------

// questActiveBatchSource 在 P5 的假 PVF 目录上补两个任务：
//
//	4004 每日任务（[grade] `[daily]`）—— 批量完成必须跳过（上游 :365-370）
//	4005 普通任务 —— 用来钉「没接过的普通任务也会被拒绝」
func questActiveBatchSource() readonlyTestSource {
	source := questMutationSource()
	base := "[type]\n`[quest clear]` \n[level]\n1 99 \n[job]\n`[all]` \n"
	source[dnfquest.DefaultList] = source[dnfquest.DefaultList] + "4004 `q4004.qst`\n4005 `q4005.qst`\n"
	source["n_quest/q4004.qst"] = "[grade]\n`[daily]` \n" + base + "[name]\n`每日任务` \n"
	source["n_quest/q4005.qst"] = "[grade]\n`[normal]` \n" + base + "[name]\n`没接过的任务` \n"
	return source
}

// questActiveBatchQuests 是「当前已接」的干净夹具：只含两条可完成的已接任务
// （3972 普通、4002 不可放弃的普通任务）+ 一条已完成（3971，不该进派生集合）。
func questActiveBatchQuests() *dnfrepo.QuestRecord {
	return &dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			3971: {Status: "completed", ProgressValue: 3, UpdatedAt: questMutationFixtureTime},
			3972: {
				Status: "active", ProgressValue: 5, UpdatedAt: questMutationFixtureTime,
				Extra: map[string]string{"completion_key": "key-3972", "quest_type": "quest clear"},
			},
			4002: {Status: "active", ProgressValue: 1, UpdatedAt: questMutationFixtureTime},
		},
	}
}

// questActiveBatchMixedQuests 在干净夹具上再叠三种「派生集合里但不可完成」的行：
// 3999（不在 Script.pvf）、4000（职业任务）、4004（每日任务）。
func questActiveBatchMixedQuests() *dnfrepo.QuestRecord {
	return &dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			3971: {Status: "completed", ProgressValue: 3, UpdatedAt: questMutationFixtureTime},
			3972: {Status: "active", ProgressValue: 5, UpdatedAt: questMutationFixtureTime},
			3999: {Status: "active", ProgressValue: 2, UpdatedAt: questMutationFixtureTime},
			4000: {Status: "active", ProgressValue: 0, UpdatedAt: questMutationFixtureTime},
			4002: {Status: "active", ProgressValue: 1, UpdatedAt: questMutationFixtureTime},
			4004: {Status: "active", ProgressValue: 0, UpdatedAt: questMutationFixtureTime},
		},
	}
}

// activeBatch 打一次 POST /api/characters/1/quests/active/complete-batch。
// body 里没有 questIds 键 = 派生档（nil → JSON 里是 null，等价于"没给"）。
func (h *questWriteHarness) activeBatch(t *testing.T, questIDs []int64) (int, map[string]any) {
	t.Helper()
	body := map[string]any{}
	if questIDs != nil {
		body["questIds"] = questIDs
	}
	return h.post(t, "/api/characters/1/quests/active/complete-batch", map[string]string{"id": "1"},
		body, h.server.handleQuestActiveCompleteBatch)
}

// activeBatchOutcomes 把 results[] 折成 questId → outcome/reason 便于断言。
func activeBatchOutcomes(t *testing.T, response map[string]any) map[float64][2]string {
	t.Helper()
	out := map[float64][2]string{}
	for _, raw := range responseList(t, response, "results") {
		row := raw.(map[string]any)
		id, _ := row["questId"].(float64)
		outcome, _ := row["outcome"].(string)
		reason, _ := row["reason"].(string)
		out[id] = [2]string{outcome, reason}
	}
	return out
}

func activeBatchIDList(t *testing.T, response map[string]any, key string) []float64 {
	t.Helper()
	ids := []float64{}
	for _, raw := range responseList(t, response, key) {
		value, _ := raw.(float64)
		ids = append(ids, value)
	}
	return ids
}

// ---------------------------------------------------------------------------
// 开关语义（与 api_character_rename_test.go 的改名开关逐条同形）
// ---------------------------------------------------------------------------

func TestQuestModeFromEnvDefaultsToEnabled(t *testing.T) {
	// 未设置/空/未知值（含笔误）= 默认档启用。
	for _, value := range []string{"", " ", "on", "ON", "true", "1", "yes", "enable", "enabled", "quest", "offf"} {
		if !QuestModeFromEnv(value) {
			t.Fatalf("QuestModeFromEnv(%q) = false, want 默认档启用", value)
		}
	}
	// 显式回退档：只有这几个字面量才算关闭。
	for _, value := range []string{"off", "OFF", " off ", "0", "false", "no", "disable", "disabled", "none", "legacy"} {
		if QuestModeFromEnv(value) {
			t.Fatalf("QuestModeFromEnv(%q) = true, want 关闭", value)
		}
	}
}

func TestQuestModeFromEnvironmentReadsProcessEnv(t *testing.T) {
	t.Setenv(questModeEnv, "off")
	if QuestModeFromEnvironment() {
		t.Fatalf("%s=off 时必须关闭", questModeEnv)
	}
	t.Setenv(questModeEnv, "")
	if !QuestModeFromEnvironment() {
		t.Fatalf("%s 为空时必须取默认档启用", questModeEnv)
	}
}

// 关闭档：路由**根本不注册**，请求落 /api/ 兜底 = 与加这条路由之前逐字节相同。
func TestQuestActiveBatchRouteIsNotRegisteredWhenDisabled(t *testing.T) {
	t.Setenv(questModeEnv, "off")
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	recorder := httpDoJSON(t, handler, token, http.MethodPost,
		"/api/characters/1/quests/active/complete-batch", map[string]any{})
	if !strings.Contains(recorder.Body.String(), unportedFeatureMessage) {
		t.Fatalf("关闭档必须落 /api/ 兜底，实际: %s", recorder.Body.String())
	}
}

// 默认档：路由注册、要登录门禁、命中自己的 handler（不是兜底）。
func TestQuestActiveBatchRouteIsRegisteredByDefaultAndRequiresSession(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	handler := harness.server.routes()

	recorder := httpDoJSON(t, handler, "", http.MethodPost,
		"/api/characters/1/quests/active/complete-batch", map[string]any{})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status = %d, want 401（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"loginRequired":true`) {
		t.Fatalf("未登录 body = %s, want loginRequired:true", recorder.Body.String())
	}

	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	recorder = httpDoJSON(t, handler, token, http.MethodPost,
		"/api/characters/1/quests/active/complete-batch", map[string]any{})
	if strings.Contains(recorder.Body.String(), unportedFeatureMessage) {
		t.Fatalf("默认档不该落兜底: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"success":true`) {
		t.Fatalf("默认档按已接任务批量完成应成功: %s", recorder.Body.String())
	}
}

// 关闭档的 handler 直调（路由没注册时唯一能到达 handler 的路径）：具名拒绝 + 零写入。
func TestQuestActiveBatchHandlerRefusesWhenDisabledWithZeroWrites(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	before := harness.questFingerprint(t)
	counter := installRenameCounters(t, harness.gmtoolWriteHarness)
	t.Setenv(questModeEnv, "off")

	_, response := harness.activeBatch(t, nil)
	message := requireFailure(t, http.StatusOK, response)
	if !strings.Contains(message, questModeEnv) {
		t.Fatalf("关闭文案必须点名开关 %s，实际 %q", questModeEnv, message)
	}
	if counter.count() != 0 {
		t.Fatalf("关闭档必须零写入（写事务数=%d）", counter.count())
	}
	harness.requireZeroWrite(t, before, "关闭档")
	if !harness.hasEvent("gm-quest-mode-disabled") {
		t.Fatalf("关闭档必须记 gm-quest-mode-disabled，实际事件 %v", harness.eventKinds())
	}
}

func TestQuestModeSwitchIsReportedInEnvironment(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questActiveBatchQuests()})
	found := false
	for _, entry := range harness.server.envSwitchReport() {
		if entry.Name != questModeEnv {
			continue
		}
		found = true
		if !entry.Enabled {
			t.Fatalf("%s 默认档必须在环境报告里显示为启用", questModeEnv)
		}
		if entry.DefaultWhenUnset != "on" {
			t.Fatalf("DefaultWhenUnset = %q, want on", entry.DefaultWhenUnset)
		}
	}
	if !found {
		t.Fatalf("环境报告缺少 %s", questModeEnv)
	}
	t.Setenv(questModeEnv, "off")
	for _, entry := range harness.server.envSwitchReport() {
		if entry.Name == questModeEnv && entry.Enabled {
			t.Fatalf("%s=off 时环境报告必须显示为关闭", questModeEnv)
		}
	}
}

// 广告位：新 UI 的渲染条件。启用档有 completionBatch，关闭档**没有这个键**
// （前端据此不渲染任何新控件 → 面板与今天逐字节相同）。
func TestQuestOverviewAdvertisesCompletionBatchOnlyWhenEnabled(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	_, response := harness.get(t, "/api/characters/1/quests",
		map[string]string{"id": "1"}, harness.server.handleQuestOverview)
	requireOK(t, http.StatusOK, response)
	advertisement := responseMap(t, response, "completionBatch")
	if advertisement["enabled"] != true || advertisement["mode"] != "enabled" {
		t.Fatalf("启用档必须广告 completionBatch: %+v", advertisement)
	}
	if advertisement["path"] != "/api/characters/{id}/quests/active/complete-batch" {
		t.Fatalf("广告位的路径 = %v", advertisement["path"])
	}
	// 既有字段一个都不能少（本批只**新增**一个键）。
	for _, key := range []string{"characterId", "activeCount", "completedCount", "active", "completed"} {
		if _, exists := response[key]; !exists {
			t.Fatalf("任务总览缺少既有字段 %s: %+v", key, response)
		}
	}

	t.Setenv(questModeEnv, "off")
	_, response = harness.get(t, "/api/characters/1/quests",
		map[string]string{"id": "1"}, harness.server.handleQuestOverview)
	requireOK(t, http.StatusOK, response)
	if _, exists := response["completionBatch"]; exists {
		t.Fatalf("关闭档不得回 completionBatch（前端不渲染新控件的唯一依据）: %+v", response["completionBatch"])
	}
}

// ---------------------------------------------------------------------------
// 派生档：以「角色当前已接」为输入
// ---------------------------------------------------------------------------

func TestQuestActiveCompleteBatchCompletesEveryAcceptedQuest(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	counter := installRenameCounters(t, harness.gmtoolWriteHarness)
	_, response := harness.activeBatch(t, nil)
	requireOK(t, http.StatusOK, response)

	if response["derived"] != true || response["requested"] != float64(2) || response["changed"] != float64(2) {
		t.Fatalf("派生档必须完成两条已接任务: %+v", response)
	}
	if response["unchanged"] != float64(0) {
		t.Fatalf("两条都应被写入: %+v", response)
	}
	if response["rewardGranted"] != false || response["professionChanged"] != false {
		t.Fatalf("GM 批量完成从不发奖励、不改职业: %+v", response)
	}
	if ids := activeBatchIDList(t, response, "changedIds"); len(ids) != 2 || ids[0] != 3972 || ids[1] != 4002 {
		t.Fatalf("changedIds = %v, want [3972 4002]", ids)
	}
	if ids := activeBatchIDList(t, response, "activeQuestIds"); len(ids) != 2 {
		t.Fatalf("响应必须回报派生输入（当前已接）: %v", ids)
	}
	if counter.count() != 1 {
		t.Fatalf("成功路径必须正好 1 个写事务（calls=%d）", counter.count())
	}
	// 读库比对（不看响应自述）。
	for _, questID := range []int64{3972, 4002} {
		state, found := harness.questState(t, questID)
		if !found || state.Status != questCompletedLiteral || state.ProgressValue != 0 {
			t.Fatalf("%d 落库状态 = %+v found=%v", questID, state, found)
		}
		if state.Extra[questActiveBatchCompletedKey] != "true" || state.Extra[questRewardsGrantedKey] != "false" {
			t.Fatalf("%d 缺少批量完成标记：%+v", questID, state.Extra)
		}
		if state.Extra[questForceCompletedKey] == "true" {
			t.Fatalf("%d 不得打上单条强制的章（那是另一条路由）: %+v", questID, state.Extra)
		}
		if state.Extra[questPVFPathKey] == "" {
			t.Fatalf("%d 必须记 PVF 路径: %+v", questID, state.Extra)
		}
		// 结算回执必须被清掉（同 complete:794-796）：3972 原本带一个 completion_key。
		if _, exists := state.Extra["completion_key"]; exists {
			t.Fatalf("%d 的结算回执键没清干净: %+v", questID, state.Extra)
		}
		// 只留 States 一份权威行。
		if _, exists := harness.questRecord(t).Progress[questID]; exists {
			t.Fatalf("%d 必须从 Progress 里删掉", questID)
		}
	}
	// 已完成的行（3971）一个字节都不许动。
	state, _ := harness.questState(t, 3971)
	if state.Status != "completed" || state.ProgressValue != 3 {
		t.Fatalf("已完成的任务必须原样保留: %+v", state)
	}
}

// 已接任务逐条完成：既有的单条路由（面板行内「强制完成」按钮打的就是它）+ 新路由的单条请求。
func TestQuestActiveCompleteBatchIndividualCompletionOfAcceptedQuest(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})

	// ① 面板行内按钮 → 既有路由 POST …/quests/{questId}/complete。
	_, response := harness.complete(t, 3972)
	requireOK(t, http.StatusOK, response)
	if response["alreadyCompleted"] != false {
		t.Fatalf("首次强条完成不得是幂等回放: %+v", response)
	}
	state, found := harness.questState(t, 3972)
	if !found || state.Status != questCompletedLiteral {
		t.Fatalf("逐条完成后 3972 落库 = %+v found=%v", state, found)
	}
	if state.Extra[questForceCompletedKey] != "true" {
		t.Fatalf("逐条完成必须打单条强制的章: %+v", state.Extra)
	}

	// ② 新路由只点名这一条（4002 仍在已接但没被点名 → 不许被顺手完成）。
	_, response = harness.activeBatch(t, []int64{4002})
	requireOK(t, http.StatusOK, response)
	if response["derived"] != false || response["requested"] != float64(1) || response["changed"] != float64(1) {
		t.Fatalf("显式单条请求必须只动一条: %+v", response)
	}
	if state, found := harness.questState(t, 4002); !found || state.Status != questCompletedLiteral {
		t.Fatalf("4002 完成后落库 = %+v found=%v", state, found)
	}
	if state, _ := harness.questState(t, 3972); state.Extra[questActiveBatchCompletedKey] == "true" {
		t.Fatalf("新路由不得重写刚才那条（不同标记）: %+v", state.Extra)
	}
}

// 幂等：派生档第二次调用时「已接集合」已经空了 → changed=0 且**不开写事务**。
func TestQuestActiveCompleteBatchIsIdempotentOnRepeat(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	counter := installRenameCounters(t, harness.gmtoolWriteHarness)

	_, response := harness.activeBatch(t, nil)
	requireOK(t, http.StatusOK, response)
	if response["changed"] != float64(2) || counter.count() != 1 {
		t.Fatalf("第一次应写入 2 条且正好 1 个事务: %+v calls=%d", response, counter.count())
	}
	before := harness.questFingerprint(t)

	_, response = harness.activeBatch(t, nil)
	requireOK(t, http.StatusOK, response)
	if response["changed"] != float64(0) || response["requested"] != float64(0) || response["derived"] != true {
		t.Fatalf("第二次必须如实回报「没有已接任务」: %+v", response)
	}
	if counter.count() != 1 {
		t.Fatalf("幂等重放不许再开写事务（calls=%d, want 1）", counter.count())
	}
	harness.requireZeroWrite(t, before, "派生档幂等重放")

	// 显式点名同样两条：已完成 → 逐条 already_completed，仍然零写入。
	_, response = harness.activeBatch(t, []int64{3972, 4002})
	requireOK(t, http.StatusOK, response)
	if response["changed"] != float64(0) || response["unchanged"] != float64(2) {
		t.Fatalf("已完成的两条必须逐条回报 already_completed: %+v", response)
	}
	outcomes := activeBatchOutcomes(t, response)
	if outcomes[3972] != [2]string{"already_completed", "already_completed"} {
		t.Fatalf("3972 outcome = %v", outcomes[3972])
	}
	if counter.count() != 1 {
		t.Fatalf("全部已完成时不许开写事务（calls=%d, want 1）", counter.count())
	}
	harness.requireZeroWrite(t, before, "全部已完成的显式批量")
}

// ---------------------------------------------------------------------------
// 拒绝路径（零写入）
// ---------------------------------------------------------------------------

// 本路由的核心合法性门：只有**角色真的已经接下**的任务才允许从这里完成。
func TestQuestActiveCompleteBatchRejectsQuestTheCharacterHasNotAccepted(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	before := harness.questFingerprint(t)
	counter := installRenameCounters(t, harness.gmtoolWriteHarness)

	// 3970 / 4005 都在 PVF 里，但任务账里根本没有这两行 = 没接过的任务。
	_, response := harness.activeBatch(t, []int64{3970})
	requireOK(t, http.StatusOK, response)
	if response["changed"] != float64(0) || response["unchanged"] != float64(1) {
		t.Fatalf("没接过的任务必须拒绝: %+v", response)
	}
	outcomes := activeBatchOutcomes(t, response)
	if outcomes[3970] != [2]string{"rejected", "not_accepted"} {
		t.Fatalf("3970 outcome = %v, want rejected/not_accepted", outcomes[3970])
	}
	if ids := activeBatchIDList(t, response, "notAccepted"); len(ids) != 1 || ids[0] != 3970 {
		t.Fatalf("notAccepted = %v", ids)
	}
	message, _ := response["error"].(string)
	if message != "" {
		t.Fatalf("逐条拒绝不该整批失败（success:true + 逐条 reason，上游 :1297-1344 口径）: %+v", response)
	}
	if counter.count() != 0 {
		t.Fatalf("拒绝路径必须零写入（写事务数=%d）", counter.count())
	}
	harness.requireZeroWrite(t, before, "点名没接过的任务")

	// 混合：一条已接 + 一条没接过 → 已接的照做，没接过的逐条拒绝（仍是零副作用）。
	_, response = harness.activeBatch(t, []int64{3972, 4005})
	requireOK(t, http.StatusOK, response)
	outcomes = activeBatchOutcomes(t, response)
	if outcomes[3972] != [2]string{"completed", ""} || outcomes[4005] != [2]string{"rejected", "not_accepted"} {
		t.Fatalf("混合批量逐条结果 = %+v", outcomes)
	}
	if response["changed"] != float64(1) {
		t.Fatalf("mixed changed = %v, want 1", response["changed"])
	}
	if state, found := harness.questState(t, 4005); found {
		t.Fatalf("被拒绝的任务不得留下任何行: %+v", state)
	}
}

// 每日任务与职业任务：跳过并逐条点名原因（上游 CompleteVisibleQuestBatch:1320-1325
// 的 skippedDaily；职业任务在本仓是「会改职业账」，一律不碰）。
func TestQuestActiveCompleteBatchSkipsDailyAndProfessionWithReasons(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchMixedQuests()})
	beforeDaily, _ := harness.questState(t, 4004)
	beforeProfession, _ := harness.questState(t, 4000)

	_, response := harness.activeBatch(t, nil)
	requireOK(t, http.StatusOK, response)
	if response["changed"] != float64(2) || response["unchanged"] != float64(3) {
		t.Fatalf("派生档应完成 3972/4002、跳过 3 条: %+v", response)
	}
	if ids := activeBatchIDList(t, response, "skippedDaily"); len(ids) != 1 || ids[0] != 4004 {
		t.Fatalf("skippedDaily = %v, want [4004]", ids)
	}
	if ids := activeBatchIDList(t, response, "skippedProfession"); len(ids) != 1 || ids[0] != 4000 {
		t.Fatalf("skippedProfession = %v, want [4000]", ids)
	}
	outcomes := activeBatchOutcomes(t, response)
	if outcomes[4004] != [2]string{"rejected", "daily_excluded"} {
		t.Fatalf("4004 outcome = %v", outcomes[4004])
	}
	if outcomes[4000] != [2]string{"rejected", "profession_excluded"} {
		t.Fatalf("4000 outcome = %v", outcomes[4000])
	}
	// 3999 在任务账里是 active、但不在 Script.pvf：逐条点名 PVF，不合成定义。
	if outcomes[3999] != [2]string{"rejected", "not_in_script_pvf"} {
		t.Fatalf("3999 outcome = %v", outcomes[3999])
	}
	// 被跳过的两行一个字节都没动。
	afterDaily, _ := harness.questState(t, 4004)
	afterProfession, _ := harness.questState(t, 4000)
	if afterDaily.Status != beforeDaily.Status || afterDaily.ProgressValue != beforeDaily.ProgressValue {
		t.Fatalf("每日任务不得被写: before=%+v after=%+v", beforeDaily, afterDaily)
	}
	if afterProfession.Status != beforeProfession.Status {
		t.Fatalf("职业任务不得被写: before=%+v after=%+v", beforeProfession, afterProfession)
	}
	if _, exists := afterProfession.Extra["grow_type"]; exists {
		t.Fatalf("职业任务批量完成不得改职业账: %+v", afterProfession.Extra)
	}
}

// 非法编号 / 不在 PVF / 整批超上限：逐条如实回报；超上限与「无一条可做」都零写入。
func TestQuestActiveCompleteBatchRejectsInvalidAndOversizedWithZeroWrites(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(), quests: questActiveBatchQuests()})
	before := harness.questFingerprint(t)
	counter := installRenameCounters(t, harness.gmtoolWriteHarness)

	_, response := harness.activeBatch(t, []int64{0, 70000, 4005})
	requireOK(t, http.StatusOK, response)
	outcomes := activeBatchOutcomes(t, response)
	if outcomes[0] != [2]string{"rejected", "out_of_range"} {
		t.Fatalf("0 outcome = %v", outcomes[0])
	}
	if outcomes[70000] != [2]string{"rejected", "out_of_range"} {
		t.Fatalf("70000 outcome = %v", outcomes[70000])
	}
	if outcomes[4005] != [2]string{"rejected", "not_accepted"} {
		t.Fatalf("4005 outcome = %v", outcomes[4005])
	}
	if counter.count() != 0 {
		t.Fatalf("无一条可做时不许开写事务（calls=%d）", counter.count())
	}
	harness.requireZeroWrite(t, before, "全部非法的批量")

	tooMany := make([]int64, 0, questBatchMaxIDs+1)
	for index := 0; index <= questBatchMaxIDs; index++ {
		tooMany = append(tooMany, int64(6000+index))
	}
	_, response = harness.activeBatch(t, tooMany)
	message := requireFailure(t, http.StatusOK, response)
	if response["reason"] != "batch_size" || !strings.Contains(message, "200") {
		t.Fatalf("201 条必须整批拒绝并说明上限: %+v", response)
	}
	if counter.count() != 0 {
		t.Fatalf("超上限必须零写入（calls=%d）", counter.count())
	}
	harness.requireZeroWrite(t, before, "超上限的批量")
}

// 写失败：整批回滚 + 如实报失败（沿用 P5 的失败注入替身）。
func TestQuestActiveCompleteBatchReportsWriteFailureAndWritesNothing(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		source: questActiveBatchSource(),
		quests: questActiveBatchQuests(),
		wrapSettlement: func(group dnfrepo.Group) dnfrepo.CharacterSettlementUnitOfWork {
			return &failingQuestSettlement{group: group, err: errors.New("injected active batch save failure")}
		},
	})
	before := harness.questFingerprint(t)

	_, response := harness.activeBatch(t, nil)
	message := requireFailure(t, http.StatusOK, response)
	if response["reason"] != "write_failed" || !strings.Contains(message, "回滚") {
		t.Fatalf("写失败必须如实回报回滚: %+v", response)
	}
	for questID, outcome := range activeBatchOutcomes(t, response) {
		if outcome[0] != "rejected" || outcome[1] != "write_failed" {
			t.Fatalf("#%v 的逐条结果 = %v, want rejected/write_failed", questID, outcome)
		}
	}
	if !harness.hasEvent("gm-quest-active-batch-failed") {
		t.Fatalf("写失败必须记具名事件，实际 %v", harness.eventKinds())
	}
	harness.requireZeroWrite(t, before, "写入失败的批量")
}

// ---------------------------------------------------------------------------
// 前端接线（静态检查，与 TestRenameFrontendIsWired 同一口径）
// ---------------------------------------------------------------------------

func TestQuestActiveBatchFrontendIsWired(t *testing.T) {
	rawIndex, err := embeddedWWW.ReadFile("wwwroot/index.html")
	if err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}
	index := string(rawIndex)
	for _, id := range []string{"questlog-active-actions", "btn-questlog-active-selected", "btn-questlog-active-all", "questlog-active-state"} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Fatalf("index.html 缺少 #%s（已接任务批量控件没接上）", id)
		}
	}

	rawScript, err := embeddedWWW.ReadFile("wwwroot/js/quests.js")
	if err != nil {
		t.Fatalf("embedded quests.js: %v", err)
	}
	script := string(rawScript)
	if !strings.Contains(script, "/quests/active/complete-batch") {
		t.Fatal("quests.js 未接 POST …/quests/active/complete-batch")
	}
	// 行内「强制完成」= 逐条完成走既有路由（不另造一条）。
	for _, marker := range []string{
		`data-quest-action="complete"`,
		"questlog-active-pick",
		"data.completionBatch",
		"skippedDaily",
		"notAccepted",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("quests.js 缺少 %q", marker)
		}
	}
	for _, name := range []string{"completeSelectedActiveQuests", "completeAllActiveQuests", "runQuestActiveBatch", "renderQuestBatchResult"} {
		if body := p4bFunctionBody(script, name); body == "" {
			t.Fatalf("quests.js 里找不到 %s", name)
		}
	}
	// 渲染条件只认服务端广告位：**不许**在前端探测环境变量/开关名。
	for _, forbidden := range []string{"DNFBRIDGE_GM_QUEST_MODE", "questModeEnabled", "QuestModeFromEnv"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("quests.js 出现了 %q：新控件的显示条件只能是 GET …/quests 的 completionBatch 广告位", forbidden)
		}
	}
	// 派生档不许在前端自己算「哪些算已接」：完成全部必须是**不传编号**的请求。
	allBody := p4bFunctionBody(script, "completeAllActiveQuests")
	if allBody == "" || !strings.Contains(allBody, "runQuestActiveBatch(path, {}") {
		t.Fatalf("completeAllActiveQuests 必须不传 questIds（派生由服务端做）: %s", allBody)
	}

	rawBindings, err := embeddedWWW.ReadFile("wwwroot/js/bindings.js")
	if err != nil {
		t.Fatalf("embedded bindings.js: %v", err)
	}
	bindings := string(rawBindings)
	for _, marker := range []string{
		"$('#btn-questlog-active-selected').onclick = completeSelectedActiveQuests;",
		"$('#btn-questlog-active-all').onclick = completeAllActiveQuests;",
	} {
		if !strings.Contains(bindings, marker) {
			t.Fatalf("bindings.js 缺少 %q", marker)
		}
	}

	// 脚本打的路径必须真的注册过（gmtool.go 的注册行是本包唯一真源）。
	if routes := p4bRegisteredRoutes(t); !routes["POST /api/characters/*/quests/active/complete-batch"] {
		t.Fatal("gmtool.go 里没有注册 POST /api/characters/{id}/quests/active/complete-batch")
	}
}
