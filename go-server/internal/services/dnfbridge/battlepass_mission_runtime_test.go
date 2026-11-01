package dnfbridge

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestBattlePassMonsterDeathBurstCountsAndReplay(t *testing.T) {
	s, p, g, packets := prepareDungeonDeathBurstTest(t, true)
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%s%s:1:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(s.gameplayNow()))
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[key] != 3 {
		t.Fatal("真实连续击杀没有累计", ch.Stats)
	}
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	ch, _, _ = g.Character.Load(nil, "99")
	if ch.Stats[key] != 3 {
		t.Fatal("重复死亡增加进度")
	}
}
func TestBattlePassMonsterZeroExperienceStillCounts(t *testing.T) {
	s, p, g, packets := prepareDungeonDeathBurstTest(t, true)
	runtime := p.dungeon.runtime
	// 当前PVF经验权重为0时仍是已经接受的真实怪物死亡。
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 0
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%s%s:1:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(s.gameplayNow()))
	ch, _, _ := g.Character.Load(nil, "99")
	if ch.Stats[key] != 3 {
		t.Fatal("零经验怪丢失计数", ch.Stats)
	}
}

func TestBattlePassGoldCardSuccessReplayAndNoFunds(t *testing.T) {
	for _, balance := range []int64{22, 100} {
		s, r, p, _, g := preparePaidCardPriceFlow(t, balance)
		key := fmt.Sprintf("%s%s:22:1", repo.BattlePassCounterPrefix, repo.ActivityDay(s.gameplayNow()))
		if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSideFree, 0, "普通免费牌"); err != nil {
			t.Fatal(err)
		}
		ch, _, _ := g.Character.Load(nil, "99")
		if ch.Stats[key] != 0 {
			t.Fatal("普通免费牌误计为黄金牌")
		}
		for range 2 {
			if err := s.selectCurrentDungeonCardLocked(p, r, dungeonCardSidePaid, 1, "黄金牌及重复点击"); err != nil {
				t.Fatal(err)
			}
		}
		ch, _, _ = g.Character.Load(nil, "99")
		want := int64(1)
		if balance == 22 {
			want = 0
		}
		if ch.Stats[key] != want {
			t.Fatal("黄金翻牌进度或重放错误", balance, ch.Stats)
		}
	}
}
