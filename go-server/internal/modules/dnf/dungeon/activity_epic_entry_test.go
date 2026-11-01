package dungeon

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestActivityEpicEntryOriginalTransactionRollbackAndReplay(t *testing.T) {
	ctx := context.Background()
	_, g, cmd := epicOwnerFixture(t)
	event := repo.ActivityEvent{ID: "epic:admission", AccountID: cmd.AccountID, CharacterID: cmd.CharacterID, Metric: repo.ActivityFatigue, Source: "epic_entry", Amount: 1, DungeonID: cmd.DungeonID, OccurredAt: cmd.UpdatedAt}
	cmd.Activity = &event
	base := g.CharacterAssets
	fault := errors.New("模拟入场事务提交前失败")
	g.CharacterAssets = epicRollbackAssets{base, fault}
	owner, _ := NewOwner(g)
	if _, err := owner.ConsumeEpicEntry(ctx, cmd); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	summary, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: cmd.AccountID})
	if err != nil || summary.Metrics[repo.ActivityFatigue] != 0 {
		t.Fatal("失败入场写入统计", summary, err)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 0 || fp != 100 || tickets != 90 {
		t.Fatal(seq, fp, tickets)
	}
	g.CharacterAssets = base
	owner, _ = NewOwner(g)
	if _, err := owner.ConsumeEpicEntry(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if result, err := owner.ConsumeEpicEntry(ctx, cmd); err != nil || !result.Replay {
		t.Fatal(result, err)
	}
	summary, err = g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: cmd.AccountID})
	if err != nil || summary.Metrics[repo.ActivityFatigue] != 1 {
		t.Fatal(summary, err)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 1 || fp != 99 || tickets != 60 {
		t.Fatal(seq, fp, tickets)
	}
}
