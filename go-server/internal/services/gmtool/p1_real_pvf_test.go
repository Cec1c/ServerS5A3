package gmtool

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档 Script.pvf 上的端到端取证（需要 DNFBRIDGE_REAL_PVF_SMOKE 指向 Script.pvf）。
//
// 期望值全部来自对同一份 PVF 的 dump，不是从上游测试抄来的：
//
//	go run ./cmd/scratch/pvfdump -pvf <Script.pvf> -path stackable/490007001/chn_490007493.stk
//	go run ./cmd/scratch/pvfdump -pvf <Script.pvf> -path equipment/character/partset/2choroset1.equ
//	go run ./cmd/scratch/pvfdump -pvf <Script.pvf> -path n_quest/quest_renewal/epic_quest/devildom/timebeast/timebeast_epic_25.qst

func loadRealPVFSource(t *testing.T) dnfpvf.Source {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the P1 read-only surface against the runtime PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real PVF: %v", err)
	}
	return archive
}

func realPVFCharacter() dnfrepo.CharacterRecord {
	// 与既有真档用例同一形态：job 14 / grow_type 0x21（二觉）/ Lv90。
	return dnfrepo.CharacterRecord{
		AccountID: "acc-1", Name: "tester", Job: "14", Level: 90,
		Stats: map[string]int64{"grow_type": 0x21},
	}
}

func TestRealPVFItemDetailMatchesTheDumpedStackableDocument(t *testing.T) {
	source := loadRealPVFSource(t)
	entry := itemEntry{ID: 490007493, Kind: "stackable", PVFPath: "490007001/chn_490007493.stk"}
	parseItemEntry(source, &entry)
	if strings.TrimSpace(entry.Name) != "艾肯通行证" {
		t.Fatalf("indexed name = %q, want 艾肯通行证 (item index must read the real document)", entry.Name)
	}
	if entry.Invalid {
		t.Fatal("item 490007493 must parse from the real PVF")
	}

	harness := newReadonlyHarness(t, readonlyFixture{
		pvf:       source,
		character: realPVFCharacter(),
		entries:   []itemEntry{entry},
	})
	_, response := harness.get(t, "/api/items/490007493/detail", map[string]string{"itemId": "490007493"}, harness.server.handleItemDetail)
	requireOK(t, http.StatusOK, response)

	if !strings.Contains(response["explain"].(string), "艾肯地下城的入场凭证") {
		t.Fatalf("explain = %q", response["explain"])
	}
	if response["flavorText"] != "<欢乐劳动节 礼物周周拿>" {
		t.Fatalf("flavorText = %q", response["flavorText"])
	}
	if response["weight"] != float64(1) {
		t.Fatalf("weight = %v, want 1 (the document has [weight] 1)", response["weight"])
	}
	item := responseMap(t, response, "item")
	if item["iconPath"] != "Item/stackable/consumption.img" || item["iconIndex"] != float64(2066) {
		t.Fatalf("icon = %v / %v, want the real [icon] pair", item["iconPath"], item["iconIndex"])
	}
	if item["attachType"] != "[account]" || item["stackableType"] != "[material]" {
		t.Fatalf("attachType/stackableType = %v / %v", item["attachType"], item["stackableType"])
	}
	// 这份文档没有 [basic explain]/[detail explain]/[repair price]/[value]：
	// 必须留空 / 缺省，不许补默认值。
	if response["basicExplain"] != "" || response["detailExplain"] != "" {
		t.Fatalf("absent explain sections must stay empty: %q / %q", response["basicExplain"], response["detailExplain"])
	}
	for _, key := range []string{"repairPrice", "value", "partSetIndex", "equipmentSet", "qualityPercent"} {
		if _, present := response[key]; present {
			t.Fatalf("%s must be absent for this stackable document, got %v", key, response[key])
		}
	}
}

func TestRealPVFPartSetDetailMatchesTheDumpedSetFile(t *testing.T) {
	source := loadRealPVFSource(t)
	harness := newReadonlyHarness(t, readonlyFixture{
		pvf:       source,
		character: realPVFCharacter(),
	})
	set, err := harness.server.partSetDetail(context.Background(), 10)
	if err != nil {
		t.Fatalf("part set 10: %v", err)
	}
	if set == nil {
		t.Fatal("part set 10 must resolve on the real PVF (etc/equipmentpartset.etc lists it)")
	}
	if set["pvfPath"] != "equipment/character/partset/2choroset1.equ" {
		t.Fatalf("set path = %v, want the equipment/-prefixed path from the real list", set["pvfPath"])
	}
	if set["name"] != "Mr.根之源 套装" {
		t.Fatalf("set name = %v, want Mr.根之源 套装 (whitespace collapsed)", set["name"])
	}
	bonuses, _ := set["bonuses"].([]equipmentSetBonus)
	if len(bonuses) != 3 {
		t.Fatalf("bonuses = %d blocks, want 3 (the real file has [piece set ability] 3 / 6 / 9)", len(bonuses))
	}
	for index, want := range []int{3, 6, 9} {
		if bonuses[index].RequiredPieces != want {
			t.Fatalf("bonus[%d].RequiredPieces = %d, want %d", index, bonuses[index].RequiredPieces, want)
		}
	}
	if !strings.HasPrefix(bonuses[0].BasicExplain, "[三段斩]攻击力 +20%") {
		t.Fatalf("bonus[0].BasicExplain = %q, want the dumped [parameter basic explain] text", bonuses[0].BasicExplain)
	}
	if !strings.Contains(bonuses[1].BasicExplain, "冷却时间 -30%") {
		t.Fatalf("bonus[1].BasicExplain = %q", bonuses[1].BasicExplain)
	}
	if bonuses[0].DetailExplain != "" {
		t.Fatalf("this set has no [parameter detail explain]; got %q", bonuses[0].DetailExplain)
	}
	// setID 不在列表里 -> 静默无套装，不是错误。
	missing, err := harness.server.partSetDetail(context.Background(), 999999)
	if err != nil || missing != nil {
		t.Fatalf("unknown set id must degrade silently, got set=%v err=%v", missing, err)
	}
	if zero, err := harness.server.partSetDetail(context.Background(), 0); err != nil || zero != nil {
		t.Fatalf("set id 0 must degrade silently, got set=%v err=%v", zero, err)
	}
}

func TestRealPVFQuestEndpointsReadTheFarewellMoment(t *testing.T) {
	source := loadRealPVFSource(t)
	harness := newReadonlyHarness(t, readonlyFixture{
		pvf:       source,
		withQuest: true,
		character: realPVFCharacter(),
		quests: &dnfrepo.QuestRecord{
			States: map[int64]dnfrepo.QuestState{
				3970: {Status: "completed"},
				3971: {Status: "completed"},
			},
		},
	})

	_, response := harness.get(t, "/api/characters/1/quests/3971/detail",
		map[string]string{"id": "1", "questId": "3971"}, harness.server.handleQuestDetail)
	requireOK(t, http.StatusOK, response)
	if responseMap(t, response, "quest")["name"] != "离别时刻" {
		t.Fatalf("quest name = %v, want 离别时刻", responseMap(t, response, "quest")["name"])
	}
	if !strings.HasPrefix(response["introduction"].(string), "卢克大人拯救了异面族") {
		t.Fatalf("introduction = %.40q", response["introduction"])
	}
	if response["objective"] != "完成以下的全部任务。" {
		t.Fatalf("objective = %q", response["objective"])
	}
	if !strings.HasPrefix(response["completion"].(string), "给我安静！") {
		t.Fatalf("completion = %.40q", response["completion"])
	}
	if responseMap(t, response, "quest")["completed"] != true {
		t.Fatalf("quest 3971 must read as completed: %+v", responseMap(t, response, "quest"))
	}

	// 搜索：按真实任务名命中。
	_, response = harness.get(t, "/api/characters/1/quests/search?q=%E7%A6%BB%E5%88%AB%E6%97%B6%E5%88%BB",
		map[string]string{"id": "1"}, harness.server.handleQuestSearch)
	requireOK(t, http.StatusOK, response)
	found := false
	for _, entry := range responseList(t, response, "results") {
		if entry.(map[string]any)["questId"] == float64(3971) {
			found = true
		}
	}
	if !found {
		t.Fatal("searching the real quest name 离别时刻 must return quest 3971")
	}

	// 总览：3970/3971 已完成；名字来自 PVF。
	_, response = harness.get(t, "/api/characters/1/quests", map[string]string{"id": "1"}, harness.server.handleQuestOverview)
	requireOK(t, http.StatusOK, response)
	completed := responseList(t, response, "completed")
	if len(completed) != 2 {
		t.Fatalf("completed = %d rows, want 2", len(completed))
	}
	// 总览按 questID 升序：3970 在前、3971 在后，两行名字都来自 PVF。
	if completed[0].(map[string]any)["questId"] != float64(3970) ||
		completed[1].(map[string]any)["questId"] != float64(3971) {
		t.Fatalf("completed ids = %v / %v, want 3970 / 3971",
			completed[0].(map[string]any)["questId"], completed[1].(map[string]any)["questId"])
	}
	if completed[0].(map[string]any)["name"] != "格拉古尔和诺尔妮" {
		t.Fatalf("completed[0] = %+v, want the PVF [name] of quest 3970", completed[0])
	}
	if completed[1].(map[string]any)["name"] != "离别时刻" {
		t.Fatalf("completed[1] = %+v, want quest 3971 离别时刻", completed[1])
	}
}
