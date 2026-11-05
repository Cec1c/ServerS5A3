package quest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingQuestFixture(t *testing.T) (*Catalog, *Owner, wedding.Owner, time.Time) {
	t.Helper()
	ctx := context.Background()
	source := catalogTestSource{
		DefaultList:           "20737 `wedding.qst`\n",
		"n_quest/wedding.qst": questCatalogTestDefinition("[wedding]", 30, 99, "[all]", "[type]\n`[condition under clear]`\n[sub type]\n22\n[int data]\n-1 -1 5\n"),
	}
	index, err := pvf.Build(ctx, source, pvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	g := memory.NewMemoryGroup()
	for i, job := range []string{"0", "11"} {
		id := fmt.Sprint(i + 1)
		if err = g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Level: 30, Job: job}); err != nil {
			t.Fatal(err)
		}
		if err = g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, o, wedding.Owner{Repositories: g, Rules: &wedding.Rules{MinimumLevel: 30}}, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
}

func TestWeddingPreparationRequiresReciprocalEngagement(t *testing.T) {
	catalog, o, marriage, now := weddingQuestFixture(t)
	ctx := context.Background()
	cmd := Command{AccountID: "a1", SelectedCharacterID: 1, QuestID: 20737, Operation: "accept_quest"}
	e := CharacterEligibility{Level: 30, Job: 0}
	if _, err := o.ApplyAcceptAt(ctx, catalog, e, cmd, now); !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("single: %v", err)
	}
	if _, err := marriage.Engage(ctx, wedding.PairRequest{Actor: wedding.Actor{AccountID: "a1", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "a2", CharacterID: "2"}, Now: now}); err != nil {
		t.Fatal(err)
	}
	result, err := o.ApplyAcceptAt(ctx, catalog, e, cmd, now)
	if err != nil || result.InitTrigger != 5 {
		t.Fatalf("accept %+v %v", result, err)
	}
	record, _, _ := o.quests.Load(ctx, "1")
	if record.States[20737].Extra["wedding_partner"] != "2" {
		t.Fatal("accepted without binding partner")
	}
	// Client SET_TRIGGER cannot supply the five authoritative dungeon clears.
	cmd.Operation = "set_quest_trigger"
	cmd.TriggerType = 0
	if result, err := o.ApplySetTrigger(ctx, cmd); err != nil || result.StateChanged {
		t.Fatalf("client trigger %+v %v", result, err)
	}
}

func TestWeddingPreparationFiveRunsAndReplayAndWrongPartner(t *testing.T) {
	catalog, o, marriage, now := weddingQuestFixture(t)
	ctx := context.Background()
	_, err := marriage.Engage(ctx, wedding.PairRequest{Actor: wedding.Actor{AccountID: "a1", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "a2", CharacterID: "2"}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 30}, Command{AccountID: "a1", SelectedCharacterID: 1, QuestID: 20737}, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		input := ClearMapCompletionInput{DungeonID: 3, MapID: 10, WeddingPartnerID: "3", CompletionKey: fmt.Sprint("run", i), CompletedAt: now.Add(time.Duration(i) * time.Minute)}
		bad, err := o.ApplyClearMapCompletion(ctx, catalog, "1", input)
		if err != nil || len(bad.Completions) != 0 {
			t.Fatalf("wrong partner %+v %v", bad, err)
		}
		input.WeddingPartnerID = "2"
		got, err := o.ApplyClearMapCompletion(ctx, catalog, "1", input)
		if err != nil || len(got.Completions) != 1 || got.Completions[0].RemainingTrigger != int64(5-i) {
			t.Fatalf("run %d %+v %v", i, got, err)
		}
		replayed, err := o.ApplyClearMapCompletion(ctx, catalog, "1", input)
		if err != nil || !replayed.Idempotent {
			t.Fatalf("replay %+v %v", replayed, err)
		}
		row, _, _ := o.quests.Load(ctx, "1")
		state := row.States[20737]
		if state.ProgressValue != int64(5-i) || (state.Extra["reward_state"] == "pending") != (i == 5) {
			t.Fatalf("run %d %+v", i, state)
		}
	}
}
