package dungeon

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestBattlePassClearConditionsReplayAndRollback(t *testing.T) {
	g, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.DungeonID = 310
	cmd.UpdatedAt = time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("CST", 28800))
	cmd.BattlePassNoCoinClear = true
	cmd.BattlePassSRankClear = true
	owner, _ := NewOwner(g)
	if _, err := owner.CommitSettlement(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if out, err := owner.CommitSettlement(context.Background(), cmd); err != nil || !out.Replayed {
		t.Fatal(out, err)
	}
	check := func() {
		t.Helper()
		ch, _, _ := g.Character.Load(nil, "c")
		for _, kind := range []int{0, 5, 6} {
			key := fmt.Sprintf("%s%s:%d:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(cmd.UpdatedAt), kind)
			if ch.Stats[key] != 1 {
				t.Fatal(kind, ch.Stats)
			}
		}
	}
	check()
	cmd.CompletionKey = "失败通关"
	g.CharacterSettlement = failDespairPotionCommit{g.CharacterSettlement}
	bad, _ := NewOwner(g)
	if _, err := bad.CommitSettlement(context.Background(), cmd); err == nil {
		t.Fatal("失败未拒绝")
	}
	check()
}
