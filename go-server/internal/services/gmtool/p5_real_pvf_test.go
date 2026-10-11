package gmtool

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P5 真档 Script.pvf 上的端到端取证（需要 DNFBRIDGE_REAL_PVF_SMOKE 指向 Script.pvf）。
//
// 两个问题只能在真档上回答，假 PVF 答不了：
//  1. 接取路径真的能复用本仓 owner 在**真任务目录**上落库吗（J9-a）；
//  2. 职业任务门是不是死代码 —— 真档里到底有没有 `[reward type]` 为
//     `[grow type]` / `[awakening type]` 的任务（J10-b 的开关只对它们有意义）。

func TestRealPVFQuestAcceptUsesTheForkOwnerOnRealCatalog(t *testing.T) {
	source := loadRealPVFSource(t)
	harness := newQuestWriteHarness(t, questWriteFixture{
		pvf:       source,
		character: realPVFCharacter(),
	})

	catalog, err := harness.server.pvf.questCatalog(harness.server)
	if err != nil {
		t.Fatalf("load real quest catalog: %v", err)
	}
	record := harness.questRecord(t)
	eligibility := questEligibilityForTest(t, harness)
	available := catalog.QuestList(eligibility, record)
	if len(available.IDs) == 0 {
		t.Fatal("真档里这个角色没有任何可接任务，用例前提不成立")
	}
	questID := int64(available.IDs[0])
	definition, found := catalog.Find(questID)
	if !found {
		t.Fatalf("catalog 给出的可接任务 %d 找不到定义", questID)
	}
	t.Logf("real catalog: acceptable=%d first=%d name=%q path=%q",
		len(available.IDs), questID, definition.Name, definition.Path)

	_, response := harness.accept(t, questID)
	requireOK(t, http.StatusOK, response)
	quest := responseMap(t, response, "quest")
	if quest["active"] != true {
		t.Fatalf("真档接取后必须是 active: %+v", quest)
	}
	state, ok := harness.questState(t, questID)
	if !ok || state.Status != "active" {
		t.Fatalf("真档接取没有落库: %+v found=%v", state, ok)
	}
	// owner 的标记证明走的是 dnfquest.Owner（而不是面板自己写一行）。
	if state.Extra["pvf_path"] != definition.Path {
		t.Fatalf("owner 的 pvf_path = %q, want %q", state.Extra["pvf_path"], definition.Path)
	}
}

func TestRealPVFProfessionQuestCompleteRespectsTheWriteSwitch(t *testing.T) {
	source := loadRealPVFSource(t)
	harness := newQuestWriteHarness(t, questWriteFixture{
		pvf:       source,
		character: realPVFCharacter(),
		skills:    &dnfrepo.SkillRecord{Points: dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100}},
	})
	catalog, err := harness.server.pvf.questCatalog(harness.server)
	if err != nil {
		t.Fatalf("load real quest catalog: %v", err)
	}

	// 真档里必须有职业任务，否则 J10-b 的开关是死代码（如实记录数量）。
	// 目录是 map，按 id 全量扫一遍（65535 次 Find 是常数级开销）比翻 Search 的
	// 分页更可靠 —— Search 的 limit 上限是 200，翻不完 2996 条。
	professionIDs := make([]int64, 0, 8)
	for questID := int64(1); questID <= math.MaxUint16; questID++ {
		definition, found := catalog.Find(questID)
		if !found {
			continue
		}
		if IsProfessionQuestReward(definition.RewardType) {
			professionIDs = append(professionIDs, definition.ID)
		}
	}
	if len(professionIDs) == 0 {
		t.Fatal("真档任务目录里没有任何 [grow type]/[awakening type] 任务：职业任务门无从验证")
	}
	t.Logf("real catalog: profession quests = %d, first few = %v", len(professionIDs), professionIDs[:min(8, len(professionIDs))])

	professionID := professionIDs[0]
	before := harness.questFingerprint(t)

	// 1) 默认档（开关未设）= 拒绝，且零写入。
	_, response := harness.complete(t, professionID)
	requireFailure(t, http.StatusOK, response)
	if response["reason"] != "profession_write_disabled" {
		t.Fatalf("真档职业任务默认必须被拒: %+v", response)
	}
	harness.requireZeroWrite(t, before, "真档职业任务默认档")

	// 2) 开关 on：门必须打开。往后是否真的能走完职业跃迁取决于这份角色状态与
	//    PVF 职业表是否匹配 —— 两种结果都接受，但**绝不允许**再报"默认关闭"，
	//    也不允许失败时留下任何字节。
	t.Setenv(questProfessionWriteEnv, "on")
	_, response = harness.complete(t, professionID)
	if response["reason"] == "profession_write_disabled" {
		t.Fatalf("开关 on 后不得再报默认关闭: %+v", response)
	}
	if response["success"] == true {
		if response["professionChanged"] != true {
			t.Fatalf("职业任务完成必须回报 professionChanged: %+v", response)
		}
		state, ok := harness.questState(t, professionID)
		if !ok || state.Status != questCompletedLiteral {
			t.Fatalf("职业任务完成后状态 = %+v found=%v", state, ok)
		}
		if state.Extra[questRewardsGrantedKey] != "false" {
			t.Fatalf("GM 强制完成不得发奖励: %+v", state.Extra)
		}
		t.Logf("real profession transition applied: quest=%d grow_type=%v", professionID, response["growType"])
		return
	}
	harness.requireZeroWrite(t, before, "真档职业任务开关 on 但被 PVF/状态拒绝")
	onMessage, _ := response["error"].(string)
	t.Logf("real profession transition refused (zero writes): %q", onMessage)
}

// TestRealPVFProfessionClassChangeCommitsAllAggregates 覆盖职业分支的**成功路径**：
// 在真档里挑一个 `[grow type]` 转职任务 + 一个能容纳它的职业，让
// ParseReward -> PlanTransition -> ApplySkillTransition -> applyLevelStats ->
// 三聚合同事务写入 全部真的跑一遍。
//
// grow_type=0 是转职的合法前置（PlanTransition 要求 first==0 && awakening==0），
// 所以这里用"没转职过"的角色，而不是上面那个 0x21 的二觉角色。
func TestRealPVFProfessionClassChangeCommitsAllAggregates(t *testing.T) {
	source := loadRealPVFSource(t)
	harness := newQuestWriteHarness(t, questWriteFixture{
		pvf:       source,
		character: realPVFCharacter(),
		skills:    &dnfrepo.SkillRecord{Points: dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100}},
	})
	profiles, skillCatalog, err := harness.server.pvf.professionResources(harness.server)
	if err != nil {
		t.Fatalf("load real profession resources: %v", err)
	}
	catalog, err := harness.server.pvf.questCatalog(harness.server)
	if err != nil {
		t.Fatalf("load real quest catalog: %v", err)
	}

	// 找一个"存在某个 job 能承接"的转职任务。
	var (
		classQuestID int64
		classJob     byte
		classGrow    byte
	)
	for questID := int64(1); questID <= math.MaxUint16 && classQuestID == 0; questID++ {
		definition, found := catalog.Find(questID)
		if !found || !IsProfessionQuestReward(definition.RewardType) {
			continue
		}
		request, parseErr := dnfprofession.ParseReward(
			definition.JobChangeQuest, definition.RewardType, definition.RewardIntData)
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
	if classQuestID == 0 {
		t.Skip("真档里找不到任何可用 grow_type=0 承接的转职任务；成功路径未验证")
	}
	t.Logf("real class-change candidate: quest=%d job=%d -> grow_type=%d", classQuestID, classJob, classGrow)

	// 换一份匹配的角色夹具（job=classJob、grow_type=0、带技能账）。
	harness = newQuestWriteHarness(t, questWriteFixture{
		pvf: source,
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: strconv.Itoa(int(classJob)), Level: 90,
			Stats: map[string]int64{"grow_type": 0},
		},
		skills: &dnfrepo.SkillRecord{Points: dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100}},
	})
	t.Setenv(questProfessionWriteEnv, "on")
	before := harness.questFingerprint(t)

	_, response := harness.complete(t, classQuestID)
	if response["success"] != true {
		message, _ := response["error"].(string)
		harness.requireZeroWrite(t, before, "真档转职失败路径")
		// 失败也必须是"有理由的失败 + 零写入"，绝不能是静默成功。
		t.Fatalf("真档转职完成失败（已确认零写入）: %q", message)
	}
	if response["professionChanged"] != true || response["rewardGranted"] != false {
		t.Fatalf("职业跃迁结果字段不对: %+v", response)
	}
	character, found, err := harness.group.Character.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("reload character: found=%v err=%v", found, err)
	}
	if int64(growTypeValue(character)) != int64(classGrow) {
		t.Fatalf("角色 grow_type = %d, want %d", growTypeValue(character), classGrow)
	}
	state, ok := harness.questState(t, classQuestID)
	if !ok || state.Status != questCompletedLiteral || state.Extra[questRewardsGrantedKey] != "false" {
		t.Fatalf("任务行 = %+v found=%v", state, ok)
	}
	if _, found, err := harness.group.Skill.Load(context.Background(), "1"); err != nil || !found {
		t.Fatalf("技能账必须仍在（同事务写入）: found=%v err=%v", found, err)
	}
	_ = skillCatalog
}

// questEligibilityForTest 复刻 questReadContext 的资格投影，供真档用例选择可接任务。
func questEligibilityForTest(t *testing.T, harness *questWriteHarness) dnfquest.CharacterEligibility {
	t.Helper()
	character, found, err := harness.group.Character.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%v err=%v", found, err)
	}
	job, err := strconv.Atoi(strings.TrimSpace(character.Job))
	if err != nil {
		t.Fatalf("stored character job %q: %v", character.Job, err)
	}
	return dnfquest.CharacterEligibility{
		Level:    character.Level,
		Job:      job,
		GrowType: int(growTypeValue(character)),
	}
}
