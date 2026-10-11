package gmtool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// ---------------------------------------------------------------------------
// P5 任务写面测试台
//
// 证据强度对齐 P2 的写路由测试台：
//   - 每个"被拒绝"的用例都断言**库内容哈希不变**（quest + character + skill
//     三条记录的 JSON 的 sha256），而不只是看响应；
//   - 日志收集器用来断言具名审计事件（复用 P2 的 gmtoolWriteHarness）。
// ---------------------------------------------------------------------------

// questMutationFixtureTime 固定时间，避免哈希断言受 now 影响（写成功的用例才需要）。
var questMutationFixtureTime = time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)

// questMutationSource 是一份自足的假 PVF 任务目录：
//
//	3970 普通可接任务（[item] 奖励）
//	3971 普通任务（夹具里是 completed）
//	3972 普通任务（夹具里是 active，带一个 completion_key 回执）
//	4000 职业任务（[reward type] `[grow type]` + [job change quest] 1）
//	4001 主线子任务（[main quest] 3970 -> MainQuestID > 0）
//	4002 不可放弃（[cant giveup]）
//	4003 等级窗口 95..99（90 级角色不可接）
//
// 3999 刻意**不在**目录里，用来钉死"PVF 里没有的任务一律报错"。
func questMutationSource() readonlyTestSource {
	base := "[grade]\n`[epic]` \n[type]\n`[quest clear]` \n[level]\n1 99 \n[job]\n`[all]` \n"
	return readonlyTestSource{
		dnfquest.DefaultList: "3970 `q3970.qst`\n3971 `q3971.qst`\n3972 `q3972.qst`\n" +
			"4000 `q4000.qst`\n4001 `q4001.qst`\n4002 `q4002.qst`\n4003 `q4003.qst`\n",
		"n_quest/q3970.qst": base + "[name]\n`可接任务` \n[reward type]\n`[item]` \n[reward int data]\n1 2 \n",
		"n_quest/q3971.qst": base + "[name]\n`已完成任务` \n",
		"n_quest/q3972.qst": base + "[name]\n`进行中任务` \n",
		"n_quest/q4000.qst": base + "[name]\n`职业任务` \n[reward type]\n`[grow type]` \n" +
			"[reward int data]\n1 \n[job change quest]\n1 \n",
		"n_quest/q4001.qst": base + "[name]\n`主线子任务` \n[main quest]\n3970 \n",
		"n_quest/q4002.qst": base + "[name]\n`不可放弃任务` \n[cant giveup]\n1 \n",
		"n_quest/q4003.qst": "[grade]\n`[epic]` \n[type]\n`[quest clear]` \n[level]\n95 99 \n" +
			"[job]\n`[all]` \n[name]\n`高等级任务` \n",
	}
}

// questMutationFixtureQuests 是任务账夹具：
//   - 3971 completed 且**带四个结算回执键**，Progress 里还有一条同名行（验证双删）；
//   - 3972 active，带一个 completion_key（验证 ready 清键）；
//   - 4001 completed（主线子任务，验证 uncomplete 的链路门）；
//   - 4002 active（不可放弃，验证 give-up 的 owner 门）；
//   - 3999 active 但**不在 PVF**（验证"PVF 里没有就报错"）。
func questMutationFixtureQuests() *dnfrepo.QuestRecord {
	return &dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			3971: {
				Status: "completed", ProgressValue: 3, UpdatedAt: questMutationFixtureTime,
				Extra: map[string]string{
					"completion_key":        "key-3971",
					"completion_kind":       "clear map",
					"reward_state":          "pending",
					"settlement_receipt_v1": "receipt-3971",
				},
			},
			3972: {
				Status: "active", ProgressValue: 5, UpdatedAt: questMutationFixtureTime,
				Extra: map[string]string{"completion_key": "key-3972", "quest_type": "quest clear"},
			},
			4001: {
				Status: "completed", ProgressValue: 0, UpdatedAt: questMutationFixtureTime,
			},
			4002: {
				Status: "active", ProgressValue: 1, UpdatedAt: questMutationFixtureTime,
			},
			3999: {
				Status: "active", ProgressValue: 2, UpdatedAt: questMutationFixtureTime,
			},
		},
		Progress: map[int64]dnfrepo.QuestState{
			3971: {Status: "completed", UpdatedAt: questMutationFixtureTime},
		},
	}
}

type questWriteFixture struct {
	source    readonlyTestSource
	character dnfrepo.CharacterRecord
	quests    *dnfrepo.QuestRecord
	skills    *dnfrepo.SkillRecord
	// pvf 非空时覆盖 source：真实 Script.pvf 的用例（platformpvf.Archive 本身
	// 就实现 dnfpvf.Source）。
	pvf dnfpvf.Source
	// wrapSettlement 非空时替换仓储组的 CharacterSettlement：注入保存失败，
	// 用来证明"写入失败必须如实报失败"。
	wrapSettlement func(dnfrepo.Group) dnfrepo.CharacterSettlementUnitOfWork
}

type questWriteHarness struct {
	*gmtoolWriteHarness
}

func newQuestWriteHarness(t *testing.T, fixture questWriteFixture) *questWriteHarness {
	t.Helper()
	// 注意：readonlyTestSource 是 map，直接塞进接口会得到"非 nil 接口 + nil map"，
	// 必须在赋值前判空。
	var source dnfpvf.Source
	if fixture.source != nil {
		source = fixture.source
	}
	if fixture.pvf != nil {
		source = fixture.pvf
	}
	if source == nil {
		source = questMutationSource()
	}
	// 与 P1 测试台同口径：列表里引用不到的条目不会进目录，夹具也要先建一遍列表。
	if _, err := dnfpvf.Build(context.Background(), source,
		dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}}); err != nil {
		t.Fatalf("test quest PVF is not loadable: %v", err)
	}
	group := memory.NewMemoryGroup()
	character := fixture.character
	character.CharacterID = "1"
	if strings.TrimSpace(character.Job) == "" {
		character.Job = "11"
	}
	if character.Level == 0 {
		character.Level = 90
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	if err := group.Character.Save(context.Background(), character); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if fixture.quests != nil {
		record := *fixture.quests
		record.CharacterID = "1"
		if err := group.Quest.Save(context.Background(), record); err != nil {
			t.Fatalf("save quests: %v", err)
		}
	}
	if fixture.skills != nil {
		record := *fixture.skills
		record.CharacterID = "1"
		if err := group.Skill.Save(context.Background(), record); err != nil {
			t.Fatalf("save skills: %v", err)
		}
	}
	if fixture.wrapSettlement != nil {
		group.CharacterSettlement = fixture.wrapSettlement(group)
	}
	server := &Server{
		cfg: Config{Logger: func(string, ...any) {}},
		deps: Deps{
			Repos: func() (dnfrepo.Group, bool) { return group, true },
			PVF:   func() (dnfpvf.Source, error) { return source, nil },
		},
		pvf:      newPVFCache(),
		sessions: map[string]time.Time{},
	}
	server.items = newItemIndex("", server, server.cfg.Logger)
	base := &readonlyHarness{server: server, group: group, source: source}
	write := &gmtoolWriteHarness{readonlyHarness: base}
	server.cfg.Logger = write.record
	return &questWriteHarness{gmtoolWriteHarness: write}
}

// questFingerprint 是"库内容哈希"：任务账 + 角色 + 技能三条记录的规范 JSON 的 sha256。
// 拒绝路径必须让它逐字节不变。
func (h *questWriteHarness) questFingerprint(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	payload := map[string]any{}
	if record, found, err := h.group.Quest.Load(ctx, "1"); err != nil {
		t.Fatalf("load quest: %v", err)
	} else if found {
		payload["quest"] = record
	}
	if record, found, err := h.group.Character.Load(ctx, "1"); err != nil {
		t.Fatalf("load character: %v", err)
	} else if found {
		payload["character"] = record
	}
	if record, found, err := h.group.Skill.Load(ctx, "1"); err != nil {
		t.Fatalf("load skill: %v", err)
	} else if found {
		payload["skill"] = record
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fingerprint: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (h *questWriteHarness) requireZeroWrite(t *testing.T, before string, what string) {
	t.Helper()
	if after := h.questFingerprint(t); after != before {
		t.Fatalf("%s 应当零写入，但库内容哈希变了:\nbefore=%s\nafter =%s", what, before, after)
	}
}

func (h *questWriteHarness) questRecord(t *testing.T) dnfrepo.QuestRecord {
	t.Helper()
	record, found, err := h.group.Quest.Load(context.Background(), "1")
	if err != nil {
		t.Fatalf("load quest record: %v", err)
	}
	if !found {
		return dnfrepo.QuestRecord{CharacterID: "1"}
	}
	return record
}

func (h *questWriteHarness) questState(t *testing.T, questID int64) (dnfrepo.QuestState, bool) {
	t.Helper()
	record := h.questRecord(t)
	return questStateLookup(record, questID)
}

// mutate 打一个 POST /api/characters/1/quests/{questId}/{action}。
func (h *questWriteHarness) mutate(t *testing.T, action string, questID int64, handler http.HandlerFunc) (int, map[string]any) {
	t.Helper()
	path := "/api/characters/1/quests/" + strconv.FormatInt(questID, 10) + "/" + action
	return h.post(t, path, map[string]string{"id": "1", "questId": strconv.FormatInt(questID, 10)},
		map[string]any{}, handler)
}

func (h *questWriteHarness) accept(t *testing.T, questID int64) (int, map[string]any) {
	t.Helper()
	return h.mutate(t, "accept", questID, h.server.handleQuestAccept)
}

func (h *questWriteHarness) giveUp(t *testing.T, questID int64) (int, map[string]any) {
	t.Helper()
	return h.mutate(t, "give-up", questID, h.server.handleQuestGiveUp)
}

func (h *questWriteHarness) ready(t *testing.T, questID int64) (int, map[string]any) {
	t.Helper()
	return h.mutate(t, "ready", questID, h.server.handleQuestMarkReady)
}

func (h *questWriteHarness) complete(t *testing.T, questID int64) (int, map[string]any) {
	t.Helper()
	return h.mutate(t, "complete", questID, h.server.handleQuestForceComplete)
}

func (h *questWriteHarness) uncomplete(t *testing.T, questID int64) (int, map[string]any) {
	t.Helper()
	return h.mutate(t, "uncomplete", questID, h.server.handleQuestUncomplete)
}

func (h *questWriteHarness) batchComplete(t *testing.T, questIDs []int64) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/quests/complete-batch", map[string]string{"id": "1"},
		map[string]any{"questIds": questIDs}, h.server.handleQuestCompleteBatch)
}

// ---------------------------------------------------------------------------
// 开关语义
// ---------------------------------------------------------------------------

func TestQuestMutateFromEnvDefaultsToOn(t *testing.T) {
	for _, value := range []string{"", "  ", "on", "ON", " On ", "true", "1", "yes", "enable"} {
		if !QuestMutateFromEnv(value) {
			t.Fatalf("DNFBRIDGE_GM_QUEST_MUTATE=%q 必须是 on（空 = on）", value)
		}
	}
	for _, value := range []string{"off", "OFF", " off ", "0", "no", "false", "disable", "disabled", "onn"} {
		if QuestMutateFromEnv(value) {
			t.Fatalf("DNFBRIDGE_GM_QUEST_MUTATE=%q 必须解析成 off", value)
		}
	}
}

func TestQuestProfessionWriteFromEnvOnlyLiteralOn(t *testing.T) {
	if QuestProfessionWriteFromEnv("") {
		t.Fatal("空值必须是 off（职业任务写入默认关闭）")
	}
	for _, value := range []string{"on", "ON", " On "} {
		if !QuestProfessionWriteFromEnv(value) {
			t.Fatalf("%q 必须启用", value)
		}
	}
	// 安全开关只认显式 on，与 RequireOfflineFromEnv 同一约定。
	for _, value := range []string{"true", "1", "yes", "enable", "off", "0", "no"} {
		if QuestProfessionWriteFromEnv(value) {
			t.Fatalf("%q 不得启用职业任务写入（只认显式 on）", value)
		}
	}
}

func TestIsProfessionQuestRewardMatchesPVFLiterals(t *testing.T) {
	for _, value := range []string{"`[grow type]`", "[Awakening Type]", " grow type "} {
		if !IsProfessionQuestReward(value) {
			t.Fatalf("%q 必须判为职业任务", value)
		}
	}
	for _, value := range []string{"`[item]`", "`[title]`", "11", "growtype", ""} {
		if IsProfessionQuestReward(value) {
			t.Fatalf("%q 不得判为职业任务（不许靠 job id 猜）", value)
		}
	}
	// 读面（P1）与写面必须永远同一处判定。
	for _, value := range []string{"`[grow type]`", "`[awakening type]`", "`[item]`", ""} {
		if isProfessionRewardType(value) != IsProfessionQuestReward(value) {
			t.Fatalf("读面与写面的职业任务判定分叉: %q", value)
		}
	}
}

func TestQuestMutateSwitchOffDisablesAllRoutesWithoutWrites(t *testing.T) {
	t.Setenv(questMutateEnv, "off")
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	calls := map[string]func() (int, map[string]any){
		"accept":         func() (int, map[string]any) { return harness.accept(t, 3970) },
		"give-up":        func() (int, map[string]any) { return harness.giveUp(t, 3972) },
		"ready":          func() (int, map[string]any) { return harness.ready(t, 3972) },
		"complete":       func() (int, map[string]any) { return harness.complete(t, 3972) },
		"uncomplete":     func() (int, map[string]any) { return harness.uncomplete(t, 3971) },
		"complete-batch": func() (int, map[string]any) { return harness.batchComplete(t, []int64{3970}) },
	}
	for name, call := range calls {
		_, response := call()
		message := requireFailure(t, http.StatusOK, response)
		if !strings.Contains(message, questMutateEnv) {
			t.Fatalf("%s: 关闭文案必须点名开关，实际 %q", name, message)
		}
	}
	harness.requireZeroWrite(t, before, "总开关关闭时")
	if !harness.hasEvent("gm-quest-mutate-disabled") {
		t.Fatalf("总开关关闭必须记 gm-quest-mutate-disabled，实际事件 %v", harness.eventKinds())
	}
}

// ---------------------------------------------------------------------------
// J9-a 接取：复用本仓 owner
// ---------------------------------------------------------------------------

func TestQuestAcceptUsesForkOwnerSemantics(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	_, response := harness.accept(t, 3970)
	requireOK(t, http.StatusOK, response)

	if response["idempotent"] != false {
		t.Fatalf("首次接取不得是幂等回放: %+v", response)
	}
	if response["rewardGranted"] != false {
		t.Fatalf("GM 接取不得声称发过奖励: %+v", response)
	}
	quest := responseMap(t, response, "quest")
	if quest["active"] != true || quest["status"] != "active" {
		t.Fatalf("接取后任务必须是 active: %+v", quest)
	}
	state, found := harness.questState(t, 3970)
	if !found || state.Status != "active" {
		t.Fatalf("3970 落库状态 = %+v found=%v", state, found)
	}
	// owner 自己的标记必须在（证明走的是 dnfquest.Owner，而不是面板另写一套）。
	if state.Extra["pvf_path"] != "n_quest/q3970.qst" || state.Extra["quest_type"] == "" {
		t.Fatalf("owner 的 Extra 标记缺失: %+v", state.Extra)
	}
	// 面板**不得**给接取盖 GM 强制完成的章。
	for _, key := range []string{questForceCompletedKey, questBatchCompletedKey, questRewardsGrantedKey} {
		if _, exists := state.Extra[key]; exists {
			t.Fatalf("接取不得写 %s: %+v", key, state.Extra)
		}
	}
}

func TestQuestAcceptIsIdempotentAndWritesNothingOnReplay(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	status, firstResponse := harness.accept(t, 3972)
	requireOK(t, status, firstResponse)
	before := harness.questFingerprint(t)

	_, response := harness.accept(t, 3972)
	requireOK(t, http.StatusOK, response)
	if response["idempotent"] != true {
		t.Fatalf("已在进行中的任务必须走幂等回放: %+v", response)
	}
	harness.requireZeroWrite(t, before, "对已在进行中的任务重复接取")
}

func TestQuestAcceptRejectsUnknownAndIneligibleQuestsWithZeroWrites(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	// 3999 只在任务账里、不在 Script.pvf 里：必须报错并点名 PVF，不合成定义。
	_, response := harness.accept(t, 3999)
	message := requireFailure(t, http.StatusOK, response)
	if !strings.Contains(message, "Script.pvf") {
		t.Fatalf("不在 PVF 的任务必须点名 Script.pvf，实际 %q", message)
	}
	harness.requireZeroWrite(t, before, "接取不在 PVF 的任务")

	// 4003 的等级窗口是 95..99，90 级角色不可接。
	_, response = harness.accept(t, 4003)
	message = requireFailure(t, http.StatusOK, response)
	if !strings.Contains(message, "接取条件") {
		t.Fatalf("不满足条件的接取必须说明原因，实际 %q", message)
	}
	harness.requireZeroWrite(t, before, "接取不满足条件的任务")
	if harness.hasEvent("gm-quest-accept") {
		t.Fatalf("被拒绝的接取不得记成功事件: %v", harness.eventKinds())
	}
}

// ---------------------------------------------------------------------------
// J9-b 放弃：owner 的两道门
// ---------------------------------------------------------------------------

func TestQuestGiveUpRemovesActiveRow(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	_, response := harness.giveUp(t, 3972)
	requireOK(t, http.StatusOK, response)
	if response["removed"] != true {
		t.Fatalf("放弃必须如实回报删除: %+v", response)
	}
	record := harness.questRecord(t)
	if _, exists := record.States[3972]; exists {
		t.Fatalf("放弃后 States 里不该还有 3972: %+v", record.States)
	}
	if _, exists := record.Progress[3972]; exists {
		t.Fatalf("放弃后 Progress 里不该还有 3972: %+v", record.Progress)
	}
	// 别的任务一个字节都不许动。
	if _, exists := record.States[3971]; !exists {
		t.Fatalf("放弃 3972 不得影响 3971: %+v", record.States)
	}
}

func TestQuestGiveUpRejectsCantGiveUpAndInactiveWithZeroWrites(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	// 4002 带 [cant giveup]：owner 必须拒绝（计划书要求复用 owner 语义）。
	_, response := harness.giveUp(t, 4002)
	message := requireFailure(t, http.StatusOK, response)
	if !strings.Contains(message, "不可放弃") {
		t.Fatalf("不可放弃任务必须给出明确原因，实际 %q", message)
	}
	harness.requireZeroWrite(t, before, "放弃不可放弃的任务")

	// 3971 已完成，不是进行中：拒绝。
	_, response = harness.giveUp(t, 3971)
	requireFailure(t, http.StatusOK, response)
	harness.requireZeroWrite(t, before, "放弃非进行中的任务")
}

// ---------------------------------------------------------------------------
// J10-a ready
// ---------------------------------------------------------------------------

func TestQuestMarkReadyClearsReceiptsAndIsIdempotent(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	_, response := harness.ready(t, 3972)
	requireOK(t, http.StatusOK, response)
	if response["markedReady"] != true || response["changed"] != true {
		t.Fatalf("ready 必须回报 markedReady/changed: %+v", response)
	}
	cleared := responseList(t, response, "clearedKeys")
	if len(cleared) != 1 || cleared[0] != "completion_key" {
		t.Fatalf("clearedKeys = %v, want [completion_key]", cleared)
	}
	state, found := harness.questState(t, 3972)
	if !found || state.ProgressValue != 0 {
		t.Fatalf("ready 必须把进度归零: %+v found=%v", state, found)
	}
	if state.Extra[questMarkedReadyKey] != "true" {
		t.Fatalf("ready 必须打 gm_marked_ready: %+v", state.Extra)
	}
	for _, key := range questCompletionExtraKeys {
		if _, exists := state.Extra[key]; exists {
			t.Fatalf("ready 必须清掉 %s: %+v", key, state.Extra)
		}
	}

	// 重复点击 = 零写入。
	before := harness.questFingerprint(t)
	_, response = harness.ready(t, 3972)
	requireOK(t, http.StatusOK, response)
	if response["unchanged"] != true || response["changed"] != false {
		t.Fatalf("重复 ready 必须是 unchanged: %+v", response)
	}
	harness.requireZeroWrite(t, before, "重复标记可交付")
}

func TestQuestMarkReadyRequiresActiveQuest(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	// 3971 已完成；3970 在账里根本没有。
	for _, questID := range []int64{3971, 3970} {
		_, response := harness.ready(t, questID)
		message := requireFailure(t, http.StatusOK, response)
		if !strings.Contains(message, "进行中") {
			t.Fatalf("ready %d: 文案 = %q, want 进行中", questID, message)
		}
	}
	harness.requireZeroWrite(t, before, "对非进行中任务标记可交付")

	// 任务是"行都不存在"时，必须在事务内也明确失败（不许假成功）。
	if _, response := harness.ready(t, 3999); response["success"] != false {
		t.Fatalf("PVF 里没有的任务行也必须明确失败: %+v", response)
	}
}

// ---------------------------------------------------------------------------
// J10-b 强制完成
// ---------------------------------------------------------------------------

func TestQuestForceCompleteMarksRewardsNotGranted(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	_, response := harness.complete(t, 3972)
	requireOK(t, http.StatusOK, response)
	if response["rewardGranted"] != false {
		t.Fatalf("GM 强制完成从不发奖励: %+v", response)
	}
	if response["alreadyCompleted"] != false || response["changed"] != true {
		t.Fatalf("首次完成必须 changed: %+v", response)
	}
	state, found := harness.questState(t, 3972)
	if !found || state.Status != questCompletedLiteral || state.ProgressValue != 0 {
		t.Fatalf("完成后状态 = %+v found=%v", state, found)
	}
	if state.Extra[questRewardsGrantedKey] != "false" {
		t.Fatalf("%s 必须是 false（上游 gm.go:1305）: %+v", questRewardsGrantedKey, state.Extra)
	}
	if state.Extra[questForceCompletedKey] != "true" {
		t.Fatalf("%s 必须是 true: %+v", questForceCompletedKey, state.Extra)
	}
	if state.Extra[questPVFPathKey] != "n_quest/q3972.qst" {
		t.Fatalf("%s 必须写 PVF 路径: %+v", questPVFPathKey, state.Extra)
	}
	for _, key := range questCompletionExtraKeys {
		if _, exists := state.Extra[key]; exists {
			t.Fatalf("完成必须清掉 %s（上游 gm.go:1306-1308）: %+v", key, state.Extra)
		}
	}
	// Progress 里的同名行必须删掉（上游 gm.go:1310）。
	record := harness.questRecord(t)
	if _, exists := record.Progress[3972]; exists {
		t.Fatalf("完成后 Progress 里不该还有 3972: %+v", record.Progress)
	}
	if _, exists := record.States[3972]; !exists {
		t.Fatalf("完成后 States 里必须有 3972: %+v", record.States)
	}
}

func TestQuestForceCompleteIsIdempotentOnCompletedQuest(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	_, response := harness.complete(t, 3971)
	requireOK(t, http.StatusOK, response)
	if response["alreadyCompleted"] != true || response["changed"] != false {
		t.Fatalf("已完成的任务必须幂等无操作: %+v", response)
	}
	// 上游 gm.go:1236-1238 在完成态直接 return：四个结算回执**原样保留**。
	harness.requireZeroWrite(t, before, "对已完成任务再次强制完成")
	state, _ := harness.questState(t, 3971)
	if state.Extra["settlement_receipt_v1"] != "receipt-3971" {
		t.Fatalf("幂等回放不得破坏既有结算回执: %+v", state.Extra)
	}
}

func TestQuestForceCompleteRejectsProfessionQuestByDefault(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	_, response := harness.complete(t, 4000)
	message := requireFailure(t, http.StatusOK, response)
	if response["reason"] != "profession_write_disabled" {
		t.Fatalf("职业任务默认必须走 profession_write_disabled: %+v", response)
	}
	if !strings.Contains(message, questProfessionWriteEnv) {
		t.Fatalf("拒绝文案必须点名开关 %s: %q", questProfessionWriteEnv, message)
	}
	harness.requireZeroWrite(t, before, "默认档完成职业任务")
	if _, exists := harness.questRecord(t).States[4000]; exists {
		t.Fatal("被拒绝的职业任务不得留下任何任务行")
	}
}

func TestQuestForceCompleteProfessionSwitchOpensGateButStillNeedsPVF(t *testing.T) {
	t.Setenv(questProfessionWriteEnv, "on")
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	_, response := harness.complete(t, 4000)
	message := requireFailure(t, http.StatusOK, response)
	// 开关打开后不该再撞"默认关闭"那扇门；下一步的真实失败是这份假 PVF 没有
	// 职业/技能表（证明门开了、而且拿不到 PVF 数据时明确失败、零写入）。
	if response["reason"] == "profession_write_disabled" {
		t.Fatalf("开关 on 后不得再报 profession_write_disabled: %+v", response)
	}
	if !strings.Contains(message, "PVF") {
		t.Fatalf("缺 PVF 职业资源时必须明确说明，实际 %q", message)
	}
	harness.requireZeroWrite(t, before, "开关 on 但 PVF 职业资源不可用")
}

// ---------------------------------------------------------------------------
// J10-c 反完成
// ---------------------------------------------------------------------------

func TestQuestUncompleteRemovesFromBothMaps(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	_, response := harness.uncomplete(t, 3971)
	requireOK(t, http.StatusOK, response)
	if response["removedStates"] != true || response["removedProgress"] != true {
		t.Fatalf("3971 在两张表里都有行，必须都删: %+v", response)
	}
	record := harness.questRecord(t)
	if _, exists := record.States[3971]; exists {
		t.Fatalf("States 里不该还有 3971: %+v", record.States)
	}
	if _, exists := record.Progress[3971]; exists {
		t.Fatalf("Progress 里不该还有 3971: %+v", record.Progress)
	}
	// 反完成后再看该任务：不再是 completed。
	if state, found := harness.questState(t, 3971); found && isCompletedQuestStatus(state.Status) {
		t.Fatalf("反完成后仍是完成态: %+v", state)
	}
}

func TestQuestUncompleteRejectsWithZeroWrites(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	cases := []struct {
		questID int64
		want    string
		label   string
	}{
		{3972, "不是已完成状态", "反完成进行中的任务"},
		{3970, "不在该角色的任务账里", "反完成任务账里没有的行"},
		{4001, "主线链路", "反完成主线链路任务"},
		{3999, "Script.pvf", "反完成不在 PVF 的任务"},
	}
	for _, testCase := range cases {
		_, response := harness.uncomplete(t, testCase.questID)
		message := requireFailure(t, http.StatusOK, response)
		if !strings.Contains(message, testCase.want) {
			t.Fatalf("%s: 文案 = %q, want 含 %q", testCase.label, message, testCase.want)
		}
		harness.requireZeroWrite(t, before, testCase.label)
	}
}

// ---------------------------------------------------------------------------
// J11 批量完成
// ---------------------------------------------------------------------------

func TestQuestBatchCompleteReportsPerItemOutcome(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	_, response := harness.batchComplete(t, []int64{3970, 3971, 3972, 3972})
	requireOK(t, http.StatusOK, response)

	if response["requested"] != float64(4) || response["unique"] != float64(3) {
		t.Fatalf("重复 ID 必须静默折叠: %+v", response)
	}
	if response["changed"] != float64(2) || response["unchanged"] != float64(1) {
		t.Fatalf("changed/unchanged = %v/%v, want 2/1: %+v", response["changed"], response["unchanged"], response)
	}
	if response["rewardGranted"] != false {
		t.Fatalf("批量完成从不发奖励: %+v", response)
	}
	results := responseList(t, response, "results")
	if len(results) != 3 {
		t.Fatalf("必须逐条回报（4 个请求、3 个唯一）: %+v", results)
	}
	outcomes := map[float64]string{}
	for _, raw := range results {
		row := raw.(map[string]any)
		outcomes[row["questId"].(float64)] = row["outcome"].(string)
	}
	if outcomes[3970] != "completed" || outcomes[3972] != "completed" || outcomes[3971] != "already_completed" {
		t.Fatalf("逐条 outcome = %+v", outcomes)
	}
	// 3971 是幂等跳过：它原来的结算回执一个字节都不许动。
	state, _ := harness.questState(t, 3971)
	if state.Extra["settlement_receipt_v1"] != "receipt-3971" {
		t.Fatalf("已完成的条目必须原样保留: %+v", state.Extra)
	}
	// 新完成的两条要有批量标记。
	for _, questID := range []int64{3970, 3972} {
		state, found := harness.questState(t, questID)
		if !found || state.Status != questCompletedLiteral {
			t.Fatalf("%d 完成后状态 = %+v found=%v", questID, state, found)
		}
		if state.Extra[questBatchCompletedKey] != "true" || state.Extra[questRewardsGrantedKey] != "false" {
			t.Fatalf("%d 缺少批量完成标记: %+v", questID, state.Extra)
		}
	}
}

func TestQuestBatchCompleteIsIdempotentWhenEverythingIsDone(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	_, response := harness.batchComplete(t, []int64{3971})
	requireOK(t, http.StatusOK, response)
	if response["changed"] != float64(0) || response["unchanged"] != float64(1) {
		t.Fatalf("全部已完成时必须如实回报 changed=0: %+v", response)
	}
	harness.requireZeroWrite(t, before, "整批都已完成")
}

func TestQuestBatchCompleteRejectsWholeBatchWithZeroWrites(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	before := harness.questFingerprint(t)

	// 1) 空 / 超上限（上游 gm_extended.go:834-836）。
	_, response := harness.batchComplete(t, nil)
	if response["reason"] != "batch_size" {
		t.Fatalf("空列表必须拒绝: %+v", response)
	}
	tooMany := make([]int64, 0, questBatchMaxIDs+1)
	for index := 0; index <= questBatchMaxIDs; index++ {
		tooMany = append(tooMany, int64(6000+index))
	}
	_, response = harness.batchComplete(t, tooMany)
	message := requireFailure(t, http.StatusOK, response)
	if response["reason"] != "batch_size" || !strings.Contains(message, "200") {
		t.Fatalf("201 个 ID 必须拒绝并说明上限: %+v", response)
	}
	harness.requireZeroWrite(t, before, "批量上限校验")

	// 2) 含职业任务 -> 整批拒绝（上游 gm_extended.go:857-859），逐条列出原因。
	_, response = harness.batchComplete(t, []int64{3970, 4000})
	message = requireFailure(t, http.StatusOK, response)
	if response["reason"] != "precheck_failed" || !strings.Contains(message, "职业任务") {
		t.Fatalf("含职业任务必须整批拒绝: %+v", response)
	}
	problems := responseList(t, response, "problems")
	if len(problems) != 1 || problems[0].(map[string]any)["reason"] != "profession_excluded" ||
		problems[0].(map[string]any)["questId"] != float64(4000) {
		t.Fatalf("problems = %+v", problems)
	}
	harness.requireZeroWrite(t, before, "含职业任务的批量")
	if state, found := harness.questState(t, 3970); found {
		t.Fatalf("整批拒绝时 3970 不得被写成完成: %+v", state)
	}

	// 3) 含不在 PVF 的任务 -> 整批拒绝并点名 Script.pvf。
	_, response = harness.batchComplete(t, []int64{3970, 3999})
	message = requireFailure(t, http.StatusOK, response)
	if !strings.Contains(message, "Script.pvf") {
		t.Fatalf("不在 PVF 的任务必须点名 Script.pvf: %q", message)
	}
	harness.requireZeroWrite(t, before, "含不在 PVF 任务的批量")
}

func TestQuestBatchCompleteReportsWriteFailureAndWritesNothing(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{
		quests: questMutationFixtureQuests(),
		wrapSettlement: func(group dnfrepo.Group) dnfrepo.CharacterSettlementUnitOfWork {
			return &failingQuestSettlement{group: group, err: errors.New("injected quest save failure")}
		},
	})
	before := harness.questFingerprint(t)

	_, response := harness.batchComplete(t, []int64{3970, 3972})
	message := requireFailure(t, http.StatusOK, response)
	if response["reason"] != "write_failed" || !strings.Contains(message, "整批回滚") {
		t.Fatalf("写入失败必须如实报失败（不许静默假成功）: %+v", response)
	}
	harness.requireZeroWrite(t, before, "批量写入失败")
	if !harness.hasEvent("gm-quest-batch-failed") {
		t.Fatalf("写入失败必须记 gm-quest-batch-failed: %v", harness.eventKinds())
	}
}

// ---------------------------------------------------------------------------
// 在线守卫（J0 复用）：只作用于这 6 条新路由，默认档不探针
// ---------------------------------------------------------------------------

func TestQuestMutationsDoNotProbeOnlineByDefault(t *testing.T) {
	probeCalls := 0
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	harness.server.cfg.RequireOffline = false // DNFBRIDGE_GM_REQUIRE_OFFLINE 未设置
	harness.server.deps.Online = func(uint16) bool { probeCalls++; return true }

	acceptStatus, acceptResponse := harness.accept(t, 3970)
	requireOK(t, acceptStatus, acceptResponse)
	readyStatus, readyResponse := harness.ready(t, 3972)
	requireOK(t, readyStatus, readyResponse)
	if probeCalls != 0 {
		t.Fatalf("默认档连在线探针都不该碰，实际调用 %d 次", probeCalls)
	}
	if kinds := harness.kindsWithPrefix("gm-online-guard"); len(kinds) != 0 {
		t.Fatalf("默认档不得产生守卫日志: %v", kinds)
	}
}

func TestQuestMutationsRejectOnlineCharactersWhenGuardIsOn(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(uint16) bool { return true }
	before := harness.questFingerprint(t)

	calls := map[string]func() (int, map[string]any){
		"accept":         func() (int, map[string]any) { return harness.accept(t, 3970) },
		"give-up":        func() (int, map[string]any) { return harness.giveUp(t, 3972) },
		"ready":          func() (int, map[string]any) { return harness.ready(t, 3972) },
		"complete":       func() (int, map[string]any) { return harness.complete(t, 3972) },
		"uncomplete":     func() (int, map[string]any) { return harness.uncomplete(t, 3971) },
		"complete-batch": func() (int, map[string]any) { return harness.batchComplete(t, []int64{3970}) },
	}
	for name, call := range calls {
		status, response := call()
		message := requireFailure(t, status, response)
		if status != http.StatusOK {
			t.Fatalf("%s: 守卫拒绝必须是 HTTP 200（计划书 §5.2 C10）", name)
		}
		if !strings.Contains(message, "在线") {
			t.Fatalf("%s: 文案 = %q, want 在线", name, message)
		}
		harness.requireZeroWrite(t, before, name+" 在线守卫拒绝")
	}
	if !harness.hasEvent("gm-online-guard-rejected") {
		t.Fatalf("必须记 gm-online-guard-rejected: %v", harness.eventKinds())
	}
}

// ---------------------------------------------------------------------------
// 路由面：鉴权 + 不遮住既有路由
// ---------------------------------------------------------------------------

func questMutationProbes() []p3RouteProbe {
	return []p3RouteProbe{
		{"接取任务", http.MethodPost, "/api/characters/1/quests/3970/accept", map[string]any{}},
		{"放弃任务", http.MethodPost, "/api/characters/1/quests/3972/give-up", map[string]any{}},
		{"标记可交付", http.MethodPost, "/api/characters/1/quests/3972/ready", map[string]any{}},
		{"强制完成", http.MethodPost, "/api/characters/1/quests/3972/complete", map[string]any{}},
		{"反完成", http.MethodPost, "/api/characters/1/quests/3971/uncomplete", map[string]any{}},
		{"批量完成", http.MethodPost, "/api/characters/1/quests/complete-batch", map[string]any{"questIds": []int64{3970}}},
	}
}

func TestQuestMutationAddedRoutesRequireSession(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	handler := harness.server.routes() // 注册冲突会在这里 panic
	for _, probe := range questMutationProbes() {
		recorder := httpDoJSON(t, handler, "", probe.method, probe.target, probe.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401（body=%s）", probe.name, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "\"loginRequired\":true") {
			t.Fatalf("%s: body = %s, want loginRequired:true", probe.name, recorder.Body.String())
		}
	}
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	for _, probe := range questMutationProbes() {
		recorder := httpDoJSON(t, handler, token, probe.method, probe.target, probe.body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: 带会话 status = %d（body=%s）", probe.name, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s: 落到了 /api/ 兜底: %s", probe.name, recorder.Body.String())
		}
	}
}

func TestQuestMutationRoutesDoNotShadowExistingQuestRoutes(t *testing.T) {
	harness := newQuestWriteHarness(t, questWriteFixture{quests: questMutationFixtureQuests()})
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	probes := []p3RouteProbe{
		{"既有任务总览", http.MethodGet, "/api/characters/1/quests", nil},
		{"既有任务搜索", http.MethodGet, "/api/characters/1/quests/search?q=3970", nil},
		{"既有任务详情", http.MethodGet, "/api/characters/1/quests/3971/detail", nil},
	}
	for _, probe := range probes {
		recorder := httpDoJSON(t, handler, token, probe.method, probe.target, probe.body)
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s: %s -> 404，既有路由被新路由遮住了", probe.name, probe.target)
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s: %s 落到了兜底: %s", probe.name, probe.target, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "\"success\":true") {
			t.Fatalf("%s: %s 读面被改动: %s", probe.name, probe.target, recorder.Body.String())
		}
	}
	// 既有"任务写"入口（P4 未做）仍必须落兜底：证明本批没有顺手接管别的路由。
	recorder := httpDoJSON(t, handler, token, http.MethodPost, "/api/characters/1/items/delete",
		map[string]any{"container": "inventory", "key": "0:1"})
	if !strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
		t.Fatalf("未移植路由的兜底行为被改变: %s", recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 前端资产
// ---------------------------------------------------------------------------

func TestQuestMutationFrontendAssetsAreEmbedded(t *testing.T) {
	raw, err := embeddedWWW.ReadFile("wwwroot/index.html")
	if err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}
	index := string(raw)
	for _, id := range []string{"questlog-batch-ids", "btn-questlog-batch", "questlog-batch-result"} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Fatalf("index.html 缺少 #%s", id)
		}
	}
	raw, err = embeddedWWW.ReadFile("wwwroot/js/quests.js")
	if err != nil {
		t.Fatalf("embedded quests.js: %v", err)
	}
	quests := string(raw)
	for _, marker := range []string{"runQuestMutation", "completeBatchQuests", "questMutationPath", "questMutationButtons"} {
		if !strings.Contains(quests, marker) {
			t.Fatalf("quests.js 缺少 %s", marker)
		}
	}
	// 六条写路由：五条逐条操作走同一个模板，批量完成是独立路径。
	if !strings.Contains(quests, "/quests/${questId}/${action}") {
		t.Fatal("quests.js 未接逐条任务写路由模板")
	}
	for _, action := range []string{"accept", "give-up", "ready", "complete", "uncomplete"} {
		if !strings.Contains(quests, "'"+action+"':") {
			t.Fatalf("quests.js 未声明 %s 动作", action)
		}
	}
	if !strings.Contains(quests, "/quests/complete-batch") {
		t.Fatal("quests.js 未接 /quests/complete-batch")
	}
	raw, err = embeddedWWW.ReadFile("wwwroot/js/bindings.js")
	if err != nil {
		t.Fatalf("embedded bindings.js: %v", err)
	}
	if !strings.Contains(string(raw), "btn-questlog-batch") {
		t.Fatal("bindings.js 未绑定批量完成按钮")
	}
}

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// failingQuestSettlement 是注入"任务保存失败"的事务替身：回调照跑，但任务保存
// 一定失败。用来证明写入失败会如实报失败、且库里没有任何变更（绝不静默假成功）。
type failingQuestSettlement struct {
	group dnfrepo.Group
	err   error
}

func (u *failingQuestSettlement) WithinCharacterSettlement(
	_ context.Context,
	_ string,
	apply func(dnfrepo.Group) error,
) error {
	if apply == nil {
		return errors.New("nil apply")
	}
	tx := dnfrepo.Group{
		Character: u.group.Character,
		Quest:     &failingQuestSaveRepo{inner: u.group.Quest, err: u.err},
		Skill:     u.group.Skill,
	}
	return apply(tx)
}

type failingQuestSaveRepo struct {
	inner dnfrepo.QuestRepository
	err   error
}

func (r *failingQuestSaveRepo) Load(ctx context.Context, key string) (dnfrepo.QuestRecord, bool, error) {
	return r.inner.Load(ctx, key)
}

func (r *failingQuestSaveRepo) Save(context.Context, dnfrepo.QuestRecord) error { return r.err }
