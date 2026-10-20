package quest

import (
	"context"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

func TestInfernoChallengeFinishCommitsAndReplays(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{10110: {Status: "active", ProgressValue: 0, Extra: map[string]string{"completion_key": "run-19/inferno/10110", "reward_state": "pending"}}}}); err != nil {
		t.Fatal(err)
	}
	d := Definition{ID: 10110, Path: "Hell/achievement_86_Hell_inferno_2.qst", Type: "[condition under clear]", LevelMin: 1, LevelMax: 99, Difficulty: "N", RewardType: "[inferno challenge]", RewardIntData: []int64{7539}}
	catalog := &Catalog{ordered: []Definition{d}, byID: map[int64]Definition{d.ID: d}}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	in := FinishCommitInput{AccountID: "acc", CharacterID: "19", QuestID: d.ID, Multiplier: 1, CommittedAt: time.Now().UTC(), Progression: finishOwnerProgression(t)}
	first, err := owner.ApplyFinishSettlement(ctx, catalog, in)
	if err != nil {
		t.Fatal(err)
	}
	if !first.AtomicCommitted || first.Replayed || !first.HasHellChallenge || first.HellChallengeValue != 7539 || len(first.Items) != 0 {
		t.Fatalf("inferno finish=%+v", first)
	}
	second, err := owner.ApplyFinishSettlement(ctx, catalog, in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || !second.HasHellChallenge {
		t.Fatalf("inferno replay=%+v", second)
	}
	record, _, err := repos.Quest.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if record.States[d.ID].Status != "completed" || record.States[d.ID].Extra["reward_state"] != "granted" {
		t.Fatalf("quest not settled: %+v", record.States[d.ID])
	}
}

func TestRealPVFInfernoChallengeEP5Reward(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), a, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := catalog.byID[10110]
	if !ok || normalizeQuestTag(d.RewardType) != "inferno challenge" {
		t.Fatalf("actual EP5 reward=%+v", d)
	}
	plan, err := catalog.PlanFinishReward(CharacterEligibility{Level: 90, Job: 0}, 10110, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.HasHellChallenge || plan.HellChallengeValue != 7539 || len(plan.Items) != 0 {
		t.Fatalf("real EP5 plan=%+v", plan)
	}
}
