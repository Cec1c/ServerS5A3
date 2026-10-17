package quest

import (
	"context"
	"errors"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func awakeningPreflightCatalog(t *testing.T) *Catalog {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func awakeningListed(catalog *Catalog, character CharacterEligibility, record dnfrepo.QuestRecord, questID int64) bool {
	for _, id := range catalog.QuestList(character, record).IDs {
		if int64(id) == questID {
			return true
		}
	}
	return false
}

// TestRealPVFAwakeningQuestRefusedForWrongAwakeningStage 是 DOC-126 的觉醒家族回归：
// 真档觉醒任务的 [grow type] 只钉第一转职分支、不钉觉醒阶段，先完成一条第一次觉醒链后
// 再回头接另一条（或二次觉醒阶段不足）会被 PlanTransition 以 target != awakening+1 拒绝，
// 形成永远做不完的 active 任务。接取预检与可接列表投影必须与结算前置一致，同时保留
// 合法的第一次/第二次觉醒链。
func TestRealPVFAwakeningQuestRefusedForWrongAwakeningStage(t *testing.T) {
	catalog := awakeningPreflightCatalog(t)
	cases := []struct {
		quest     int64
		label     string
		level     int
		pre       map[int64]string
		legitGrow []int
		badGrow   []int
	}{
		{3368, "northmyre 剑魂一觉", 50, map[int64]string{3356: "completed"}, []int{0x01}, []int{0x11, 0x21}},
		{3369, "northmyre 鬼泣一觉", 50, map[int64]string{3356: "completed"}, []int{0x02}, []int{0x12, 0x22}},
		{2698, "虚空行者 (4/4)", 50, map[int64]string{2695: "completed", 2696: "completed", 2697: "completed"}, []int{0x05}, []int{0x15}},
		{4929, "剑神 (6/6) 二觉", 75, map[int64]string{5146: "completed"}, []int{0x11}, []int{0x01, 0x21}},
		{4933, "黑暗君主 (6/6) 二觉", 75, map[int64]string{5300: "completed"}, []int{0x12}, []int{0x22}},
	}
	for _, tc := range cases {
		definition, found := catalog.Find(tc.quest)
		if !found {
			t.Fatalf("quest %d missing from the real PVF catalog", tc.quest)
		}
		if rewardType := normalizeQuestTag(definition.RewardType); rewardType != "awakening type" {
			t.Fatalf("quest %d reward type=%q, want awakening type", tc.quest, definition.RewardType)
		}
		states := map[int64]dnfrepo.QuestState{}
		for id, status := range tc.pre {
			states[id] = dnfrepo.QuestState{Status: status}
		}
		record := dnfrepo.QuestRecord{CharacterID: "1", States: states}
		job := 0
		if definition.Job != "" {
			if tag := normalizeQuestTag(definition.Job); tag != "swordman" {
				t.Fatalf("quest %d job=%q, unexpected for this regression", tc.quest, definition.Job)
			}
		} else if len(definition.TargetCharacterRules) > 0 {
			switch normalizeQuestTag(definition.TargetCharacterRules[0].JobTag) {
			case "at mage":
				job = 8
			case "swordman":
				job = 0
			default:
				t.Fatalf("quest %d target rule=%+v, unexpected", tc.quest, definition.TargetCharacterRules)
			}
		}
		for _, grow := range tc.legitGrow {
			character := CharacterEligibility{Level: tc.level, Job: job, GrowType: grow}
			decision, err := catalog.DecideAccept(character, record, tc.quest)
			if err != nil || decision != AcceptNew || !awakeningListed(catalog, character, record, tc.quest) {
				t.Fatalf("%s quest=%d grow=0x%02x must keep the legitimate awakening chain: decision=%v err=%v listed=%v",
					tc.label, tc.quest, grow, decision, err, awakeningListed(catalog, character, record, tc.quest))
			}
		}
		for _, grow := range tc.badGrow {
			character := CharacterEligibility{Level: tc.level, Job: job, GrowType: grow}
			decision, err := catalog.DecideAccept(character, record, tc.quest)
			if decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
				t.Fatalf("%s quest=%d grow=0x%02x decision=%v err=%v, want rejected/ErrQuestNotAcceptable",
					tc.label, tc.quest, grow, decision, err)
			}
			if awakeningListed(catalog, character, record, tc.quest) {
				t.Fatalf("%s quest=%d grow=0x%02x must not be projected as available", tc.label, tc.quest, grow)
			}
		}
	}
}

// TestRealPVFAwakeningAcceptEntryRefusesWrongStage 走真正的接取入口：觉醒阶段不匹配时被拒且
// 零写入；合法的第一次觉醒对照正常落库。
func TestRealPVFAwakeningAcceptEntryRefusesWrongStage(t *testing.T) {
	catalog := awakeningPreflightCatalog(t)
	ctx := context.Background()
	seed := func(t *testing.T, grow int) dnfrepo.Group {
		t.Helper()
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: "19", AccountID: "acc", Job: "0", Level: 50,
			Stats: map[string]int64{"grow_type": int64(grow)},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
			3356: {Status: "completed"},
		}}); err != nil {
			t.Fatal(err)
		}
		return repos
	}
	repos := seed(t, 0x11)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, acceptErr := owner.ApplyAccept(ctx, catalog, CharacterEligibility{Level: 50, Job: 0, GrowType: 0x11},
		NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: 3368})); !errors.Is(acceptErr, ErrQuestNotAcceptable) {
		t.Fatalf("already-awakened character ApplyAccept err=%v, want ErrQuestNotAcceptable", acceptErr)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		if _, ok := stored.States[3368]; ok {
			t.Fatalf("wrong-stage awakening quest was written: %+v", stored.States[3368])
		}
	}

	repos = seed(t, 0x01)
	owner, err = NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, acceptErr := owner.ApplyAccept(ctx, catalog, CharacterEligibility{Level: 50, Job: 0, GrowType: 0x01},
		NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: 3368}))
	if acceptErr != nil || result.QuestID != 3368 || result.Idempotent {
		t.Fatalf("legitimate first awakening must accept: result=%+v err=%v", result, acceptErr)
	}
	stored, found, err = repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest record found=%t err=%v", found, err)
	}
	if state := stored.States[3368]; !isActiveQuestStatus(state.Status) {
		t.Fatalf("legitimate first awakening state=%+v, want active", state)
	}
}

// TestRealPVFStuckAwakeningQuestStaysVisibleAndAbandonable 固定「已有 active 可见/放弃保持」：
// 修复只阻止新接取，存量错阶段 active 行仍必须在可接投影里且可正常放弃。
func TestRealPVFStuckAwakeningQuestStaysVisibleAndAbandonable(t *testing.T) {
	catalog := awakeningPreflightCatalog(t)
	ctx := context.Background()
	states := map[int64]dnfrepo.QuestState{
		3356: {Status: "completed"},
		3368: {Status: "active", ProgressValue: 1},
	}
	record := dnfrepo.QuestRecord{CharacterID: "19", States: states}
	character := CharacterEligibility{Level: 50, Job: 0, GrowType: 0x11}
	if !awakeningListed(catalog, character, record, 3368) {
		t.Fatalf("an existing active awakening quest must stay in the available projection for the tracker")
	}
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19", AccountID: "acc", Job: "0", Level: 50,
		Stats: map[string]int64{"grow_type": 0x11},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyGiveUp(ctx, catalog, Command{
		Operation:           "giveup_quest",
		AccountID:           "acc",
		SelectedCharacterID: 19,
		QuestID:             3368,
	}); err != nil {
		t.Fatalf("an existing active awakening quest must stay abandonable: %v", err)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest record found=%t err=%v", found, err)
	}
	if _, ok := stored.States[3368]; ok {
		t.Fatalf("give-up left the wrong-stage awakening row behind: %+v", stored.States[3368])
	}
}

// The reported missing-task symptom is not established as an awakening-stage
// bug. Preserve its legitimate entry while rejecting impossible reward stages.
func TestRealPVFVoidWalkerAwakeningEntryRemainsAvailable(t *testing.T) {
	catalog := awakeningPreflightCatalog(t)
	character := CharacterEligibility{Level: 50, Job: 8, GrowType: 5}
	record := dnfrepo.QuestRecord{CharacterID: "94", States: map[int64]dnfrepo.QuestState{
		3427: {Status: "completed"},
	}}
	decision, err := catalog.DecideAccept(character, record, 2695)
	if err != nil || decision != AcceptNew || !awakeningListed(catalog, character, record, 2695) {
		t.Fatalf("legitimate void-walker entry changed: decision=%v err=%v listed=%v", decision, err, awakeningListed(catalog, character, record, 2695))
	}
}
