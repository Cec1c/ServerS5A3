package gmtool

import (
	"net/http"
	"strings"
	"testing"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 任务只读查询面。文案全部来自 PVF；缺段必须是空串（界面显示「PVF 未提供」），
// **不允许**任何替代文案。

func readonlyQuestDocument(name, depend, condition, solve string, extra string) string {
	var builder strings.Builder
	builder.WriteString("[grade]\n`[epic]` \n[type]\n`[quest clear]` \n")
	builder.WriteString("[level]\n1 99 \n[job]\n`[all]` \n")
	if name != "" {
		builder.WriteString("[name]\n`" + name + "` \n")
	}
	if depend != "" {
		builder.WriteString("[depend message]\n`" + depend + "` \n")
	}
	if condition != "" {
		builder.WriteString("[condition message]\n`" + condition + "` \n")
	}
	if solve != "" {
		builder.WriteString("[solve message]\n`" + solve + "` \n")
	}
	builder.WriteString(extra)
	return builder.String()
}

func readonlyQuestSource() readonlyTestSource {
	return readonlyTestSource{
		"stackable/stackable.lst": "1 `coin.stk`\n",
		"equipment/equipment.lst": "2 `sword.equ`\n",
		"stackable/coin.stk":      readonlyTestStackable,
		"equipment/sword.equ":     "[name]\n`无套装武器` \n[equipment type]\n`[weapon]` \n",
		dnfquest.DefaultList:      "3970 `q3970.qst`\n3971 `q3971.qst`\n3972 `q3972.qst`\n",
		"n_quest/q3970.qst": readonlyQuestDocument("起始任务", "起始介绍", "起始条件", "起始结算",
			"[reward type]\n`[item]` \n[reward int data]\n1 2 \n"),
		"n_quest/q3971.qst": readonlyQuestDocument("离别时刻", "介绍文本", "完成以下的全部任务。", "结算文本", ""),
		// 真档里 263/2996 条任务没有 [name]，三段展示文本也各有缺口：
		// 这份文档一段都不写，用来钉死「缺段 = 空串」。
		"n_quest/q3972.qst": "[grade]\n`[epic]` \n[type]\n`[quest clear]` \n[level]\n1 99 \n[job]\n`[all]` \n",
	}
}

func questReadonlyHarness(t *testing.T) *readonlyHarness {
	t.Helper()
	return newReadonlyHarness(t, readonlyFixture{
		source:    readonlyQuestSource(),
		withQuest: true,
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: "11", Level: 90,
			Stats: map[string]int64{"grow_type": 0},
		},
		quests: &dnfrepo.QuestRecord{
			States: map[int64]dnfrepo.QuestState{
				3971: {Status: "completed"},
				3972: {Status: "active"},
			},
			Progress: map[int64]dnfrepo.QuestState{
				3973: {Status: "progress", ProgressValue: 5},
			},
		},
	})
}

func TestQuestOverviewSplitsActiveAndCompleted(t *testing.T) {
	harness := questReadonlyHarness(t)
	_, response := harness.get(t, "/api/characters/1/quests", map[string]string{"id": "1"}, harness.server.handleQuestOverview)
	requireOK(t, http.StatusOK, response)

	if response["characterId"] != float64(1) {
		t.Fatalf("characterId = %v", response["characterId"])
	}
	active := responseList(t, response, "active")
	if len(active) != 2 || response["activeCount"] != float64(2) {
		t.Fatalf("active = %+v count=%v, want 3972 + 3973", active, response["activeCount"])
	}
	// Progress 里的任务同样计入（上游遍历 States + Progress 并按 id 去重）。
	if active[0].(map[string]any)["questId"] != float64(3972) ||
		active[1].(map[string]any)["questId"] != float64(3973) {
		t.Fatalf("active ids = %+v, want sorted [3972 3973]", active)
	}
	if active[0].(map[string]any)["name"] != "" {
		t.Fatalf("known-but-nameless quest must keep the empty PVF name: %+v", active[0])
	}
	// PVF 里没有的任务只留 ID：不报错、也不编造名称/路径（上游 gm.go:944-946）。
	unknown := active[1].(map[string]any)
	if unknown["path"] != "" || unknown["grade"] != "" || unknown["levelMin"] != float64(0) {
		t.Fatalf("quest missing from the PVF must stay ID-only: %+v", unknown)
	}
	if unknown["progressValue"] != float64(5) || unknown["status"] != "progress" {
		t.Fatalf("progress row = %+v", unknown)
	}

	completed := responseList(t, response, "completed")
	if len(completed) != 1 || response["completedCount"] != float64(1) {
		t.Fatalf("completed = %+v count=%v", completed, response["completedCount"])
	}
	row := completed[0].(map[string]any)
	if row["questId"] != float64(3971) || row["name"] != "离别时刻" || row["completed"] != true {
		t.Fatalf("completed row = %+v", row)
	}
	if row["active"] != false || row["acceptable"] != false {
		t.Fatalf("a completed quest must never be active/acceptable: %+v", row)
	}
}

func TestQuestSearchAnnotatesAcceptableWithoutInventingNames(t *testing.T) {
	harness := questReadonlyHarness(t)

	_, response := harness.get(t, "/api/characters/1/quests/search?q=%E7%A6%BB%E5%88%AB",
		map[string]string{"id": "1"}, harness.server.handleQuestSearch)
	requireOK(t, http.StatusOK, response)
	results := responseList(t, response, "results")
	if len(results) != 1 || results[0].(map[string]any)["questId"] != float64(3971) {
		t.Fatalf("search by [name] = %+v, want only 3971", results)
	}
	if results[0].(map[string]any)["acceptable"] != false {
		t.Fatalf("completed quest must not be acceptable: %+v", results[0])
	}

	// 数字 query = 任务 ID 精确命中。
	_, response = harness.get(t, "/api/characters/1/quests/search?q=3970", map[string]string{"id": "1"}, harness.server.handleQuestSearch)
	results = responseList(t, response, "results")
	if len(results) != 1 || results[0].(map[string]any)["questId"] != float64(3970) {
		t.Fatalf("numeric search = %+v", results)
	}
	if results[0].(map[string]any)["acceptable"] != true {
		t.Fatalf("a fresh epic quest for an eligible character must be acceptable: %+v", results[0])
	}

	// 路径子串命中（给没有 [name] 的任务留的降级通道）。
	_, response = harness.get(t, "/api/characters/1/quests/search?q=q3972", map[string]string{"id": "1"}, harness.server.handleQuestSearch)
	results = responseList(t, response, "results")
	if len(results) != 1 || results[0].(map[string]any)["questId"] != float64(3972) {
		t.Fatalf("path search = %+v", results)
	}
	if results[0].(map[string]any)["name"] != "" {
		t.Fatalf("a quest without [name] must not get a fabricated name: %+v", results[0])
	}

	// 空 query = 第一页；limit 生效。
	_, response = harness.get(t, "/api/characters/1/quests/search?limit=2", map[string]string{"id": "1"}, harness.server.handleQuestSearch)
	results = responseList(t, response, "results")
	if len(results) != 2 || results[0].(map[string]any)["questId"] != float64(3970) ||
		results[1].(map[string]any)["questId"] != float64(3971) {
		t.Fatalf("empty query page = %+v, want [3970 3971]", results)
	}
}

func TestQuestDetailReturnsPVFTextAndEmptyWhenAbsent(t *testing.T) {
	harness := questReadonlyHarness(t)

	_, response := harness.get(t, "/api/characters/1/quests/3971/detail", map[string]string{"id": "1", "questId": "3971"}, harness.server.handleQuestDetail)
	requireOK(t, http.StatusOK, response)
	if response["introduction"] != "介绍文本" || response["objective"] != "完成以下的全部任务。" ||
		response["completion"] != "结算文本" {
		t.Fatalf("PVF messages = %q / %q / %q", response["introduction"], response["objective"], response["completion"])
	}
	quest := responseMap(t, response, "quest")
	if quest["name"] != "离别时刻" || quest["completed"] != true || quest["acceptable"] != false {
		t.Fatalf("quest = %+v", quest)
	}
	if quest["levelMin"] != float64(1) || quest["levelMax"] != float64(99) {
		t.Fatalf("level window = %v..%v", quest["levelMin"], quest["levelMax"])
	}

	// 同目录里 3972 一段展示文本都没有：必须留空，前端显示「PVF 未提供」。
	_, response = harness.get(t, "/api/characters/1/quests/3972/detail", map[string]string{"id": "1", "questId": "3972"}, harness.server.handleQuestDetail)
	requireOK(t, http.StatusOK, response)
	for _, key := range []string{"introduction", "objective", "completion"} {
		if response[key] != "" {
			t.Fatalf("%s = %q, want empty (PVF 未提供)", key, response[key])
		}
	}
	if responseMap(t, response, "quest")["name"] != "" {
		t.Fatalf("name must stay empty when [name] is absent: %+v", responseMap(t, response, "quest"))
	}
}

func TestQuestDetailReturnsRewardItemsWithIndexNames(t *testing.T) {
	harness := questReadonlyHarness(t)
	_, response := harness.get(t, "/api/characters/1/quests/3970/detail", map[string]string{"id": "1", "questId": "3970"}, harness.server.handleQuestDetail)
	requireOK(t, http.StatusOK, response)
	rewards := responseList(t, response, "rewardItems")
	if len(rewards) != 1 {
		t.Fatalf("rewardItems = %+v", rewards)
	}
	reward := rewards[0].(map[string]any)
	if reward["itemId"] != float64(1) || reward["count"] != float64(2) {
		t.Fatalf("reward = %+v", reward)
	}
	if reward["name"] != "测试消耗品" {
		t.Fatalf("reward name = %v, want the PVF-index name", reward["name"])
	}
}

func TestQuestDetailRejectsUnknownAndOutOfRangeIDs(t *testing.T) {
	harness := questReadonlyHarness(t)
	// 不在 PVF 的任务：报错，且必须点名 Script.pvf（与上游 gm.go:1003 同义）。
	_, response := harness.get(t, "/api/characters/1/quests/3973/detail", map[string]string{"id": "1", "questId": "3973"}, harness.server.handleQuestDetail)
	if message := requireFailure(t, http.StatusOK, response); !strings.Contains(message, "Script.pvf") {
		t.Fatalf("message = %q, want it to name Script.pvf", message)
	}
	for _, questID := range []string{"0", "-1", "65536", "abc"} {
		_, response = harness.get(t, "/api/characters/1/quests/"+questID+"/detail",
			map[string]string{"id": "1", "questId": questID}, harness.server.handleQuestDetail)
		requireFailure(t, http.StatusOK, response)
	}
}

func TestQuestEndpointsRejectUnparsableStoredJob(t *testing.T) {
	harness := newReadonlyHarness(t, readonlyFixture{
		source:    readonlyQuestSource(),
		withQuest: true,
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: "not-a-job", Level: 90, Stats: map[string]int64{},
		},
	})
	// 上游对无法解析的 job 直接报错，而不是拿 0（鬼剑士）顶上——那会把别的职业的
	// 任务误标成可接。
	for name, call := range map[string]func() (int, map[string]any){
		"overview": func() (int, map[string]any) {
			return harness.get(t, "/api/characters/1/quests", map[string]string{"id": "1"}, harness.server.handleQuestOverview)
		},
		"search": func() (int, map[string]any) {
			return harness.get(t, "/api/characters/1/quests/search?q=3970", map[string]string{"id": "1"}, harness.server.handleQuestSearch)
		},
		"detail": func() (int, map[string]any) {
			return harness.get(t, "/api/characters/1/quests/3971/detail", map[string]string{"id": "1", "questId": "3971"}, harness.server.handleQuestDetail)
		},
	} {
		_, response := call()
		if message := requireFailure(t, http.StatusOK, response); !strings.Contains(message, "职业") {
			t.Fatalf("%s: message = %q, want the stored-job complaint", name, message)
		}
	}
}

func TestQuestStatusVocabularyMatchesServerAndUpstream(t *testing.T) {
	// 服务端 modules/dnf/quest 的 normalizer 与上游 gmQuestStatus* 的同义词并集：
	// 任何一边认为是「在做/已完成」的写法，面板都不能显示成别的状态。
	for _, status := range []string{"active", "ACCEPTED", "in-progress", "progress", "in_progress"} {
		if !isActiveQuestStatus(status) {
			t.Fatalf("%q must count as active", status)
		}
	}
	for _, status := range []string{"complete", "completed", "cleared", "finished", "done", "rewarded"} {
		if !isCompletedQuestStatus(status) {
			t.Fatalf("%q must count as completed", status)
		}
	}
	for _, status := range []string{"", "not_started", "available", "failed"} {
		if isActiveQuestStatus(status) || isCompletedQuestStatus(status) {
			t.Fatalf("%q must count as neither active nor completed", status)
		}
	}
}

func TestProfessionRewardTypeOnlyMatchesPVFLiterals(t *testing.T) {
	for _, value := range []string{"`[grow type]`", "[Awakening Type]", " grow type "} {
		if !isProfessionRewardType(value) {
			t.Fatalf("%q must be a profession reward type", value)
		}
	}
	// 绝不用 job id / 其它奖励类型猜。
	for _, value := range []string{"`[item]`", "`[title]`", "11", "growtype"} {
		if isProfessionRewardType(value) {
			t.Fatalf("%q must NOT be a profession reward type", value)
		}
	}
}
