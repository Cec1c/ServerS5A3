package dungeon

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestDungeonSettlementCommitsAndRollsBackLevelEventWithExperience(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	owner, _ := NewOwner(repos)
	if _, err := owner.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	c, _, _ := repos.Character.Load(ctx, "c")
	stamp := c.Stats[repo.LevelUpEventKey(2)]
	if stamp <= 0 {
		t.Fatal("settlement upgrade event absent")
	}
	if _, err := owner.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	c, _, _ = repos.Character.Load(ctx, "c")
	if c.Stats[repo.LevelUpEventKey(2)] != stamp {
		t.Fatal("settlement replay changed event")
	}
	repos, cmd = luckyStarSettlementFixture(t, "0")
	repos.CharacterSettlement = failingLuckyUOW{repos.CharacterSettlement}
	owner, _ = NewOwner(repos)
	if _, err := owner.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("injected failure absent")
	}
	c, _, _ = repos.Character.Load(ctx, "c")
	if repo.HasPendingLevelUp(c) {
		t.Fatal("failed settlement retained upgrade event")
	}
}
