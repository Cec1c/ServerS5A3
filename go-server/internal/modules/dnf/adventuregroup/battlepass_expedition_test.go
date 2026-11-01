package adventuregroup

import (
	"context"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestBattlePassExpeditionAtomicStaleAndOwner(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("CST", 28800))
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	next := RuntimeState{}
	if err := CommitBattlePassExpeditionStart(ctx, g, "a", "1", "", next, now); err != nil {
		t.Fatal(err)
	}
	if err := CommitBattlePassExpeditionStart(ctx, g, "a", "1", "", next, now); !errors.Is(err, ErrExpeditionChanged) {
		t.Fatal("旧派遣重复提交", err)
	}
	a, _, _ := g.Account.Load(ctx, "a")
	if a.Metadata["keep"] != "yes" {
		t.Fatal("改了其他数据")
	}
	if err := CommitBattlePassExpeditionStart(ctx, g, "b", "1", "", next, now); err == nil {
		t.Fatal("跨账号派遣未拒绝")
	}
	ch, _, _ := g.Character.Load(ctx, "1")
	if ch.Stats[fmt.Sprintf("%s%s:12:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(now))] != 1 {
		t.Fatal("远征计数错误", ch.Stats)
	}
}
