package pvp

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestPVFZeroArcadeCostStillChecksAccountAndKeepsFatigue(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Now()
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"fatigue": 156, "fatigue_limit": 156, "fatigue_updated_at": now.Unix()}}); err != nil {
		t.Fatal(err)
	}
	out, err := ConsumeArcadeFatigue(ctx, r, ConsumeArcadeCommand{AccountID: "a", CharacterID: "1", Amount: 0, UpdatedAt: now})
	if err != nil || out.Consumed != 0 || out.Before != out.After {
		t.Fatal(out, err)
	}
	if _, err := ConsumeArcadeFatigue(ctx, r, ConsumeArcadeCommand{AccountID: "wrong", CharacterID: "1", Amount: 0, UpdatedAt: now}); err == nil {
		t.Fatal("零消耗跳过账号校验")
	}
}
