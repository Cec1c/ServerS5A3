package quest

import (
	"context"
	"errors"
	"os"
	"testing"

	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func jobChangePreflightCatalog(t *testing.T) *Catalog {
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

func jobChangePreflightRecord() dnfrepo.QuestRecord {
	return dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{
		3151: {Status: "completed"},
		3152: {Status: "completed"},
		3153: {Status: "completed"},
		3155: {Status: "completed"},
	}}
}

func questListed(catalog *Catalog, character CharacterEligibility, record dnfrepo.QuestRecord, questID int64) bool {
	for _, id := range catalog.QuestList(character, record).IDs {
		if int64(id) == questID {
			return true
		}
	}
	return false
}

// TestRealPVFJobChangeQuestRefusedForChangedCharacter 覆盖 DOC-124 的 阿修罗(7814) 与
// 魔枪士(2633/2634/5761/5762)：已转职或已觉醒的角色不得再接取这些任务——它们的 PVF 只有
// [job] 与 [grow type] -1，没有转职阶段门槛，接了之后 PlanTransition 必然以
// ErrTransitionOutOfStep 拒绝，形成永远做不完的 active 任务。grow 0 的合法链路保持可接，
// 可接列表投影（op21 来源）与接取预检必须一致。
func TestRealPVFJobChangeQuestRefusedForChangedCharacter(t *testing.T) {
	catalog := jobChangePreflightCatalog(t)
	record := jobChangePreflightRecord()
	cases := []struct {
		quest int64
		job   int
		label string
	}{
		{7814, 0, "阿修罗"},
		{2633, 13, "决战者"},
		{2634, 13, "征战者"},
		{5761, 13, "狩猎者"},
		{5762, 13, "暗枪士"},
	}
	for _, tc := range cases {
		definition, found := catalog.Find(tc.quest)
		if !found {
			t.Fatalf("quest %d missing from the real PVF catalog", tc.quest)
		}
		if grade := normalizeQuestTag(definition.RewardType); grade != "grow type" {
			t.Fatalf("quest %d reward type=%q, want grow type", tc.quest, definition.RewardType)
		}
		for _, grow := range []int{0, 1, 2, 3, 4, 0x11} {
			character := CharacterEligibility{Level: 15, Job: tc.job, GrowType: grow}
			decision, err := catalog.DecideAccept(character, record, tc.quest)
			listed := questListed(catalog, character, record, tc.quest)
			if grow == 0 {
				if decision != AcceptNew || err != nil || !listed {
					t.Fatalf("%s quest=%d grow=0 must keep the legitimate chain: decision=%v err=%v listed=%v",
						tc.label, tc.quest, decision, err, listed)
				}
				continue
			}
			if decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
				t.Fatalf("%s quest=%d grow=0x%02x decision=%v err=%v, want rejected/ErrQuestNotAcceptable",
					tc.label, tc.quest, grow, decision, err)
			}
			if listed {
				t.Fatalf("%s quest=%d grow=0x%02x must not be projected as available", tc.label, tc.quest, grow)
			}
		}
	}
}

// TestRealPVFJobChangeAcceptEntryRefusesChangedCharacter 走真正的接取入口
// (Owner.ApplyAccept)：已转职/已觉醒角色被拒且零写入；grow 0 对照正常接取并落库。
func TestRealPVFJobChangeAcceptEntryRefusesChangedCharacter(t *testing.T) {
	catalog := jobChangePreflightCatalog(t)
	ctx := context.Background()
	seed := func(t *testing.T, grow int) dnfrepo.Group {
		t.Helper()
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: "19", AccountID: "acc", Job: "0", Level: 15,
			Stats: map[string]int64{"grow_type": int64(grow)},
		}); err != nil {
			t.Fatal(err)
		}
		record := jobChangePreflightRecord()
		record.CharacterID = "19"
		if err := repos.Quest.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		return repos
	}
	for _, grow := range []int{1, 0x11} {
		repos := seed(t, grow)
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		_, acceptErr := owner.ApplyAccept(ctx, catalog, CharacterEligibility{Level: 15, Job: 0, GrowType: grow},
			NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: 7814}))
		if !errors.Is(acceptErr, ErrQuestNotAcceptable) {
			t.Fatalf("grow=0x%02x ApplyAccept err=%v, want ErrQuestNotAcceptable", grow, acceptErr)
		}
		stored, found, err := repos.Quest.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if _, ok := stored.States[7814]; ok {
				t.Fatalf("grow=0x%02x wrote a row for a quest the character can never finish", grow)
			}
		}
	}

	repos := seed(t, 0)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, acceptErr := owner.ApplyAccept(ctx, catalog, CharacterEligibility{Level: 15, Job: 0, GrowType: 0},
		NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: 7814}))
	if acceptErr != nil || result.QuestID != 7814 || result.Idempotent {
		t.Fatalf("grow=0 control must accept 7814: result=%+v err=%v", result, acceptErr)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest record found=%t err=%v", found, err)
	}
	if state := stored.States[7814]; !isActiveQuestStatus(state.Status) {
		t.Fatalf("grow=0 control state=%+v, want active", state)
	}
}

// TestRealPVFAwakeningQuestStaysAcceptableAfterClassChange 固定觉醒行为不受本改动影响：
// 已转职未觉醒(0x01)的角色仍可接取剑圣觉醒(4/4) 977，且计划侧仍产出 awakening 请求。
func TestRealPVFAwakeningQuestStaysAcceptableAfterClassChange(t *testing.T) {
	catalog := jobChangePreflightCatalog(t)
	definition, found := catalog.Find(977)
	if !found {
		t.Fatalf("awakening quest 977 missing")
	}
	if rewardType := normalizeQuestTag(definition.RewardType); rewardType != "awakening type" {
		t.Fatalf("quest 977 reward type=%q, want awakening type", definition.RewardType)
	}
	record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{
		976: {Status: "completed"},
	}}
	character := CharacterEligibility{Level: 50, Job: 0, GrowType: 0x01}
	if decision, err := catalog.DecideAccept(character, record, 977); err != nil || decision != AcceptNew {
		t.Fatalf("awakening quest 977 must stay acceptable at grow 0x01: decision=%v err=%v (job=%q target=%v levelMin=%d)",
			decision, err, definition.Job, definition.TargetCharacterRules, definition.LevelMin)
	}
	if !questListed(catalog, character, record, 977) {
		t.Fatalf("awakening quest 977 must stay in the available projection")
	}
	plan, planErr := catalog.PlanFinishReward(character, 977, 0, false)
	if planErr != nil || !plan.HasProfession || plan.ProfessionRequest.Kind != dnfprofession.KindAwakening {
		t.Fatalf("awakening plan=%+v err=%v, want an awakening request", plan, planErr)
	}
}

// TestRealPVFStuckJobChangeQuestCanBeAbandoned 回答「存量已经 active 的此类任务还能不能
// 放弃」：本修复只阻止新接取，存量行必须仍可正常放弃（否则就是把仅阻止新接误报成全修）。
func TestRealPVFStuckJobChangeQuestCanBeAbandoned(t *testing.T) {
	catalog := jobChangePreflightCatalog(t)
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19", AccountID: "acc", Job: "0", Level: 15,
		Stats: map[string]int64{"grow_type": 1},
	}); err != nil {
		t.Fatal(err)
	}
	record := jobChangePreflightRecord()
	record.CharacterID = "19"
	record.States[7814] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
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
		QuestID:             7814,
	}); err != nil {
		t.Fatalf("an existing active job-change quest must stay abandonable: %v", err)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest record found=%t err=%v", found, err)
	}
	if _, ok := stored.States[7814]; ok {
		t.Fatalf("give-up left the active job-change row behind: %+v", stored.States[7814])
	}
}
