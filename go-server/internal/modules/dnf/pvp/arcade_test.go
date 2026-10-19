package pvp

import (
	"context"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestArcadeRewardAtomicReceiptAndTrainingGate(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	c := repo.CharacterRecord{CharacterID: "29", AccountID: "a", Level: 50, Stats: map[string]int64{"exp": 100, "gold": 10}}
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "29", Points: repo.SkillPointState{SyncedLevel: 50}}); err != nil {
		t.Fatal(err)
	}
	called := 0
	planner := func(c repo.CharacterRecord, skill repo.SkillRecord) (ArcadeRewardSnapshot, error) {
		called++
		c.Stats["exp"] += 20
		c.Stats["gold"] += 3
		c.Level++
		skill.Points.SyncedLevel++
		skill.Points.TotalSP += 10
		skill.Points.RemainingSP += 10
		return ArcadeRewardSnapshot{Character: c, Skill: skill, Reward: ArcadeReward{20, 3}}, nil
	}
	failed := repos
	failed.CharacterSettlement = failCommit{repos.CharacterSettlement}
	if _, err := ApplyArcadeReward(ctx, failed, "a", "29", "run1", planner, time.Now()); err == nil {
		t.Fatal("commit failure ignored")
	}
	stored, _, _ := repos.Character.Load(ctx, "29")
	skill, _, _ := repos.Skill.Load(ctx, "29")
	if stored.Level != 50 || stored.Stats["exp"] != 100 || stored.Stats["gold"] != 10 || skill.Points.TotalSP != 0 {
		t.Fatal("reward transaction partially committed")
	}
	for i := 0; i < 2; i++ {
		r, err := ApplyArcadeReward(ctx, repos, "a", "29", "run1", planner, time.Now())
		if err != nil || r.Character.Level != 51 || r.Character.Stats["gold"] != 13 || r.Skill.Points.TotalSP != 10 || r.Reward != (ArcadeReward{20, 3}) {
			t.Fatalf("reward=%+v error=%v", r, err)
		}
	}
	if called != 2 {
		t.Fatal("receipt replay recalculated reward")
	}
	if _, err := ApplyArcadeReward(ctx, repos, "other", "29", "run2", planner, time.Now()); err == nil {
		t.Fatal("unowned reward accepted")
	}
	r, err := ApplyArcadeReward(ctx, repos, "a", "29", "run2", planner, time.Now())
	if err != nil || r.Character.Stats["gold"] != 16 {
		t.Fatal("new run reused old receipt")
	}
	q := repo.QuestRecord{States: map[int64]repo.QuestState{}, Progress: map[int64]repo.QuestState{}}
	for id := int64(2670); id <= 2674; id++ {
		q.States[id] = repo.QuestState{Status: "completed"}
	}
	if !ArcadeTrainingCompleted(q) {
		t.Fatal("completed training rejected")
	}
	q.States[2674] = repo.QuestState{Status: "active", ProgressValue: 262657}
	q.Progress[2674] = repo.QuestState{Status: "completed"}
	if ArcadeTrainingCompleted(q) {
		t.Fatal("active canonical quest treated as completed")
	}
}
