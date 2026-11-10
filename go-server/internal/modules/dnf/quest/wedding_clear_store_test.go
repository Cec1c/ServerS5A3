package quest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingStoredClearFixture(t *testing.T) (*Catalog, *Owner, time.Time) {
	t.Helper()
	catalog, owner, marriage, now := weddingQuestFixture(t)
	ctx := context.Background()
	for i, id := range []string{"50001", "50002"} {
		if err := marriage.Repositories.Character.Save(ctx, r.CharacterRecord{
			CharacterID: id, AccountID: "a" + id, Level: 30, Job: []string{"0", "11"}[i],
		}); err != nil {
			t.Fatal(err)
		}
		if err := marriage.Repositories.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := marriage.Engage(ctx, wedding.PairRequest{
		Actor:   wedding.Actor{AccountID: "a50001", CharacterID: "50001"},
		Partner: wedding.Actor{AccountID: "a50002", CharacterID: "50002"}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := owner.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 30}, Command{
		AccountID: "a50001", SelectedCharacterID: 50001, QuestID: 20737,
	}, now); err != nil || result.InitTrigger != 5 {
		t.Fatalf("accept result=%+v err=%v", result, err)
	}
	return catalog, owner, now
}

func weddingNativeStoredClearInput(at time.Time) ClearMapCompletionInput {
	return ClearMapCompletionInput{
		WeddingPartnerID: "50002", DungeonID: 310, MapID: 58569, CompletedAt: at,
		CompletionKey: fmt.Sprintf("dungeon-clear:char:50001:dungeon:310:maze:0:map:58569:seed:4294967295:completed_at_unix_nano:%d:target:65535:source:clear_map_phase_a", at.UnixNano()),
	}
}

func assertWeddingStoredClearState(t *testing.T, owner *Owner, remaining int64, originalKey string) r.QuestState {
	t.Helper()
	record, found, err := owner.quests.Load(context.Background(), "50001")
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	state := record.States[20737]
	if state.ProgressValue != remaining || state.Extra["completion_key"] != originalKey ||
		(state.Extra["reward_state"] == "pending") != (remaining == 0) {
		t.Fatalf("stored wedding state=%+v remaining=%d key=%q", state, remaining, originalKey)
	}
	for key := range state.Extra {
		if len(key) > 128 {
			t.Fatalf("metadata key exceeds MySQL VARCHAR(128): length=%d key=%q", len(key), key)
		}
	}
	return state
}

func TestWeddingClearNativeLongReceiptsFitStoreAndCountFiveRuns(t *testing.T) {
	catalog, owner, accepted := weddingStoredClearFixture(t)
	for run := 1; run <= 5; run++ {
		input := weddingNativeStoredClearInput(accepted.Add(time.Duration(run) * time.Minute))
		if len(input.CompletionKey) < 147 {
			t.Fatalf("test receipt is shorter than the native long-key boundary: %d", len(input.CompletionKey))
		}
		result, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", input)
		if err != nil || len(result.Completions) != 1 || result.Completions[0].RemainingTrigger != int64(5-run) {
			t.Fatalf("run=%d result=%+v err=%v", run, result, err)
		}
		assertWeddingStoredClearState(t, owner, int64(5-run), input.CompletionKey)
		replayed, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", input)
		if err != nil || !replayed.Idempotent {
			t.Fatalf("same-run replay=%+v err=%v", replayed, err)
		}
		assertWeddingStoredClearState(t, owner, int64(5-run), input.CompletionKey)
	}
}

func TestWeddingClearLongReceiptsWithSameStoredPrefixRemainDistinct(t *testing.T) {
	catalog, owner, accepted := weddingStoredClearFixture(t)
	first := weddingNativeStoredClearInput(accepted.Add(time.Minute))
	second := first
	second.CompletionKey = strings.TrimSuffix(first.CompletionKey, "a") + "b"
	if first.CompletionKey == second.CompletionKey || first.CompletionKey[:128] != second.CompletionKey[:128] {
		t.Fatal("test receipts must differ only after their long common prefix")
	}
	for i, input := range []ClearMapCompletionInput{first, second} {
		result, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", input)
		if err != nil || result.Idempotent || len(result.Completions) != 1 {
			t.Fatalf("distinct long receipt result=%+v err=%v", result, err)
		}
		assertWeddingStoredClearState(t, owner, int64(4-i), input.CompletionKey)
	}
	// The first receipt remains identifiable after the second one is stored.
	result, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", first)
	if err != nil || !result.Idempotent {
		t.Fatalf("earlier long receipt replay=%+v err=%v", result, err)
	}
	assertWeddingStoredClearState(t, owner, 3, second.CompletionKey)
}

func TestWeddingClearLegacyShortRawReceiptDoesNotCountAgain(t *testing.T) {
	catalog, owner, accepted := weddingStoredClearFixture(t)
	const legacyKey = "legacy-short-run"
	record, _, err := owner.quests.Load(context.Background(), "50001")
	if err != nil {
		t.Fatal(err)
	}
	state := record.States[20737]
	state.ProgressValue, state.UpdatedAt = 4, accepted.Add(time.Minute)
	state.Extra["wedding_clear_run:"+legacyKey] = "1"
	state.Extra["completion_key"] = legacyKey
	record.States[20737] = state
	if err := owner.quests.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	input := ClearMapCompletionInput{WeddingPartnerID: "50002", DungeonID: 310, MapID: 58569, CompletionKey: legacyKey, CompletedAt: state.UpdatedAt}
	result, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", input)
	if err != nil || !result.Idempotent {
		t.Fatalf("legacy receipt replay=%+v err=%v", result, err)
	}
	assertWeddingStoredClearState(t, owner, 4, legacyKey)
	newRun := weddingNativeStoredClearInput(accepted.Add(2 * time.Minute))
	if _, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", newRun); err != nil {
		t.Fatal(err)
	}
	if state := assertWeddingStoredClearState(t, owner, 3, newRun.CompletionKey); state.Extra["wedding_clear_run:"+legacyKey] != "1" {
		t.Fatal("new receipt discarded the legacy replay fence")
	}
}
