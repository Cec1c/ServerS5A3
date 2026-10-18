package quest

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The PVF-linked terminal step 3473 consumes three fragments and grants four.
// The client can finish parent 3458 without sending a separate child op34.
func TestOwnerTerminalChildItemSettlementPreservesNetReward(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}
	if _, err := finishOwnerAllocator(nil)(&inventory, FinishItemGrantRequest{ItemID: 10164800, Count: 4}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3458: {Status: "active", ProgressValue: 0},
		3472: {Status: "completed", ProgressValue: 0},
		3473: {Status: "active", ProgressValue: 0},
	}}); err != nil {
		t.Fatal(err)
	}
	parent := Definition{ID: 3458, Grade: "[epic]", Type: "[quest clear]", IntData: []int64{3472, 3473}, LevelMin: 1, LevelMax: 99, Difficulty: "N", RewardType: "[item]", RewardIntData: []int64{0, 0}, NoExperience: true}
	prior := Definition{ID: 3472, MainQuestID: 3458, Grade: "[sub]", Type: "[hunt monster]"}
	child := Definition{ID: 3473, MainQuestID: 3458, Grade: "[sub]", Type: "[seeking]", IntData: []int64{10164800, 3}, LevelMin: 1, LevelMax: 99, Difficulty: "N", RewardType: "[item]", RewardIntData: []int64{10164800, 4}, RewardItems: []RewardItemRule{{ItemID: 10164800, Count: 4}}, NoExperience: true}
	catalog := &Catalog{ordered: []Definition{parent, prior, child}, byID: map[int64]Definition{3458: parent, 3472: prior, 3473: child}}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := FinishCommitInput{AccountID: "acc", CharacterID: "19", QuestID: 3458, Multiplier: 1, CommittedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), Progression: finishOwnerProgression(t), AllocateItem: finishOwnerAllocator(nil)}
	result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	stored, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%v err=%v", found, err)
	}
	if got := stored.Slots["0:65"].Count; got != 5 {
		t.Fatalf("terminal child item settlement: fragments=%d want 5 (4 - 3 + 4); child=%+v", got, result.PostCommitQuest.States[3473])
	}
	if len(result.TerminalChildren) != 1 || result.TerminalChildren[0].QuestID != 3473 ||
		len(result.TerminalChildren[0].ConsumedItems) != 1 ||
		len(result.TerminalChildren[0].Items) != 1 {
		t.Fatalf("terminal child receipt attribution=%+v", result.TerminalChildren)
	}
	replayedResult, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatalf("parent replay: %v", err)
	}
	if !replayedResult.Replayed || len(replayedResult.TerminalChildren) != 1 ||
		len(replayedResult.TerminalChildren[0].ConsumedItems) != 1 ||
		len(replayedResult.TerminalChildren[0].Items) != 1 {
		t.Fatalf("terminal child replay attribution=%+v", replayedResult.TerminalChildren)
	}
	replayed, _, _ := repos.Inventory.Load(ctx, "19")
	if got := replayed.Slots["0:65"].Count; got != 5 {
		t.Fatalf("parent replay duplicated child reward: fragments=%d want 5", got)
	}
}

func TestOwnerTerminalChildSettlementRollsBackWhenRequiredItemsMissing(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildInventory(t, ctx, repos, 2)
	seedTerminalChildQuests(t, ctx, repos, "active")
	catalog := terminalChildRewardCatalog()
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	_, err = owner.ApplyFinishSettlement(ctx, catalog, terminalChildFinishInput(t, nil))
	if !errors.Is(err, ErrFinishRequiredItemsMissing) {
		t.Fatalf("terminal child missing-item error=%v", err)
	}
	assertTerminalChildRolledBack(t, ctx, repos, 2)
}

func TestOwnerTerminalChildSettlementRollsBackAllocatorFailure(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildInventory(t, ctx, repos, 4)
	seedTerminalChildQuests(t, ctx, repos, "active")
	catalog := terminalChildRewardCatalog()
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("terminal child grant failed")

	_, err = owner.ApplyFinishSettlement(ctx, catalog, terminalChildFinishInput(t, finishOwnerAllocator(wantErr)))
	if !errors.Is(err, wantErr) {
		t.Fatalf("terminal child allocator error=%v want=%v", err, wantErr)
	}
	assertTerminalChildRolledBack(t, ctx, repos, 4)
}

func TestOwnerTerminalChildSettlementDoesNotRewardIndependentlyCompletedChild(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildInventory(t, ctx, repos, 4)
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3458: {Status: "active", ProgressValue: 0},
		3472: {Status: "completed", ProgressValue: 0},
		3473: {Status: "completed", ProgressValue: 0, Extra: map[string]string{
			"completion_key": "independent-3473",
			"reward_state":   finishRewardGranted,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.ApplyFinishSettlement(ctx, terminalChildRewardCatalog(), terminalChildFinishInput(t, finishOwnerAllocator(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TerminalChildren) != 0 {
		t.Fatalf("completed child was settled again: %+v", result.TerminalChildren)
	}
	if state := result.PostCommitQuest.States[3473]; state.Extra["completion_key"] != "independent-3473" ||
		state.Extra["auto_completed_by_parent"] != "" {
		t.Fatalf("completed child state changed: %+v", state)
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "19")
	if got := inventory.Slots["0:65"].Count; got != 4 {
		t.Fatalf("completed child duplicated reward: fragments=%d want 4", got)
	}
}

func TestOwnerTerminalChildSettlementCompletesNoRewardChild(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildQuests(t, ctx, repos, "active")
	parent := Definition{ID: 3458, Grade: "[epic]", Type: "[quest clear]", IntData: []int64{3472, 3473}, LevelMin: 1, LevelMax: 99, Difficulty: "N"}
	prior := Definition{ID: 3472, MainQuestID: 3458, Grade: "[sub]", Type: "[hunt monster]"}
	child := Definition{ID: 3473, MainQuestID: 3458, Grade: "[sub]", Type: "[seek n meet npc]", NoExperience: true}
	catalog := &Catalog{ordered: []Definition{parent, prior, child}, byID: map[int64]Definition{3458: parent, 3472: prior, 3473: child}}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.ApplyFinishSettlement(ctx, catalog, terminalChildFinishInput(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TerminalChildren) != 1 || result.TerminalChildren[0].QuestID != 3473 ||
		len(result.TerminalChildren[0].Items) != 0 ||
		len(result.TerminalChildren[0].ConsumedItems) != 0 ||
		len(result.TerminalChildren[0].Currency) != 0 {
		t.Fatalf("no-reward terminal child settlement=%+v", result.TerminalChildren)
	}
	if state := result.PostCommitQuest.States[3473]; state.Status != "completed" ||
		state.Extra["reward_state"] != finishRewardGranted {
		t.Fatalf("no-reward terminal child state=%+v", state)
	}
}

func TestRealScriptPVFTerminalChild3458ConsumesThreeAndGrantsFour(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify terminal child 3473")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
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
	parent, parentKnown := catalog.Find(3458)
	prior, priorKnown := catalog.Find(3472)
	child, childKnown := catalog.Find(3473)
	if !parentKnown || !priorKnown || !childKnown ||
		normalizeQuestTag(parent.Type) != "quest clear" ||
		len(parent.IntData) != 2 || parent.IntData[0] != 3472 || parent.IntData[1] != 3473 ||
		prior.MainQuestID != 3458 || child.MainQuestID != 3458 ||
		normalizeQuestTag(child.Type) != "seeking" {
		t.Fatalf("real terminal child graph parent=%+v/%t prior=%+v/%t child=%+v/%t",
			parent, parentKnown, prior, priorKnown, child, childKnown)
	}
	if len(child.IntData) < 2 || child.IntData[0] != 10164800 || child.IntData[1] != 3 ||
		len(child.RewardItems) != 1 || child.RewardItems[0].ItemID != 10164800 ||
		child.RewardItems[0].Count != 4 {
		t.Fatalf("real terminal child settlement child=%+v", child)
	}
	// Use the loaded definitions in the owner transaction, not only as a schema check.
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildInventory(t, ctx, repos, 4)
	seedTerminalChildQuests(t, ctx, repos, "active")
	character, _, _ := repos.Character.Load(ctx, "19")
	character.Level = parent.LevelMin
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "19", Points: dnfrepo.SkillPointState{SyncedLevel: character.Level}}); err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	startExperience, err := tables.ThresholdToNext(character.Level - 1)
	if err != nil {
		t.Fatal(err)
	}
	character.Stats["exp"] = int64(startExperience)
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	input := terminalChildFinishInput(t, finishOwnerAllocator(nil))
	input.Progression = tables
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.PostCommitInventory.Slots["0:65"].Count; got != 5 {
		t.Fatalf("real definition fragments=%d want5", got)
	}
	if !child.NoExperience {
		t.Fatal("real child experience policy changed")
	}
	if _, err := owner.ApplyFinishSettlement(ctx, catalog, input); err != nil {
		t.Fatal(err)
	}
	// All actual linked child definitions in this asset carry NoExperience;
	// the parent owns progression while this change settles their assets.
	for _, linked := range catalog.ordered {
		parent, known := catalog.Find(linked.MainQuestID)
		if known && normalizeQuestTag(parent.Type) == "quest clear" && !linked.NoExperience {
			t.Fatalf("linked child %d now requires separate experience settlement", linked.ID)
		}
	}
}

func TestOwnerTerminalChildSettlementRejectsForeignParent(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildInventory(t, ctx, repos, 4)
	seedTerminalChildQuests(t, ctx, repos, "active")
	catalog := terminalChildRewardCatalog()
	child := catalog.byID[3473]
	child.MainQuestID = 9999
	catalog.byID[3473] = child
	owner, _ := NewOwner(repos)
	_, err := owner.ApplyFinishSettlement(ctx, catalog, terminalChildFinishInput(t, finishOwnerAllocator(nil)))
	if !errors.Is(err, ErrFinishQuestNotPending) {
		t.Fatalf("foreign parent accepted: %v", err)
	}
	assertTerminalChildRolledBack(t, ctx, repos, 4)
}

func TestOwnerTerminalChildSettlementKeepsBothSameSlotGrants(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildInventory(t, ctx, repos, 4)
	seedTerminalChildQuests(t, ctx, repos, "active")
	catalog := terminalChildRewardCatalog()
	parent := catalog.byID[3458]
	parent.RewardIntData = []int64{10164800, 2}
	parent.RewardItems = []RewardItemRule{{ItemID: 10164800, Count: 2}}
	parent.HasGoldReward = true
	catalog.byID[3458] = parent
	child := catalog.byID[3473]
	child.HasGoldReward = true
	catalog.byID[3473] = child
	owner, _ := NewOwner(repos)
	input := terminalChildFinishInput(t, finishOwnerAllocator(nil))
	result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Delta != 6 || result.Items[0].CountOrSeed != 6 || result.Items[0].PostCount != 7 {
		t.Fatalf("combined grants must retain both ACK quantities: %+v", result.Items)
	}
	if len(result.Currency) != 1 || result.Currency[0].Delta != 20 || result.Currency[0].PostValue != 20 {
		t.Fatalf("combined gold %+v", result.Currency)
	}
	replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil || !reflect.DeepEqual(result.Items, replay.Items) {
		t.Fatalf("replay %v %+v", err, replay.Items)
	}
}

func TestOwnerTerminalChildSettlementLateCommitRollback(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	seedTerminalChildInventory(t, ctx, repos, 4)
	seedTerminalChildQuests(t, ctx, repos, "active")
	characterBefore, _, _ := repos.Character.Load(ctx, "19")
	skillBefore, _, _ := repos.Skill.Load(ctx, "19")
	failed := repos
	failed.CharacterSettlement = antonPrerequisiteFailCommit{repos.CharacterSettlement}
	owner, _ := NewOwner(failed)
	result, err := owner.ApplyFinishSettlement(ctx, terminalChildRewardCatalog(), terminalChildFinishInput(t, finishOwnerAllocator(nil)))
	if err == nil || result.AtomicCommitted {
		t.Fatalf("late failure escaped: %v %+v", err, result)
	}
	assertTerminalChildRolledBack(t, ctx, repos, 4)
	characterAfter, _, _ := repos.Character.Load(ctx, "19")
	skillAfter, _, _ := repos.Skill.Load(ctx, "19")
	if !reflect.DeepEqual(characterBefore, characterAfter) || !reflect.DeepEqual(skillBefore, skillAfter) {
		t.Fatal("late failure changed character or skill")
	}
}

func terminalChildRewardCatalog() *Catalog {
	parent := Definition{ID: 3458, Grade: "[epic]", Type: "[quest clear]", IntData: []int64{3472, 3473}, LevelMin: 1, LevelMax: 99, Difficulty: "N", RewardType: "[item]", RewardIntData: []int64{0, 0}, NoExperience: true}
	prior := Definition{ID: 3472, MainQuestID: 3458, Grade: "[sub]", Type: "[hunt monster]"}
	child := Definition{ID: 3473, MainQuestID: 3458, Grade: "[sub]", Type: "[seeking]", Path: "n_quest/albert_request.qst", IntData: []int64{10164800, 3}, LevelMin: 1, LevelMax: 99, Difficulty: "N", RewardType: "[item]", RewardIntData: []int64{10164800, 4}, RewardItems: []RewardItemRule{{ItemID: 10164800, Count: 4}}, NoExperience: true}
	return &Catalog{ordered: []Definition{parent, prior, child}, byID: map[int64]Definition{3458: parent, 3472: prior, 3473: child}}
}

func terminalChildFinishInput(t *testing.T, allocator FinishItemAllocator) FinishCommitInput {
	t.Helper()
	return FinishCommitInput{
		AccountID: "acc", CharacterID: "19", QuestID: 3458, Multiplier: 1,
		CommittedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
		Progression: finishOwnerProgression(t), AllocateItem: allocator,
	}
}

func seedTerminalChildQuests(t *testing.T, ctx context.Context, repos dnfrepo.Group, childStatus string) {
	t.Helper()
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3458: {Status: "active", ProgressValue: 0},
		3472: {Status: "completed", ProgressValue: 0},
		3473: {Status: childStatus, ProgressValue: 0},
	}}); err != nil {
		t.Fatal(err)
	}
}

func seedTerminalChildInventory(t *testing.T, ctx context.Context, repos dnfrepo.Group, count int64) {
	t.Helper()
	inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}
	if _, err := finishOwnerAllocator(nil)(&inventory, FinishItemGrantRequest{ItemID: 10164800, Count: count}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
}

func assertTerminalChildRolledBack(t *testing.T, ctx context.Context, repos dnfrepo.Group, fragments int64) {
	t.Helper()
	inventory, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found || inventory.Slots["0:65"].Count != fragments {
		t.Fatalf("rolled-back inventory found=%v err=%v inventory=%+v", found, err, inventory)
	}
	quests, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("rolled-back quest found=%v err=%v", found, err)
	}
	if parent := quests.States[3458]; parent.Status != "active" || parent.Extra[finishSettlementReceiptKey] != "" {
		t.Fatalf("rolled-back parent=%+v", parent)
	}
	if child := quests.States[3473]; child.Status != "active" || child.Extra["reward_state"] == finishRewardGranted {
		t.Fatalf("rolled-back child=%+v", child)
	}
}
