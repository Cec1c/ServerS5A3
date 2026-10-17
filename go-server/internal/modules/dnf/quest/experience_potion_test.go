package quest

import (
	"context"
	"testing"
	"time"
)

func TestQuestExperiencePotionAddsToGrowthContractAndReplays(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
	input.ExperienceBonusPercent = 20
	c, _, _ := repos.Character.Load(ctx, "19")
	c.Stats["experience_potion_quest_basis_points"] = 1000
	c.Stats["experience_potion_quest_until_ms"] = input.CommittedAt.Add(time.Minute).UnixMilli()
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseExperienceDelta != 110 || first.ExperienceBonusDelta != 33 || first.ExperienceDelta != 143 {
		t.Fatalf("base=%d bonus=%d total=%d, want 110+22+11=143", first.BaseExperienceDelta, first.ExperienceBonusDelta, first.ExperienceDelta)
	}
	input.CommittedAt = input.CommittedAt.Add(time.Hour)
	replay, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.ExperienceDelta != 143 {
		t.Fatalf("expired receipt replay=%+v", replay)
	}
}
