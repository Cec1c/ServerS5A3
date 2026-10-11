package gmtool

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// ---------------------------------------------------------------------------
// 计划层：只算改动，不落库
// ---------------------------------------------------------------------------

func TestPlanProfessionQuestStatesFlipsActiveRowsAndCreatesMissingOnes(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{
		CharacterID: "1",
		States: map[int64]dnfrepo.QuestState{
			6926: {Status: "active", ProgressValue: 5, Extra: map[string]string{"pvf_path": "n_quest/job/gb_hitman_01.qst"}},
			3145: {Status: "active", ProgressValue: 1},
			777:  {Status: "completed"},
		},
		Progress: map[int64]dnfrepo.QuestState{
			6887: {Status: "active", ProgressValue: 3},
		},
	}
	updated, plan := planProfessionQuestStates(record, []int64{6927, 6926, 6887}, nil, now)

	if !plan.changed || !reflect.DeepEqual(plan.Created, []int64{6927}) ||
		!reflect.DeepEqual(plan.Completed, []int64{6887, 6926}) || len(plan.AlreadyDone) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if !reflect.DeepEqual(plan.TargetIDs, []int64{6887, 6926, 6927}) {
		t.Fatalf("target ids = %v", plan.TargetIDs)
	}
	if !reflect.DeepEqual(plan.savedFields, []dnfrepo.QuestField{dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress}) {
		t.Fatalf("saved fields = %v", plan.savedFields)
	}

	// 已存在的 active 行翻成 completed 并清掉进度，但保留原有 Extra（回执）。
	flipped := updated.States[6926]
	if flipped.Status != "completed" || flipped.ProgressValue != 0 || flipped.UpdatedAt.IsZero() ||
		flipped.Extra["pvf_path"] != "n_quest/job/gb_hitman_01.qst" {
		t.Fatalf("existing row = %+v", flipped)
	}
	// Progress 里的孤行也必须一起翻，否则它仍会出现在 op0x23E 进行中快照里。
	if progress := updated.Progress[6887]; progress.Status != "completed" || progress.ProgressValue != 0 {
		t.Fatalf("progress row = %+v", progress)
	}
	// 缺失的行按需补 completed，只带 GM 来源标记，不伪造奖励/进度。
	created := updated.States[6927]
	if created.Status != "completed" || created.ProgressValue != 0 || created.RewardSelectIndex != 0 || created.Multiplier != 0 {
		t.Fatalf("created row = %+v", created)
	}
	if len(created.Extra) != 1 || created.Extra["gm_override"] != professionQuestOverrideMarker {
		t.Fatalf("created row extra = %+v", created.Extra)
	}
	if _, exists := created.Extra["reward_state"]; exists {
		t.Fatalf("created row fabricated a reward receipt: %+v", created.Extra)
	}
	// 无关任务一行都不许动。
	if state := updated.States[3145]; state.Status != "active" || state.ProgressValue != 1 {
		t.Fatalf("unrelated active quest was touched: %+v", state)
	}
	if state := updated.States[777]; state.Status != "completed" {
		t.Fatalf("unrelated completed quest was touched: %+v", state)
	}
}

func TestPlanProfessionQuestStatesIsIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{
		CharacterID: "1",
		States: map[int64]dnfrepo.QuestState{
			6926: {Status: "active", ProgressValue: 5},
		},
	}
	targets := []int64{6926, 6887, 6927}
	first, firstPlan := planProfessionQuestStates(record, targets, nil, now)
	if !firstPlan.changed || len(firstPlan.Created) != 2 || len(firstPlan.Completed) != 1 {
		t.Fatalf("first plan = %+v", firstPlan)
	}

	second, secondPlan := planProfessionQuestStates(first, targets, nil, now.Add(time.Hour))
	if secondPlan.changed || len(secondPlan.savedFields) != 0 || len(secondPlan.Created) != 0 || len(secondPlan.Completed) != 0 {
		t.Fatalf("second plan must be a no-op: %+v", secondPlan)
	}
	if !reflect.DeepEqual(secondPlan.AlreadyDone, []int64{6887, 6926, 6927}) {
		t.Fatalf("second plan already-completed = %v", secondPlan.AlreadyDone)
	}
	// 幂等：重复点击不重写时间戳、不改内容。
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second plan mutated the record:\nfirst  = %+v\nsecond = %+v", first, second)
	}
}

func TestPlanProfessionQuestStatesReportsDowngradeLeftoversWithoutDeleting(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{
		CharacterID: "1",
		States: map[int64]dnfrepo.QuestState{
			6905: {Status: "completed"},
			6926: {Status: "completed"},
		},
	}
	// 从二次觉醒改回一次觉醒：6905 属于更高段位，只报告不删除。
	updated, plan := planProfessionQuestStates(record, []int64{6926, 6927}, []int64{6905, 6926, 6927}, now)
	if !reflect.DeepEqual(plan.StaleHigher, []int64{6905}) {
		t.Fatalf("stale higher stage ids = %v", plan.StaleHigher)
	}
	if state, exists := updated.States[6905]; !exists || state.Status != "completed" {
		t.Fatalf("downgrade must not delete completed rows: %+v", state)
	}
}

// ---------------------------------------------------------------------------
// 处理器层：完整走一遍 PVF -> 角色/技能/任务三处写入
// ---------------------------------------------------------------------------

type growQuestTestSource map[string]string

func (s growQuestTestSource) ReadText(relativePath string) (string, error) {
	text, ok := s[relativePath]
	if !ok {
		return "", errors.New("missing test PVF path: " + relativePath)
	}
	return text, nil
}

// growQuestTestPVF 用运行时 Script.pvf 的同一批节名构造最小 PVF：
// character/*.chr（职业成长 + 转职/觉醒技能）、skill/*（技能树）、
// n_quest/*（转职 1 / 一次觉醒 2 / 二次觉醒 3 的任务链）。
func growQuestTestPVF() growQuestTestSource {
	return growQuestTestSource{
		"character/character.lst": "15 `GunBlader.chr`\n",
		// charstat 与 profession 共用同一份 character/*.chr：[growtype 2] 既是
		// 1 转的成长段，也是 [awakening 1]/[awakening 2] 的宿主段，
		// [growtype name] 的第 1/2 项对应转职前后名称。
		"character/GunBlader.chr": "[growtype name]\n`枪剑士` `暗刃`\n" +
			"[initial value]\n[skill]\n1 1\n[/skill]\n" +
			"[HP MAX] 100\n" +
			"[growtype 1]\n[skill]\n2 1\n[/skill]\n" +
			"[HP MAX] 120\n" +
			"[growtype 2]\n[skill]\n5 1\n[/skill]\n" +
			"[awakening name]\n`暗刃·一觉` `暗刃·二觉`\n" +
			"[HP MAX] 130\n" +
			"[awakening 1]\n[awakening skill]\n3 1\n[/awakening skill]\n[HP MAX] 140\n" +
			"[awakening 2]\n[awakening skill]\n4 1\n[/awakening skill]\n[HP MAX] 160\n",
		dnfskill.DefaultList: "15 `GunBlader.lst`\n",
		"skill/GunBlader.lst": "1 `GunBlader/a.skl` 2 `GunBlader/b.skl` 3 `GunBlader/c.skl` " +
			"4 `GunBlader/d.skl` 5 `GunBlader/e.skl`\n",
		"skill/GunBlader/a.skl": "[name]\n`a`\n[skill type]\n`[active]`\n",
		"skill/GunBlader/b.skl": "[name]\n`b`\n[skill type]\n`[passive]`\n",
		"skill/GunBlader/c.skl": "[name]\n`c`\n[skill type]\n`[active]`\n",
		"skill/GunBlader/d.skl": "[name]\n`d`\n[skill type]\n`[passive]`\n",
		"skill/GunBlader/e.skl": "[name]\n`e`\n[skill type]\n`[passive]`\n",
		dnfquest.DefaultList: "6926 `job/b1.qst`\n" +
			"6927 `job/congrats.qst`\n" +
			"6884 `awaken/first_1.qst`\n" +
			"6885 `awaken/first_2.qst`\n" +
			"6887 `awaken/first_3.qst`\n" +
			"6900 `awaken/second_1.qst`\n" +
			"6905 `awaken/second_6.qst`\n",
		"n_quest/job/b1.qst": "[grade]\n`[common unique]`\n[level]\n15 99\n[job]\n`[gun blader]`\n" +
			"[job change quest]\n1\n[collision quest]\n6881\n[reward type]\n`[grow type]`\n[reward int data]\n1\n",
		"n_quest/job/congrats.qst": "[grade]\n`[common unique]`\n[level]\n15 99\n[job]\n`[gun blader]`\n" +
			"[job change quest]\n1\n[pre required quest]\n6926\n",
		"n_quest/awaken/first_1.qst": "[grade]\n`[achievement]`\n[level]\n50 99\n[job]\n`[gun blader]`\n" +
			"[job change quest]\n2\n[grow type]\n1\n[target character]\n`[gun blader]` 1 0\n",
		"n_quest/awaken/first_2.qst": "[grade]\n`[achievement]`\n[level]\n50 99\n[job]\n`[gun blader]`\n" +
			"[job change quest]\n2\n[grow type]\n1\n[target character]\n`[gun blader]` 1 0\n[pre required quest]\n6884\n",
		"n_quest/awaken/first_3.qst": "[grade]\n`[achievement]`\n[level]\n50 99\n[job]\n`[gun blader]`\n" +
			"[job change quest]\n2\n[grow type]\n1\n[target character]\n`[gun blader]` 1 0\n[pre required quest]\n6885\n" +
			"[reward type]\n`[awakening type]`\n[reward int data]\n1\n",
		"n_quest/awaken/second_1.qst": "[grade]\n`[achievement]`\n[level]\n75 99\n[job]\n`[gun blader]`\n" +
			"[job change quest]\n3\n[target character]\n`[gun blader]` 1 1\n",
		"n_quest/awaken/second_6.qst": "[grade]\n`[achievement]`\n[level]\n75 99\n[job]\n`[gun blader]`\n" +
			"[job change quest]\n3\n[grow type]\n1\n[pre required quest]\n6900\n" +
			"[reward type]\n`[awakening type]`\n[reward int data]\n2\n",
	}
}

type growQuestTestHarness struct {
	server    *Server
	group     dnfrepo.Group
	quests    dnfrepo.QuestRecord
	character dnfrepo.CharacterRecord
}

func newGrowQuestTestHarness(t *testing.T) *growQuestTestHarness {
	t.Helper()
	source := growQuestTestPVF()
	if _, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList, dnfquest.DefaultList}}); err != nil {
		t.Fatalf("test PVF is not loadable: %v", err)
	}
	group := memory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "1",
		AccountID:   "acc-1",
		Name:        "tester",
		Job:         "15",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 0, "exp": 1234, "gold": 777},
	}
	if err := group.Character.Save(context.Background(), character); err != nil {
		t.Fatalf("save character: %v", err)
	}
	quests := dnfrepo.QuestRecord{
		CharacterID: "1",
		States: map[int64]dnfrepo.QuestState{
			6926: {Status: "active", ProgressValue: 7},
			3145: {Status: "active", ProgressValue: 1},
		},
	}
	if err := group.Quest.Save(context.Background(), quests); err != nil {
		t.Fatalf("save quests: %v", err)
	}
	server := &Server{
		cfg: Config{Logger: func(string, ...any) {}},
		deps: Deps{
			Repos: func() (dnfrepo.Group, bool) { return group, true },
			PVF:   func() (dnfpvf.Source, error) { return source, nil },
		},
		pvf: newPVFCache(),
	}
	return &growQuestTestHarness{server: server, group: group, quests: quests, character: character}
}

func (h *growQuestTestHarness) postGrowType(t *testing.T, payload string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/characters/1/growtype", strings.NewReader(payload))
	request.SetPathValue("id", "1")
	recorder := httptest.NewRecorder()
	h.server.handleSetGrowType(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func questIDsFromResponse(t *testing.T, response map[string]any, key string) []int64 {
	t.Helper()
	quests, ok := response["quests"].(map[string]any)
	if !ok {
		t.Fatalf("response has no quests payload: %+v", response)
	}
	raw, ok := quests[key].([]any)
	if !ok {
		t.Fatalf("quests.%s missing: %+v", key, quests)
	}
	ids := make([]int64, 0, len(raw))
	for _, entry := range raw {
		value, ok := entry.(float64)
		if !ok {
			t.Fatalf("quests.%s entry is not a number: %+v", key, entry)
		}
		ids = append(ids, int64(value))
	}
	return ids
}

func TestHandleSetGrowTypeSyncsProfessionQuestStates(t *testing.T) {
	harness := newGrowQuestTestHarness(t)
	ctx := context.Background()

	response := harness.postGrowType(t, `{"first":1,"second":2}`)
	if success, _ := response["success"].(bool); !success {
		t.Fatalf("grow override failed: %+v", response)
	}
	if got := response["quests"].(map[string]any)["unchanged"].(bool); got {
		t.Fatalf("first call must change quest rows: %+v", response["quests"])
	}
	// PVF 证据：job 15 / first=1 / second=2 的转职 6926、转职祝贺 6927、
	// 一次觉醒链 6884/6885/6887、二次觉醒链 6900/6905。
	if got := questIDsFromResponse(t, response, "targetIds"); !reflect.DeepEqual(got, []int64{6884, 6885, 6887, 6900, 6905, 6926, 6927}) {
		t.Fatalf("target ids = %v", got)
	}
	if got := questIDsFromResponse(t, response, "completedIds"); !reflect.DeepEqual(got, []int64{6926}) {
		t.Fatalf("completed ids = %v", got)
	}
	if got := questIDsFromResponse(t, response, "createdIds"); !reflect.DeepEqual(got, []int64{6884, 6885, 6887, 6900, 6905, 6927}) {
		t.Fatalf("created ids = %v", got)
	}

	character, found, err := harness.group.Character.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	if character.Stats["grow_type"] != 0x21 || character.Job != "15" {
		t.Fatalf("character grow/job = %d/%s", character.Stats["grow_type"], character.Job)
	}
	// 不伪造奖励：经验/金币一字不动。
	if character.Stats["exp"] != 1234 || character.Stats["gold"] != 777 {
		t.Fatalf("override fabricated progression: %+v", character.Stats)
	}

	record, found, err := harness.group.Quest.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load quests: found=%t err=%v", found, err)
	}
	classChange := record.States[6926]
	if classChange.Status != "completed" || classChange.ProgressValue != 0 || classChange.Extra["gm_override"] != "" {
		t.Fatalf("existing class-change row = %+v", classChange)
	}
	for _, questID := range []int64{6884, 6885, 6887, 6900, 6905, 6927} {
		state, exists := record.States[questID]
		if !exists || state.Status != "completed" || state.ProgressValue != 0 || state.Extra["gm_override"] != professionQuestOverrideMarker {
			t.Fatalf("created quest %d row = %+v exists=%t", questID, state, exists)
		}
		if _, exists := state.Extra["reward_state"]; exists {
			t.Fatalf("quest %d fabricated a reward receipt: %+v", questID, state.Extra)
		}
	}
	if unrelated := record.States[3145]; unrelated.Status != "active" || unrelated.ProgressValue != 1 {
		t.Fatalf("unrelated quest was touched: %+v", unrelated)
	}
	if len(record.States) != 8 {
		t.Fatalf("unexpected quest rows: %+v", record.States)
	}

	// 幂等：再次点击不产生任何写入。
	before := dnfrepo.CloneQuest(record)
	again := harness.postGrowType(t, `{"first":1,"second":2}`)
	if unchanged, _ := again["quests"].(map[string]any)["unchanged"].(bool); !unchanged {
		t.Fatalf("second call must report unchanged: %+v", again["quests"])
	}
	if created := questIDsFromResponse(t, again, "createdIds"); len(created) != 0 {
		t.Fatalf("second call created rows: %v", created)
	}
	after, _, err := harness.group.Quest.Load(ctx, "1")
	if err != nil {
		t.Fatalf("reload quests: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("second call mutated quest rows:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

func TestHandleSetGrowTypeReportsDowngradeLeftovers(t *testing.T) {
	harness := newGrowQuestTestHarness(t)
	ctx := context.Background()

	harness.postGrowType(t, `{"first":1,"second":2}`)
	response := harness.postGrowType(t, `{"first":1,"second":1}`)
	// 从二次觉醒改回一次觉醒：6900/6905 仍是完成行，只报告，不删除。
	if got := questIDsFromResponse(t, response, "staleHigherStageIds"); !reflect.DeepEqual(got, []int64{6900, 6905}) {
		t.Fatalf("stale higher stage ids = %v", got)
	}
	record, _, err := harness.group.Quest.Load(ctx, "1")
	if err != nil {
		t.Fatalf("reload quests: %v", err)
	}
	if record.States[6905].Status != "completed" {
		t.Fatalf("downgrade deleted a completed row: %+v", record.States[6905])
	}
	if record.States[6926].Status != "completed" {
		t.Fatalf("downgrade dropped the class-change row: %+v", record.States[6926])
	}
}
