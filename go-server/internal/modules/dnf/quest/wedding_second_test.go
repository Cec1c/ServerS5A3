package quest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// These scripts are the matching PVF's whitespace-normalized decoded entries,
// 20737/20738, including
// 20738's genuinely empty [depend give item] section and real material/reward.
func weddingPreparationPVFCatalog(t *testing.T, secondID int64, alter func(string) string) *Catalog {
	t.Helper()
	source := catalogTestSource{DefaultList: fmt.Sprintf("20737 `event/chn_2014_wedding_01.qst`\n%d `event/chn_2014_wedding_02.qst`\n", secondID)}
	for i, id := range []int{20737, 20738} {
		text, err := os.ReadFile(fmt.Sprintf("testdata/wedding_%d.qst", id))
		if err != nil {
			t.Fatal(err)
		}
		textValue := strings.ReplaceAll(string(text), "\r\n", "\n")
		if i == 1 && alter != nil {
			changed := alter(textValue)
			if changed == textValue {
				t.Fatal("negative-case fixture did not change the decoded PVF")
			}
			textValue = changed
		}
		source[fmt.Sprintf("n_quest/event/chn_2014_wedding_0%d.qst", i+1)] = textValue
	}
	index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestWeddingPreparationSecondEmptyDependSectionRequiresEngagementAndFirstQuest(t *testing.T) {
	ctx := context.Background()
	catalog := weddingPreparationPVFCatalog(t, 20738, nil)
	d, ok := catalog.Find(20738)
	if !ok || d.Name != "准备结婚吧！ (2/2)" || !d.HasDependGiveItem || !d.emptyDependGiveItem || len(d.DependGiveItemData) != 0 {
		t.Fatalf("real second preparation definition: %+v", d)
	}
	_, owner, marriage, now := weddingQuestFixture(t)
	cmd := Command{AccountID: "a1", SelectedCharacterID: 1, QuestID: 20738}
	eligibility := CharacterEligibility{Level: 30, Job: 0}
	completedFirst := r.QuestRecord{CharacterID: "1", States: map[int64]r.QuestState{20737: {Status: "completed"}}}
	if err := owner.repositories.Quest.Save(ctx, completedFirst); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyAcceptAt(ctx, catalog, eligibility, cmd, now); !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("single character accepted wedding preparation: %v", err)
	}
	if _, err := marriage.Engage(ctx, wedding.PairRequest{Actor: wedding.Actor{AccountID: "a1", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "a2", CharacterID: "2"}, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := owner.repositories.Quest.Save(ctx, r.QuestRecord{CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyAcceptAt(ctx, catalog, eligibility, cmd, now); !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("missing prerequisite accepted: %v", err)
	}
	if err := owner.repositories.Quest.Save(ctx, completedFirst); err != nil {
		t.Fatal(err)
	}
	character, _, _ := owner.repositories.Character.Load(ctx, "1")
	eligible, err := WithWeddingEligibility(ctx, owner.repositories.Character, character, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	list := catalog.QuestListAt(eligible, completedFirst, now)
	if len(list.IDs) != 1 || list.IDs[0] != 20738 {
		t.Fatalf("second preparation absent from client list: %+v", list)
	}
	accepted, err := owner.ApplyAcceptAt(ctx, catalog, eligibility, cmd, now)
	if err != nil || accepted.InitTrigger != 1 {
		t.Fatalf("accept second preparation: %+v %v", accepted, err)
	}
	row, _, _ := owner.repositories.Quest.Load(ctx, "1")
	bag, _, _ := owner.repositories.Inventory.Load(ctx, "1")
	if row.States[20738].Extra["wedding_partner"] != "2" || len(bag.Slots) != 0 {
		t.Fatalf("accept lost partner or granted an item: quest=%+v bag=%+v", row, bag)
	}
}

func TestWeddingPreparationSecondEmptyDependExceptionStaysNarrow(t *testing.T) {
	const empty = "[depend give item]\n\n[/depend give item]"
	for _, tc := range []struct {
		name  string
		id    int64
		alter func(string) string
	}{
		{"nonempty-integers", 20738, func(s string) string {
			return strings.Replace(s, empty, "[depend give item]\n10002610 1\n[/depend give item]", 1)
		}},
		{"noninteger-token", 20738, func(s string) string {
			return strings.Replace(s, empty, "[depend give item]\n`unparsed-item`\n[/depend give item]", 1)
		}},
		{"empty-string-token", 20738, func(s string) string {
			return strings.Replace(s, empty, "[depend give item]\n``\n[/depend give item]", 1)
		}},
		{"duplicate-section", 20738, func(s string) string {
			return strings.Replace(s, empty, empty+"\n[depend give item]\n10002610 1\n[/depend give item]", 1)
		}},
		{"other-quest", 20740, nil},
		{"other-grade", 20738, func(s string) string { return strings.Replace(s, "`[wedding]`", "`[normal]`", 1) }},
		{"other-type", 20738, func(s string) string { return strings.Replace(s, "`[seeking]`", "`[meet npc]`", 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := weddingPreparationPVFCatalog(t, tc.id, tc.alter)
			record := r.QuestRecord{States: map[int64]r.QuestState{20737: {Status: "completed"}}}
			_, err := catalog.DecideAcceptAt(CharacterEligibility{Level: 30, Job: 0, weddingPartner: "2"}, record, tc.id, time.Unix(1793534400, 0))
			if !errors.Is(err, ErrQuestAcceptEventItemsRequired) {
				t.Fatalf("asset acceptance boundary bypassed: %v", err)
			}
		})
	}
	// An integer projection or detached JSON cannot assert parser evidence.
	if weddingEmptyAcceptanceItems(Definition{ID: 20738, Grade: "[wedding]", Type: "[seeking]", HasDependGiveItem: true}) {
		t.Fatal("missing parsed-empty evidence was trusted")
	}
}

func TestWeddingPreparationSecondConsumesRealMaterialAndRewardsOnce(t *testing.T) {
	ctx := context.Background()
	catalog := weddingPreparationPVFCatalog(t, 20738, nil)
	_, owner, marriage, now := weddingQuestFixture(t)
	if _, err := marriage.Engage(ctx, wedding.PairRequest{Actor: wedding.Actor{AccountID: "a1", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "a2", CharacterID: "2"}, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := owner.repositories.Quest.Save(ctx, r.QuestRecord{CharacterID: "1", States: map[int64]r.QuestState{20737: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	cmd := Command{AccountID: "a1", SelectedCharacterID: 1, QuestID: 20738}
	if _, err := owner.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 30, Job: 0}, cmd, now); err != nil {
		t.Fatal(err)
	}
	cmd.Operation, cmd.TriggerType = "set_quest_trigger", 1
	if _, err := owner.ApplySetTrigger(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	character, _, _ := owner.repositories.Character.Load(ctx, "1")
	character.Stats["exp"] = 0
	for _, save := range []func() error{
		func() error { return owner.repositories.Character.Save(ctx, character) },
		func() error {
			return owner.repositories.Skill.Save(ctx, r.SkillRecord{CharacterID: "1", Points: r.SkillPointState{SyncedLevel: 30}})
		},
		func() error { return owner.repositories.Equipment.Save(ctx, r.EquipmentRecord{CharacterID: "1"}) },
	} {
		if err := save(); err != nil {
			t.Fatal(err)
		}
	}
	var exp, sp strings.Builder
	for level := 1; level <= 90; level++ {
		fmt.Fprintf(&exp, "%d ", level*100000)
		fmt.Fprintf(&sp, "%d 0\n", level)
	}
	tables, err := progression.Load(ctx, finishProgressionSource{
		progression.ExperienceTablePath: exp.String(),
		progression.SkillPointTablePath: "[sp table]\n" + sp.String() + "[/sp table]\n[tp table]\n50 0\n[/tp table]\n",
		progression.QuestParameterPath:  "[difficulty]\n`G` 30\n[/difficulty]\n[exp reward table]\n" + strings.Repeat("100 -1\n", 90) + "[gold reward table]\n10\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := FinishCommitInput{AccountID: "a1", CharacterID: "1", QuestID: 20738, Multiplier: 1, CommittedAt: now.Add(time.Minute), Progression: tables, AllocateItem: finishOwnerAllocator(nil)}
	if _, err := owner.ApplyFinishSettlement(ctx, catalog, input); !errors.Is(err, ErrFinishRequiredItemsMissing) {
		t.Fatalf("collection completed without the real doll: %v", err)
	}
	if err := owner.repositories.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:40": {ItemID: 10002610, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil || !first.AtomicCommitted || first.Replayed || len(first.ConsumedItems) != 1 || first.ConsumedItems[0].ItemID != 10002610 || first.ConsumedItems[0].Delta != 1 || len(first.Items) != 1 || first.Items[0].ItemID != 10002901 || first.Items[0].Delta != 5 {
		t.Fatalf("real second preparation settlement: %+v %v", first, err)
	}
	replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil || !replay.Replayed {
		t.Fatalf("second preparation replay: %+v %v", replay, err)
	}
	bag, _, _ := owner.repositories.Inventory.Load(ctx, "1")
	quests, _, _ := owner.repositories.Quest.Load(ctx, "1")
	if len(bag.Slots) != 1 || bag.Slots["0:65"].ItemID != 10002901 || bag.Slots["0:65"].Count != 5 || quests.States[20738].Status != "completed" {
		t.Fatalf("material/reward was duplicated or quest incomplete: bag=%+v quest=%+v", bag, quests.States[20738])
	}
}
