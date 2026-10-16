package dungeon

import (
	"context"
	"testing"
)

func TestDespairSettlementPersistsWithReceiptAndRejectsSkipping(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.DespairClearedFloor = 1
	owner, _ := NewOwner(repos)
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || first.Character.Stats[DespairHighestClearedStatKey] != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// A lost transport response must not count the same completion twice.
	if result, err := owner.CommitSettlement(ctx, cmd); err != nil || !result.Replayed || result.Character.Stats[DespairHighestClearedStatKey] != 1 {
		t.Fatalf("replay=%+v err=%v", result, err)
	}
	cmd.CompletionKey = "clear-2"
	cmd.DespairClearedFloor = 3
	if _, err := owner.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("skipped floor committed")
	}
	loaded, _, _ := repos.Character.Load(ctx, "c")
	if loaded.Stats[DespairHighestClearedStatKey] != 1 || loaded.Stats["exp"] != first.Character.Stats["exp"] {
		t.Fatal("failed clear partially advanced")
	}
	cmd.DespairClearedFloor = 2
	second, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || second.Character.Stats[DespairHighestClearedStatKey] != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestDespairSettlementTransactionFailureDoesNotSaveProgress(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.DespairClearedFloor = 1
	repos.CharacterSettlement = failingLuckyUOW{repos.CharacterSettlement}
	owner, _ := NewOwner(repos)
	if _, err := owner.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("failed transaction committed")
	}
	loaded, _, _ := repos.Character.Load(ctx, "c")
	if _, present := loaded.Stats[DespairHighestClearedStatKey]; present {
		t.Fatal("tower progress survived rollback")
	}
	if loaded.Level != 1 || loaded.Stats["exp"] != 90 {
		t.Fatal("experience survived rollback")
	}
}
