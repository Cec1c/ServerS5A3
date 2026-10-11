package gmtool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// ---------------------------------------------------------------------------
// 计划层：位掩码
// ---------------------------------------------------------------------------

// 当前客户端（NoPack）的三个特殊装备槽位**不连续**：0x01 / 0x02 / 0x10。
// 这正是旧实现写 7 会漏开耳环槽的原因，所以在这里把它钉死。
func TestExtraEquipmentSlotMasksMatchCurrentClient(t *testing.T) {
	if ExtraEquipmentSlotsSupport != 0x01 || ExtraEquipmentSlotsMagicStone != 0x02 || ExtraEquipmentSlotsEarring != 0x10 {
		t.Fatalf("slot bits = %#x/%#x/%#x, want 0x01/0x02/0x10",
			ExtraEquipmentSlotsSupport, ExtraEquipmentSlotsMagicStone, ExtraEquipmentSlotsEarring)
	}
	if ExtraEquipmentSlotsAll != 0x13 {
		t.Fatalf("all-slot mask = %#x, want 0x13", ExtraEquipmentSlotsAll)
	}
	// 与 modules/dnf/quest 的权威常量必须同源；quest 包那份由 catalog_test.go 钉死。
	if byte(dnfquest.ExEquipSlotSupport) != ExtraEquipmentSlotsSupport ||
		byte(dnfquest.ExEquipSlotMagicStone) != ExtraEquipmentSlotsMagicStone ||
		byte(dnfquest.ExEquipSlotEarring) != ExtraEquipmentSlotsEarring ||
		byte(dnfquest.ExEquipSlotAll) != ExtraEquipmentSlotsAll {
		t.Fatalf("gmtool bits diverge from dnfquest: %#x/%#x/%#x/%#x vs %#x/%#x/%#x/%#x",
			ExtraEquipmentSlotsSupport, ExtraEquipmentSlotsMagicStone, ExtraEquipmentSlotsEarring, ExtraEquipmentSlotsAll,
			dnfquest.ExEquipSlotSupport, dnfquest.ExEquipSlotMagicStone, dnfquest.ExEquipSlotEarring, dnfquest.ExEquipSlotAll)
	}
	// 旧口径的 7 必须被识别为「左右槽开了、耳环没开」。
	state := extraEquipmentStatePayload(7)
	if state["leftRight"] != true || state["earring"] != false || state["all"] != false {
		t.Fatalf("mask 7 state = %+v", state)
	}
	if missing, _ := state["missing"].([]string); !reflect.DeepEqual(missing, []string{"earring"}) {
		t.Fatalf("mask 7 missing = %v", missing)
	}
	all := extraEquipmentStatePayload(ExtraEquipmentSlotsAll)
	if all["all"] != true || all["earring"] != true || len(all["missing"].([]string)) != 0 {
		t.Fatalf("mask 0x13 state = %+v", all)
	}
}

func TestPlanExtraEquipmentSlotUnlockIsBitwiseOr(t *testing.T) {
	// 从「旧实现留下的 7」出发、请求整组（all）：已经开着的左右槽只报告 alreadyOn，
	// 只有耳环进 granted —— 这正是线上 6 个 Lv90 角色要走的路径。
	plan := planExtraEquipmentSlotUnlock(7, extraEquipmentSlotMetas)
	if plan.GrantedBit != ExtraEquipmentSlotsEarring || plan.ResultMask != 0x17 || plan.NoWrite {
		t.Fatalf("upgrade-from-7 plan = %+v", plan)
	}
	if !reflect.DeepEqual(plan.Granted, []string{"earring"}) {
		t.Fatalf("granted = %v, want [earring]", plan.Granted)
	}
	if !reflect.DeepEqual(plan.AlreadyOn, []string{"support", "magicStone"}) {
		t.Fatalf("alreadyOn = %v, want [support magicStone]", plan.AlreadyOn)
	}
	// 只请求耳环、起点 7：granted 仍是 [earring]，且不会把左右槽误报成刚开启。
	plan = planExtraEquipmentSlotUnlock(7, extraEquipmentSlotMetas[2:3])
	if !reflect.DeepEqual(plan.Granted, []string{"earring"}) || plan.ResultMask != 0x17 {
		t.Fatalf("earring-only plan = %+v", plan)
	}
	if state := extraEquipmentStatePayload(plan.ResultMask); state["all"] != true || state["earring"] != true {
		t.Fatalf("state after earring unlock = %+v", state)
	}
	// 干净起点（0）只开耳环：结果就是 0x10，一位都不多。
	plan = planExtraEquipmentSlotUnlock(0, extraEquipmentSlotMetas[2:3])
	if plan.ResultMask != ExtraEquipmentSlotsEarring || plan.GrantedBit != ExtraEquipmentSlotsEarring {
		t.Fatalf("clean earring plan = %+v", plan)
	}
	// 全部开满（当前客户端口径 0x13）后再点：不写库。
	plan = planExtraEquipmentSlotUnlock(0x13, extraEquipmentSlotMetas)
	if !plan.NoWrite || plan.GrantedBit != 0 || plan.ResultMask != 0x13 {
		t.Fatalf("idempotent plan = %+v", plan)
	}
	if !reflect.DeepEqual(plan.AlreadyOn, []string{"support", "magicStone", "earring"}) {
		t.Fatalf("alreadyOn = %v", plan.AlreadyOn)
	}
	// 左右槽默认分组（A21 旧前端不带 body 的兼容路径）：只写 0x03，不碰耳环位。
	plan = planExtraEquipmentSlotUnlock(0, extraEquipmentSlotMetas[:2])
	if plan.GrantedBit != 0x03 || plan.ResultMask != 0x03 {
		t.Fatalf("left/right plan = %+v", plan)
	}
}

func TestExtraEquipmentSlotMetaForRequestDefaultsAndRejects(t *testing.T) {
	for _, value := range []string{"", "  ", "leftright", "left-right", "Left_Right"} {
		metas, err := extraEquipmentSlotMetaForRequest(value)
		if err != nil || len(metas) != 2 || metas[0].ID != "support" || metas[1].ID != "magicStone" {
			t.Fatalf("%q -> metas=%v err=%v", value, metas, err)
		}
	}
	for value, want := range map[string]string{
		"earring": "earring", "earRing": "earring", "magicstone": "magicStone", "support": "support",
	} {
		metas, err := extraEquipmentSlotMetaForRequest(value)
		if err != nil || len(metas) != 1 || metas[0].ID != want {
			t.Fatalf("%q -> metas=%v err=%v", value, metas, err)
		}
	}
	metas, err := extraEquipmentSlotMetaForRequest("all")
	if err != nil || len(metas) != 3 {
		t.Fatalf("all -> metas=%v err=%v", metas, err)
	}
	if _, err := extraEquipmentSlotMetaForRequest("wing"); err == nil {
		t.Fatal("unknown slot group must be rejected")
	}
}

// ---------------------------------------------------------------------------
// 处理器层：PVF -> 角色位 + 任务行
// ---------------------------------------------------------------------------

type slotTestSource map[string]string

func (s slotTestSource) ReadText(relativePath string) (string, error) {
	text, ok := s[relativePath]
	if !ok {
		return "", &slotTestMissingPath{path: relativePath}
	}
	return text, nil
}

type slotTestMissingPath struct{ path string }

func (e *slotTestMissingPath) Error() string { return "missing test PVF path: " + e.path }

func slotExpansionQuest(grade string, index int) string {
	return slotExpansionQuestWithPrerequisite(grade, index, 0)
}

// slotExpansionQuestWithPrerequisite 与 slotExpansionQuest 同形，只是补上真档里那条
// `[pre required quest]`：真档 649 -> 674、650 -> 675、2636 -> 2635。
func slotExpansionQuestWithPrerequisite(grade string, index int, prerequisite int64) string {
	prerequisiteSection := ""
	if prerequisite > 0 {
		prerequisiteSection = "[pre required quest]\n" + strconv.FormatInt(prerequisite, 10) + "\n[/pre required quest]\n\n"
	}
	return "[grade]\n`" + grade + "`\n" + prerequisiteSection +
		"[type]\n`[seeking]`\n" +
		"[level]\n1 99\n[job]\n`[all]`\n" +
		"[reward type]\n`[slot expansion]`\n[reward int data]\n" + string(rune('0'+index)) + "\n"
}

// slotChainPrerequisiteQuest 是槽位授权链的「资格 (1/2)」：真档里是
// 674 `穿戴辅助装备的资格 (1/2)` / 675 `佩戴魔法石的资格 (1/2)` /
// 2635 `穿戴耳环 (1/2)` —— `[grade] [side]` + `[type] [hunt monster]` + 无奖励。
func slotChainPrerequisiteQuest() string {
	return "[grade]\n`[side]`\n[type]\n`[hunt monster]`\n[int data]\n-1 -1 9602 1\n" +
		"[reward type]\n`[item]`\n[reward int data]\n0 0\n"
}

// slotTestPVF 与真档 Script.pvf 的同一批节名同构：三个 [slot expansion] 任务
// （索引 0/1/2）各带一条 `[pre required quest]` 前置（649->674、650->675、
// 2636->2635），外加两个普通任务用于验证「无关任务一行都不许动」。
func slotTestPVF() slotTestSource {
	return slotTestSource{
		dnfquest.DefaultList: "649 `support.qst`\n" +
			"650 `magic.qst`\n" +
			"2636 `earring.qst`\n" +
			"3145 `unrelated.qst`\n" +
			"674 `support_side.qst`\n" +
			"675 `magic_side.qst`\n" +
			"2635 `earring_side.qst`\n",
		"n_quest/support.qst":      slotExpansionQuestWithPrerequisite("[common unique]", 0, 674),
		"n_quest/magic.qst":        slotExpansionQuestWithPrerequisite("[common unique]", 1, 675),
		"n_quest/earring.qst":      slotExpansionQuestWithPrerequisite("[side]", 2, 2635),
		"n_quest/unrelated.qst":    "[grade]\n`[epic]`\n[type]\n`[seeking]`\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/support_side.qst": slotChainPrerequisiteQuest(),
		"n_quest/magic_side.qst":   slotChainPrerequisiteQuest(),
		"n_quest/earring_side.qst": slotChainPrerequisiteQuest(),
	}
}

type slotTestHarness struct {
	server *Server
	group  dnfrepo.Group
}

// newSlotTestHarness 建的是**开启档**（EquipSlotQuestSync=true）的测试台：既有的
// 一批用例断言的正是"位 + 任务行一起落地"。回退档（只写位）的用例见
// TestHandleUnlockEquipmentSlotsLegacyRollbackWritesBitOnly 等（用
// newSlotTestHarnessMode）。
//
// 两个建台函数都把链路开关钉成**默认档**（空 = 修复后的行为），让用例不受宿主
// 环境的 DNFBRIDGE_GM_EQUIP_SLOT_QUEST_CHAIN 影响；要回退档的用例在**建台之后**
// 再 t.Setenv(equipSlotQuestChainEnv, legacy) —— 开关是请求时自读环境的。
func newSlotTestHarness(t *testing.T, level int, stats map[string]int64, quests dnfrepo.QuestRecord) *slotTestHarness {
	t.Helper()
	return newSlotTestHarnessMode(t, level, stats, quests, true)
}

func newSlotTestHarnessMode(t *testing.T, level int, stats map[string]int64, quests dnfrepo.QuestRecord, questSync bool) *slotTestHarness {
	t.Helper()
	t.Setenv(equipSlotQuestChainEnv, "")
	source := slotTestPVF()
	if _, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}}); err != nil {
		t.Fatalf("test PVF is not loadable: %v", err)
	}
	group := memory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "1",
		AccountID:   "acc-1",
		Name:        "tester",
		Job:         "11",
		Level:       level,
		Stats:       stats,
	}
	if err := group.Character.Save(context.Background(), character); err != nil {
		t.Fatalf("save character: %v", err)
	}
	quests.CharacterID = "1"
	if err := group.Quest.Save(context.Background(), quests); err != nil {
		t.Fatalf("save quests: %v", err)
	}
	server := &Server{
		cfg: Config{Logger: func(string, ...any) {}, EquipSlotQuestSync: questSync},
		deps: Deps{
			Repos: func() (dnfrepo.Group, bool) { return group, true },
			PVF:   func() (dnfpvf.Source, error) { return source, nil },
		},
		pvf: newPVFCache(),
	}
	return &slotTestHarness{server: server, group: group}
}

func (h *slotTestHarness) unlock(t *testing.T, payload string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/characters/1/equipment-slots/unlock", strings.NewReader(payload))
	request.SetPathValue("id", "1")
	recorder := httptest.NewRecorder()
	h.server.handleUnlockEquipmentSlots(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func (h *slotTestHarness) questStates(t *testing.T) map[int64]dnfrepo.QuestState {
	t.Helper()
	record, found, err := h.group.Quest.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load quests: found=%t err=%v", found, err)
	}
	return record.States
}

func (h *slotTestHarness) slotStat(t *testing.T) int64 {
	t.Helper()
	character, found, err := h.group.Character.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	return character.Stats[extraEquipmentSlotStatColumn]
}

func int64SliceFromResponse(t *testing.T, response map[string]any, key string) []int64 {
	t.Helper()
	raw, ok := response[key].([]any)
	if !ok {
		t.Fatalf("response[%q] missing or not a list: %+v", key, response)
	}
	out := make([]int64, 0, len(raw))
	for _, entry := range raw {
		value, ok := entry.(float64)
		if !ok {
			t.Fatalf("%s entry is not a number: %+v", key, entry)
		}
		out = append(out, int64(value))
	}
	return out
}

func stringSliceFromResponse(t *testing.T, response map[string]any, key string) []string {
	t.Helper()
	raw, ok := response[key].([]any)
	if !ok {
		t.Fatalf("response[%q] missing or not a list: %+v", key, response)
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		value, ok := entry.(string)
		if !ok {
			t.Fatalf("%s entry is not a string: %+v", key, entry)
		}
		out = append(out, value)
	}
	return out
}

// 「开启耳环槽」：必须只置 0x10，并且只把 PVF 里索引 2 的任务标完成。
// 起点用 0（干净角色），避免与「旧实现留下的 7」混淆：结果必须是 0x10。
func TestHandleUnlockEarringSlotWritesEarringBitOnly(t *testing.T) {
	harness := newSlotTestHarness(t, 90, map[string]int64{extraEquipmentSlotStatColumn: 0}, dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			3145: {Status: "active", ProgressValue: 1},
		},
	})
	response := harness.unlock(t, `{"slots":"earring"}`)
	if success, _ := response["success"].(bool); !success {
		t.Fatalf("unlock failed: %+v", response)
	}
	if got := response["exEquipSlotStat"]; got != float64(ExtraEquipmentSlotsEarring) {
		t.Fatalf("exEquipSlotStat = %v, want 16 (0x10)", got)
	}
	if got := stringSliceFromResponse(t, response, "unlockedGroups"); !reflect.DeepEqual(got, []string{"earring"}) {
		t.Fatalf("unlockedGroups = %v, want [earring]", got)
	}
	if got := stringSliceFromResponse(t, response, "completedQuestIds"); len(got) != 0 {
		t.Fatalf("completedQuestIds = %v, want none (row was created)", got)
	}
	if got := int64SliceFromResponse(t, response, "createdQuestIds"); !reflect.DeepEqual(got, []int64{2635, 2636}) {
		t.Fatalf("createdQuestIds = %v, want [2635 2636]", got)
	}
	if got := int64SliceFromResponse(t, response, "questIds"); !reflect.DeepEqual(got, []int64{2635, 2636}) {
		t.Fatalf("questIds = %v, want [2635 2636] (the earring grant quest plus its [pre required quest])", got)
	}
	if stat := harness.slotStat(t); stat != ExtraEquipmentSlotsEarring {
		t.Fatalf("persisted ex_equip_slot_stat = %#x, want 0x10", stat)
	}
	states := harness.questStates(t)
	// 只写耳环那两条，左右槽的 649/650（以及它们的 674/675）一行都不许动。
	if len(states) != 3 {
		t.Fatalf("quest rows = %+v, want exactly the two earring rows plus the untouched unrelated row", states)
	}
	earring, exists := states[2636]
	if !exists || !questStatusCompleted(earring.Status) || earring.Extra["gm_override"] != professionQuestOverrideMarker {
		t.Fatalf("earring quest row = %+v (exists=%t)", earring, exists)
	}
	prerequisite, exists := states[2635]
	if !exists || !questStatusCompleted(prerequisite.Status) || prerequisite.Extra["gm_override"] != professionQuestOverrideMarker {
		t.Fatalf("earring prerequisite row 2635 = %+v (exists=%t)", prerequisite, exists)
	}
	if _, exists := states[649]; exists {
		t.Fatal("left-slot quest 649 must not be touched when only the earring slot is unlocked")
	}
	if _, exists := states[674]; exists {
		t.Fatal("left-slot prerequisite 674 must not be touched when only the earring slot is unlocked")
	}
	if unrelated := states[3145]; unrelated.Status != "active" || unrelated.ProgressValue != 1 {
		t.Fatalf("unrelated quest row was touched: %+v", unrelated)
	}
}

// 线上现状回归：角色是旧实现留下的 7（左右槽开了、耳环没开）时，点「开启耳环槽」
// 必须把它推到当前客户端口径的可读状态，而不是原样返回。
func TestHandleUnlockEarringSlotUpgradesLegacyMask7(t *testing.T) {
	harness := newSlotTestHarness(t, 90, map[string]int64{extraEquipmentSlotStatColumn: 7}, dnfrepo.QuestRecord{})
	response := harness.unlock(t, `{"slots":"earring"}`)
	if got := response["exEquipSlotStat"]; got != float64(0x17) {
		t.Fatalf("exEquipSlotStat = %v, want 23 (0x07|0x10)", got)
	}
	state, _ := response["extraEquipmentState"].(map[string]any)
	if state["earring"] != true || state["all"] != true {
		t.Fatalf("extraEquipmentState = %+v", state)
	}
}

// 旧前端兼容：不带 body POST = 左右槽（A21 语义），结果必须是 0x03 而不是 0x13。
func TestHandleUnlockEquipmentSlotsWithoutBodyUnlocksLeftRightOnly(t *testing.T) {
	harness := newSlotTestHarness(t, 90, map[string]int64{extraEquipmentSlotStatColumn: 0}, dnfrepo.QuestRecord{})
	response := harness.unlock(t, "")
	if success, _ := response["success"].(bool); !success {
		t.Fatalf("unlock failed: %+v", response)
	}
	if got := response["exEquipSlotStat"]; got != float64(0x03) {
		t.Fatalf("exEquipSlotStat = %v, want 3 (0x03)", got)
	}
	if got := int64SliceFromResponse(t, response, "questIds"); !reflect.DeepEqual(got, []int64{649, 650, 674, 675}) {
		t.Fatalf("questIds = %v, want [649 650 674 675]", got)
	}
	state, _ := response["extraEquipmentState"].(map[string]any)
	if state["earring"] != false || state["leftRight"] != true {
		t.Fatalf("extraEquipmentState = %+v", state)
	}
}

// all：一次把三槽开满 = 当前客户端口径 0x13，并且三条授权任务都落 completed。
func TestHandleUnlockAllEquipmentSlotsWritesCurrentClientMask(t *testing.T) {
	harness := newSlotTestHarness(t, 70, map[string]int64{extraEquipmentSlotStatColumn: 0}, dnfrepo.QuestRecord{})
	response := harness.unlock(t, `{"slots":"all"}`)
	if got := response["exEquipSlotStat"]; got != float64(0x13) {
		t.Fatalf("exEquipSlotStat = %v, want 19 (0x13)", got)
	}
	if got := int64SliceFromResponse(t, response, "questIds"); !reflect.DeepEqual(got, []int64{649, 650, 674, 675, 2635, 2636}) {
		t.Fatalf("questIds = %v, want [649 650 674 675 2635 2636]", got)
	}
	if stat := harness.slotStat(t); stat != ExtraEquipmentSlotsAll {
		t.Fatalf("persisted ex_equip_slot_stat = %#x, want 0x13", stat)
	}
	states := harness.questStates(t)
	for _, questID := range []int64{649, 650, 674, 675, 2635, 2636} {
		if state, exists := states[questID]; !exists || !questStatusCompleted(state.Status) {
			t.Fatalf("quest %d row = %+v (exists=%t)", questID, state, exists)
		}
	}
}

// 幂等：位已开、且授权链的任务行本来就全是 completed 时，不写位、不重写任务行、
// 不编造新的完成数。
func TestHandleUnlockEquipmentSlotsIsIdempotent(t *testing.T) {
	harness := newSlotTestHarness(t, 90, map[string]int64{extraEquipmentSlotStatColumn: 0x13}, dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			649:  {Status: "completed"},
			650:  {Status: "completed"},
			2635: {Status: "completed"},
			2636: {Status: "completed"},
		},
	})
	before := harness.questStates(t)
	response := harness.unlock(t, `{"slots":"earring"}`)
	if got := response["exEquipSlotStat"]; got != float64(0x13) {
		t.Fatalf("exEquipSlotStat = %v, want 19", got)
	}
	if got := stringSliceFromResponse(t, response, "unlockedGroups"); len(got) != 0 {
		t.Fatalf("unlockedGroups = %v, want empty", got)
	}
	if got := stringSliceFromResponse(t, response, "alreadyUnlocked"); !reflect.DeepEqual(got, []string{"earring"}) {
		t.Fatalf("alreadyUnlocked = %v", got)
	}
	if got := int64SliceFromResponse(t, response, "completedQuestIds"); len(got) != 0 {
		t.Fatalf("completedQuestIds = %v, want empty", got)
	}
	if got := int64SliceFromResponse(t, response, "createdQuestIds"); len(got) != 0 {
		t.Fatalf("createdQuestIds = %v, want empty", got)
	}
	// 位已开时任务链仍会被解析一次，响应要如实报出这次算的是哪几条。
	if got := int64SliceFromResponse(t, response, "questIds"); !reflect.DeepEqual(got, []int64{2635, 2636}) {
		t.Fatalf("questIds = %v, want [2635 2636]", got)
	}
	if got := int64SliceFromResponse(t, response, "alreadyDoneQuestIds"); !reflect.DeepEqual(got, []int64{2635, 2636}) {
		t.Fatalf("alreadyDoneQuestIds = %v, want [2635 2636]", got)
	}
	after := harness.questStates(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("idempotent call mutated quest rows:\nbefore=%+v\nafter =%+v", before, after)
	}
}

// 等级门槛与未知分组都要拒绝，且拒绝时一个字都不写库。
func TestHandleUnlockEquipmentSlotsRejectsLowLevelAndUnknownGroup(t *testing.T) {
	harness := newSlotTestHarness(t, 69, map[string]int64{extraEquipmentSlotStatColumn: 0}, dnfrepo.QuestRecord{})
	response := harness.unlock(t, `{"slots":"earring"}`)
	if success, _ := response["success"].(bool); success {
		t.Fatalf("level 69 must be rejected: %+v", response)
	}
	if message, _ := response["error"].(string); !strings.Contains(message, "70") {
		t.Fatalf("error = %q, want the level-70 message", message)
	}
	if stat := harness.slotStat(t); stat != 0 {
		t.Fatalf("rejected call wrote ex_equip_slot_stat = %#v", stat)
	}

	harness = newSlotTestHarness(t, 90, map[string]int64{extraEquipmentSlotStatColumn: 0}, dnfrepo.QuestRecord{})
	response = harness.unlock(t, `{"slots":"wing"}`)
	if success, _ := response["success"].(bool); success {
		t.Fatalf("unknown group must be rejected: %+v", response)
	}
	if stat := harness.slotStat(t); stat != 0 {
		t.Fatalf("rejected call wrote ex_equip_slot_stat = %#v", stat)
	}
}

// PVF 与工具口径不一致时必须显式失败，而不是把位开到别的槽上。
// 这条是**开启档**（EquipSlotQuestSync=true）的判据：只有开启档才会去核对 PVF
// 授权任务。默认档不读任务目录，所以这个 PVF 在默认档下只是"开耳环位"，不失败。
func TestHandleUnlockEquipmentSlotsFailsWhenPVFLacksGrantQuest(t *testing.T) {
	source := slotTestSource{
		dnfquest.DefaultList:  "649 `support.qst`\n",
		"n_quest/support.qst": slotExpansionQuest("[common unique]", 0),
	}
	group := memory.NewMemoryGroup()
	if err := group.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "1", AccountID: "acc-1", Name: "tester", Job: "11", Level: 90,
		Stats: map[string]int64{extraEquipmentSlotStatColumn: 7},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := group.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "1"}); err != nil {
		t.Fatalf("save quests: %v", err)
	}
	server := &Server{
		cfg: Config{Logger: func(string, ...any) {}, EquipSlotQuestSync: true},
		deps: Deps{
			Repos: func() (dnfrepo.Group, bool) { return group, true },
			PVF:   func() (dnfpvf.Source, error) { return source, nil },
		},
		pvf: newPVFCache(),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/characters/1/equipment-slots/unlock", strings.NewReader(`{"slots":"earring"}`))
	request.SetPathValue("id", "1")
	recorder := httptest.NewRecorder()
	server.handleUnlockEquipmentSlots(recorder, request)
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	if success, _ := response["success"].(bool); success {
		t.Fatalf("missing PVF grant quest must fail the request: %+v", response)
	}
	character, _, err := group.Character.Load(context.Background(), "1")
	if err != nil {
		t.Fatalf("load character: %v", err)
	}
	if character.Stats[extraEquipmentSlotStatColumn] != 7 {
		t.Fatalf("failed unlock wrote stat = %#v, want unchanged 7", character.Stats[extraEquipmentSlotStatColumn])
	}
}

// 面板读到的逐槽状态必须与持久位一致（含旧值 7 的「耳环没开」）。
func TestCharacterPayloadExposesPerSlotState(t *testing.T) {
	record := dnfrepo.CharacterRecord{
		CharacterID: "1", AccountID: "acc-1", Name: "tester", Job: "11", Level: 90,
		Stats: map[string]int64{extraEquipmentSlotStatColumn: 7},
	}
	server := &Server{cfg: Config{Logger: func(string, ...any) {}}}
	payload := server.characterPayload(record, 0, "tester-job")
	if payload["extraEquipmentSlotsUnlocked"] != false {
		t.Fatalf("extraEquipmentSlotsUnlocked = %v, want false for mask 7", payload["extraEquipmentSlotsUnlocked"])
	}
	if payload["exEquipSlotStat"] != int64(7) {
		t.Fatalf("exEquipSlotStat = %#v, want raw 7", payload["exEquipSlotStat"])
	}
	// 旧值 7 的原始字节在 extraEquipSlotStat 里如实返回；逐槽状态只认 0x13 掩码内
	// 的三位，所以 0x07 & 0x13 = 0x03 → 左右槽已开、耳环没开。
	state, _ := payload["extraEquipmentState"].(map[string]any)
	if state["earring"] != false || state["leftRight"] != true || state["support"] != true || state["magicStone"] != true {
		t.Fatalf("extraEquipmentState = %+v", state)
	}
	if state["mask"] != 3 || state["allMask"] != 19 || state["all"] != false {
		t.Fatalf("extraEquipmentState masks = %+v", state)
	}
	if missing, _ := state["missing"].([]string); !reflect.DeepEqual(missing, []string{"earring"}) {
		t.Fatalf("extraEquipmentState missing = %v", missing)
	}
	record.Stats[extraEquipmentSlotStatColumn] = 0x13
	payload = server.characterPayload(record, 0, "tester-job")
	if payload["extraEquipmentSlotsUnlocked"] != true {
		t.Fatalf("extraEquipmentSlotsUnlocked = %v, want true for mask 0x13", payload["extraEquipmentSlotsUnlocked"])
	}
}

// ---------------------------------------------------------------------------
// 回退档（EquipSlotQuestChainFromEnv == false，即
// DNFBRIDGE_GM_EQUIP_SLOT_QUEST_CHAIN=legacy 或 off/0/…）：
// 只写 ex_equip_slot_stat 的位，逐字节回到改动前的行为。
//
// 四个参考实现（A21 C# 工具、上游 0.1.8 dnfbridge/gm.go、本仓既有的
// cmd/scratch/grantlevel90loadout、90CN 供货方 GmTool90CN/GmService90.cs）都只写
// 这一列、一行任务都不碰。默认档**已经不再是**这一档（现场要求开槽把外传任务一起
// 收掉，见 equipment_slots.go 顶部），所以这些用例必须显式把开关设成回退字面量，
// 否则它们断言的就是另一档行为。
// ---------------------------------------------------------------------------

// 开关解析：只有显式 on 才算开启，别名一律回落默认档（legacy 档的唯一闸门）。
func TestEquipSlotQuestSyncFromEnvIsExplicitOnOnly(t *testing.T) {
	for _, value := range []string{"on", "ON", " On ", "oN"} {
		if !EquipSlotQuestSyncFromEnv(value) {
			t.Fatalf("%q must enable quest sync", value)
		}
	}
	for _, value := range []string{"", " ", "off", "false", "true", "1", "0", "yes", "enabled", "on!"} {
		if EquipSlotQuestSyncFromEnv(value) {
			t.Fatalf("%q must fall back to the default (bit-only) mode", value)
		}
	}
}

// 链路开关解析：空 = on（默认档 = 修复后的行为），只有显式回退字面量才关。
func TestEquipSlotQuestChainFromEnvDefaultsToOn(t *testing.T) {
	for _, value := range []string{"", " ", "on", "ON", "true", "1", "yes", "enabled", "chain", "typo"} {
		if !EquipSlotQuestChainFromEnv(value) {
			t.Fatalf("%q must take the default (fixed) mode", value)
		}
	}
	for _, value := range []string{equipSlotQuestChainLegacy, "LEGACY", " legacy ", "off", "0", "false", "no", "disable", "disabled", "none"} {
		if EquipSlotQuestChainFromEnv(value) {
			t.Fatalf("%q must roll back to the pre-change behaviour", value)
		}
	}
}

// 回退档在干净起点上只开耳环：位变 0x10，任务仓储一行都不许动，响应里也如实
// 报告 questSync=false / questChain=false（面板据此知道没有任务行被写）。
func TestHandleUnlockEquipmentSlotsLegacyRollbackWritesBitOnly(t *testing.T) {
	harness := newSlotTestHarnessMode(t, 90, map[string]int64{extraEquipmentSlotStatColumn: 0},
		dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
			3145: {Status: "active", ProgressValue: 1},
		}}, false)
	t.Setenv(equipSlotQuestChainEnv, equipSlotQuestChainLegacy)
	before := harness.questStates(t)
	response := harness.unlock(t, `{"slots":"earring"}`)
	if success, _ := response["success"].(bool); !success {
		t.Fatalf("unlock failed: %+v", response)
	}
	if got := response["exEquipSlotStat"]; got != float64(ExtraEquipmentSlotsEarring) {
		t.Fatalf("exEquipSlotStat = %v, want 16 (0x10)", got)
	}
	if got := response["questSync"]; got != false {
		t.Fatalf("questSync = %#v, want false", got)
	}
	if got := response["questChain"]; got != false {
		t.Fatalf("questChain = %#v, want false", got)
	}
	if got := int64SliceFromResponse(t, response, "questIds"); len(got) != 0 {
		t.Fatalf("questIds = %v, want none in the bit-only mode", got)
	}
	for _, key := range []string{"completedQuestIds", "createdQuestIds", "alreadyDoneQuestIds"} {
		if got := int64SliceFromResponse(t, response, key); len(got) != 0 {
			t.Fatalf("%s = %v, want none in the bit-only mode", key, got)
		}
	}
	if stat := harness.slotStat(t); stat != ExtraEquipmentSlotsEarring {
		t.Fatalf("persisted ex_equip_slot_stat = %#x, want 0x10", stat)
	}
	after := harness.questStates(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("bit-only mode touched the quest repository:\nbefore=%+v\nafter=%+v", before, after)
	}
	if _, exists := after[2636]; exists {
		t.Fatal("bit-only mode must not create the earring grant quest row")
	}
	if _, exists := after[2635]; exists {
		t.Fatal("bit-only mode must not create the earring prerequisite quest row either")
	}
	// 幂等：位已开后第二次调用连位都不写。
	second := harness.unlock(t, `{"slots":"earring"}`)
	if got := stringSliceFromResponse(t, second, "alreadyUnlocked"); !reflect.DeepEqual(got, []string{"earring"}) {
		t.Fatalf("alreadyUnlocked = %v, want [earring]", got)
	}
	if got := stringSliceFromResponse(t, second, "unlockedGroups"); len(got) != 0 {
		t.Fatalf("unlockedGroups = %v, want none", got)
	}
	if stat := harness.slotStat(t); stat != ExtraEquipmentSlotsEarring {
		t.Fatalf("idempotent call changed the stat: %#x", stat)
	}
}

// 回退档不依赖任务模块：任务仓储为 nil 时「开启特殊装备槽」仍必须成功并写位
// （旧任务 id 口径里 674/675/676/677 是客户端 op 码，历史上正是这一串把开槽
// 绑到了任务模块上）。
func TestHandleUnlockEquipmentSlotsLegacyRollbackNeedsNoQuestRepository(t *testing.T) {
	harness := newSlotTestHarnessWithoutQuestRepo(t)
	t.Setenv(equipSlotQuestChainEnv, equipSlotQuestChainLegacy)
	response := harness.unlock(t, `{"slots":"all"}`)
	if success, _ := response["success"].(bool); !success {
		t.Fatalf("bit-only unlock must not require the quest repository: %+v", response)
	}
	if got := response["exEquipSlotStat"]; got != float64(ExtraEquipmentSlotsAll) {
		t.Fatalf("exEquipSlotStat = %v, want 19 (0x13)", got)
	}
	if stat := harness.slotStat(t); stat != ExtraEquipmentSlotsAll {
		t.Fatalf("persisted ex_equip_slot_stat = %#x, want 0x13", stat)
	}
}

// 默认档（链路开关不设）必须把外传任务收掉 —— 这正是现场报障的判据。任务仓储
// 不可用时默认档必须显式失败且零写入：链路承诺"位与任务行一起落地"，只写位就是
// 现场那条「任务依然存在」。
func TestHandleUnlockEquipmentSlotsDefaultRequiresQuestRepositoryForTheChain(t *testing.T) {
	harness := newSlotTestHarnessWithoutQuestRepo(t)
	response := harness.unlock(t, `{"slots":"earring"}`)
	if success, _ := response["success"].(bool); success {
		t.Fatalf("chain mode must fail without a quest repository: %+v", response)
	}
	if stat := harness.slotStat(t); stat != 0 {
		t.Fatalf("failed unlock wrote ex_equip_slot_stat = %#x, want unchanged 0", stat)
	}
}

// 开启档缺任务仓储时必须显式失败且零写入（回退档与开启档的边界不能含糊）。
func TestHandleUnlockEquipmentSlotsQuestSyncRequiresQuestRepository(t *testing.T) {
	harness := newSlotTestHarnessWithoutQuestRepo(t)
	t.Setenv(equipSlotQuestChainEnv, equipSlotQuestChainLegacy)
	harness.server.cfg.EquipSlotQuestSync = true
	response := harness.unlock(t, `{"slots":"earring"}`)
	if success, _ := response["success"].(bool); success {
		t.Fatalf("quest-sync mode must fail without a quest repository: %+v", response)
	}
	if stat := harness.slotStat(t); stat != 0 {
		t.Fatalf("failed unlock wrote ex_equip_slot_stat = %#x, want unchanged 0", stat)
	}
}

// newSlotTestHarnessWithoutQuestRepo 建一个任务仓储为 nil 的测试台。只关掉 Quest，
// 角色仓储照常可用（面板读角色走的就是它）。
func newSlotTestHarnessWithoutQuestRepo(t *testing.T) *slotTestHarness {
	t.Helper()
	t.Setenv(equipSlotQuestChainEnv, "")
	source := slotTestPVF()
	if _, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}}); err != nil {
		t.Fatalf("test PVF is not loadable: %v", err)
	}
	group := memory.NewMemoryGroup()
	group.Quest = nil
	if err := group.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "1", AccountID: "acc-1", Name: "tester", Job: "11", Level: 90,
		Stats: map[string]int64{extraEquipmentSlotStatColumn: 0},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	server := &Server{
		cfg:  Config{Logger: func(string, ...any) {}},
		deps: Deps{Repos: func() (dnfrepo.Group, bool) { return group, true }, PVF: func() (dnfpvf.Source, error) { return source, nil }},
		pvf:  newPVFCache(),
	}
	return &slotTestHarness{server: server, group: group}
}
