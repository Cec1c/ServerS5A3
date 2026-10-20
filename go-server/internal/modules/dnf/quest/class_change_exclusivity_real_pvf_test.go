package quest

import (
	"context"
	"errors"
	"testing"

	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// DOC-124（p131「魔枪士、鬼剑士(阿修罗) 转职和主线任务反过来了 主线任务不能完成」）
// 的第三条现场形态：同一个转职家族在 PVF 里互相声明了 [collision quest]，而接取预检过去
// 只看 completed 的 collision，所以 15 级角色可以同时接取 7803/7807/7810/7814（鬼剑士）、
// 2633/2634/5761/5762（魔枪士），以及 15 级分支主线 6931..6934 / 6992..6995。
// 一次转职只能结算一次，第二条必然被 ErrTransitionOutOfStep 拒绝，于是变成永远做不完的
// active 任务；玩家看到的就是「转职任务做不完 / 主线任务不能完成」。
//
// 本用例在旧实现上是红的（第二条 DecideAccept=AcceptNew，且仍在 op21 可接列表里），修复后绿。
// 用例自带 PVF 事实判据（probe* 系列），不依赖被测代码里的新助手，因此旧实现下也能编译并直接
// 在行为断言上变红。
func TestRealPVFClassChangeQuestRefusedWhileSiblingActive(t *testing.T) {
	catalog := level15BranchRealPVFCatalog(t)
	eligibility := CharacterEligibility{Level: 15, Job: 0, GrowType: 0}

	// 判据交集 1：PVF 自己声明的双向 [collision quest]，且两边都是 class change 链。
	for _, job := range []int{0, 13} {
		family := level15JobChangeQuestIDs(catalog, job)
		if len(family) != 4 {
			t.Fatalf("job=%d transfer family=%v, want the four real-PVF variants", job, family)
		}
		for _, id := range family {
			definition, _ := catalog.Find(id)
			if !probeClassChange(definition) {
				t.Fatalf("job=%d quest=%d is not a class-change chain", job, id)
			}
			for _, other := range family {
				if id == other {
					continue
				}
				partner, _ := catalog.Find(other)
				if !probeMutualCollision(definition, partner) {
					t.Fatalf("job=%d quest %d and %d must declare each other as [collision quest]", job, id, other)
				}
			}
		}
	}

	// 15 级主线 + 3155（赛丽亚的决心）已完成：四条鬼剑士转职任务同时可接。
	unlocked := level15BranchRecord(3151, 3152, 3153, level15BranchUnlockID)
	for _, id := range []int64{7803, 7807, 7810, 7814} {
		if decision, err := catalog.DecideAccept(eligibility, unlocked, id); err != nil || decision != AcceptNew {
			t.Fatalf("quest %d must be acceptable on a fresh record: decision=%v err=%v", id, decision, err)
		}
	}

	// 已经持有 7814（转职 - 阿修罗）时，其余三条不能再接，也不能再出现在 op21 投影里
	// （bridge 的 scene_quest_list 用的就是 QuestList；能看不能接是同一种缺陷）。
	held := level15BranchRecord(3151, 3152, 3153, level15BranchUnlockID)
	held.States[7814] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	for _, id := range []int64{7803, 7807, 7810} {
		decision, err := catalog.DecideAccept(eligibility, held, id)
		if decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
			t.Fatalf("quest %d decision=%v err=%v, want rejected/ErrQuestNotAcceptable", id, decision, err)
		}
		if questListed(catalog, eligibility, held, id) {
			t.Fatalf("quest %d must not be projected as acceptable while 7814 is unsettled", id)
		}
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, held)

	// 拒绝的依据是结算规则本身，不是新发明的条件。
	request, parseErr := dnfprofession.ParseReward(1, "[grow type]", []int64{4})
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if preconditionErr := dnfprofession.CheckClassChangePrecondition(0x04, request); !errors.Is(preconditionErr, dnfprofession.ErrTransitionOutOfStep) {
		t.Fatalf("the sibling quest settlement rule must be the reason: %v", preconditionErr)
	}

	// 已经 active 的那条仍留在快照与可接列表里：玩家还能放弃或完成它。
	if !questListed(catalog, eligibility, held, 7814) {
		t.Fatal("the already active class-change quest must stay visible")
	}
	if decision, err := catalog.DecideAccept(eligibility, held, 7814); err != nil || decision != AcceptIdempotent {
		t.Fatalf("re-accepting the held quest must stay idempotent: decision=%v err=%v", decision, err)
	}

	// 15 级分支主线同样是转职链（[epic] + [grow type]）：持有 6934（阿修罗）时
	// 不能再接 6933（狂战士），否则这条主线任务永远做不完。
	branch := level15BranchRecord(3151, 3152, 3153)
	branch.States[6934] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	if decision, err := catalog.DecideAccept(eligibility, branch, 6933); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("branch quest 6933 decision=%v err=%v, want rejected while 6934 is unsettled", decision, err)
	}

	// 原断言把 [reward type] [item] 当作“非转职、可与 grow type 分支共存”的依据。
	// a56 的 6930/6986 同样带 [job change quest] 1、同一基础职业和 3153 前置，
	// 只是奖励为占位 item；奖励类型区分是否改职业，不能区分初始转职家族。
	// 因此同职业 active 后仍应拒绝 6930，避免两代任务并存。
	if decision, err := catalog.DecideAccept(eligibility, branch, 6930); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("the [item] branch 6930 must be refused while 6934 is active: decision=%v err=%v", decision, err)
	}
	// 范围边界（不扩大）：别的职业/家族的历史 active 行不能锁住本职业的转职任务。
	foreign := level15BranchRecord(3151, 3152, 3153, level15BranchUnlockID)
	foreign.States[2633] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	if decision, err := catalog.DecideAccept(eligibility, foreign, 7803); err != nil || decision != AcceptNew {
		t.Fatalf("a foreign-family active row must not lock quest 7803: decision=%v err=%v", decision, err)
	}
	// 范围边界（不扩大）：普通主线节点不受转职互斥判定影响。
	ordinary := level15BranchRecord(3151, 3152, 3153, level15BranchUnlockID)
	ordinary.States[7814] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	if decision, err := catalog.DecideAccept(CharacterEligibility{Level: 20, Job: 0, GrowType: 0}, ordinary, 3156); err != nil || decision != AcceptNew {
		t.Fatalf("ordinary mainline quest 3156 must stay acceptable: decision=%v err=%v", decision, err)
	}

	// 反例：已经转职（first!=0）的角色本来就被既有判据拒绝，新规则不改变这一点，
	// 而且它那条 active 的历史任务行仍然可见、仍然可以放弃。
	changed := level15BranchRecord(3151, 3152, 3153, level15BranchUnlockID)
	changed.States[7814] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	changedEligibility := CharacterEligibility{Level: 15, Job: 0, GrowType: 0x04}
	if decision, err := catalog.DecideAccept(changedEligibility, changed, 7803); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("changed character decision=%v err=%v, want rejected", decision, err)
	}
	if !questListed(catalog, changedEligibility, changed, 7814) {
		t.Fatal("the historical active row of a changed character must stay visible")
	}
	if decision, err := catalog.DecideAccept(changedEligibility, level15BranchRecord(3151, 3152, 3153, level15BranchUnlockID), 7803); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("changed character without any active row decision=%v err=%v, want rejected", decision, err)
	}
}

// probeClassChange 只用 PVF 字段与 profession 域规则判断一条任务是不是 class change 链，
// 不依赖被测实现，保证用例在旧源码上也能编译。
func probeClassChange(definition Definition) bool {
	if normalizeQuestTag(definition.RewardType) != "grow type" {
		return false
	}
	request, err := dnfprofession.ParseReward(definition.JobChangeQuest, definition.RewardType, definition.RewardIntData)
	return err == nil && request.Kind == dnfprofession.KindClassChange
}

// probeMutualCollision 只读 PVF 的 [collision quest] 双向声明。
func probeMutualCollision(left, right Definition) bool {
	return probeDeclaresCollision(left, right.ID) && probeDeclaresCollision(right, left.ID)
}

func probeDeclaresCollision(definition Definition, otherID int64) bool {
	for _, id := range definition.CollisionQuests {
		if id == otherID {
			return true
		}
	}
	return false
}

// TestRealPVFClassChangeSiblingAcceptEntryRefuses 走真正的接取入口 (Owner.ApplyAccept)：
// 已经持有一条转职任务时第二条被拒且零写入；持有的那条不被改动，仍然可以放弃。
func TestRealPVFClassChangeSiblingAcceptEntryRefuses(t *testing.T) {
	catalog := level15BranchRealPVFCatalog(t)
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19", AccountID: "acc", Job: "0", Level: 15,
		Stats: map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	record := level15BranchRecord(3151, 3152, 3153, level15BranchUnlockID)
	record.CharacterID = "19"
	record.States[7814] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	_, acceptErr := owner.ApplyAccept(ctx, catalog, CharacterEligibility{Level: 15, Job: 0, GrowType: 0},
		NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: 7810}))
	if !errors.Is(acceptErr, ErrQuestNotAcceptable) {
		t.Fatalf("ApplyAccept err=%v, want ErrQuestNotAcceptable", acceptErr)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest record found=%t err=%v", found, err)
	}
	if _, ok := stored.States[7810]; ok {
		t.Fatal("the refused accept wrote a row for the conflicting class-change quest")
	}
	if state := stored.States[7814]; !isActiveQuestStatus(state.Status) {
		t.Fatalf("the held quest row changed: %+v", state)
	}
	// 持有的那条仍然可以放弃：新规则只阻止新接取，不把存量行锁死。
	if _, err := owner.ApplyGiveUp(ctx, catalog, Command{
		Operation:           "giveup_quest",
		AccountID:           "acc",
		SelectedCharacterID: 19,
		QuestID:             7814,
	}); err != nil {
		t.Fatalf("the held class-change quest must stay abandonable: %v", err)
	}
	after, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload after give-up found=%t err=%v", found, err)
	}
	if _, ok := after.States[7814]; ok {
		t.Fatal("give-up left the held class-change row behind")
	}
}
