package dungeon

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

// 从角色当前进度接续第37/99/100层，不把任务限制为第1层；回执重放和失败均不累计。
func TestBattlePassDespairAnyFloorAndGriefReplay(t *testing.T) {
	for _, floor := range []int{1, 37, 99, 100} {
		for _, grief := range []bool{false, true} {
			g, cmd := luckyStarSettlementFixture(t, "0")
			cmd.LuckyStar = nil
			cmd.UpdatedAt = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			progress, kind := DespairHighestClearedStatKey, 7
			if grief {
				progress = GriefHighestClearedStatKey
				kind = 45
				cmd.GriefClearedFloor = floor
				cmd.GriefRewards = &GriefRewards{}
			} else {
				cmd.DespairClearedFloor = floor
			}
			ch, _, _ := g.Character.Load(nil, cmd.CharacterID)
			ch.Stats[progress] = int64(floor - 1)
			if err := g.Character.Save(nil, ch); err != nil {
				t.Fatal(err)
			}
			owner, _ := NewOwner(g)
			if _, err := owner.CommitSettlement(context.Background(), cmd); err != nil {
				t.Fatal(floor, grief, err)
			}
			if out, err := owner.CommitSettlement(context.Background(), cmd); err != nil || !out.Replayed {
				t.Fatal(out, err)
			}
			key := fmt.Sprintf("%s%s:%d:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(cmd.UpdatedAt), kind)
			ch, _, _ = g.Character.Load(nil, cmd.CharacterID)
			if ch.Stats[key] != 1 {
				t.Fatal("任意层通关/重放计数错误", floor, grief, ch.Stats)
			}
			cmd.CompletionKey = "失败的新结算"
			g.CharacterSettlement = failDespairPotionCommit{g.CharacterSettlement}
			bad, _ := NewOwner(g)
			if _, err := bad.CommitSettlement(context.Background(), cmd); err == nil {
				t.Fatal("预期结算失败")
			}
			ch, _, _ = g.Character.Load(nil, cmd.CharacterID)
			if ch.Stats[key] != 1 {
				t.Fatal("失败结算增加战令进度")
			}
		}
	}
}

func TestBattlePassAdaptedDungeonSettlementAtomic(t *testing.T) {
	for _, kind := range []uint32{8, 24, 44} {
		g, cmd := luckyStarSettlementFixture(t, "0")
		cmd.LuckyStar = nil
		cmd.BattlePassDungeonKind = kind
		cmd.UpdatedAt = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		owner, _ := NewOwner(g)
		if _, err := owner.CommitSettlement(nil, cmd); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.CommitSettlement(nil, cmd); err != nil {
			t.Fatal(err)
		}
		ch, _, _ := g.Character.Load(nil, cmd.CharacterID)
		if ch.Stats[fmt.Sprintf("%s%s:%d:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(cmd.UpdatedAt), kind)] != 1 {
			t.Fatal(kind, ch.Stats)
		}
	}
}
