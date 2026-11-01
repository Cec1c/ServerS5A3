package adventuregroup

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestBattlePassGloryActualCappedGainAndReplay(t *testing.T) {
	for _, before := range []uint32{0, 95, 100} {
		g := memory.NewMemoryGroup()
		ctx := context.Background()
		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("CST", 28800))
		cfg := RuntimeConfig{ShopCategories: []ShopCategory{{Index: ShopPointGlory, MaxPoint: 100}}}
		a := repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"保留": "权益"}}
		state, _ := ParseRuntimeState(a, cfg, now)
		state.ShopPoints[ShopPointGlory] = before
		state.Expeditions["1"] = Expedition{Area: 1, EndsAt: now.Unix() - 1, Reward: 20}
		old, _ := state.Marshal()
		a.Metadata[RuntimeStateMetadataKey] = old
		if err := g.Account.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
		if _, err := CommitBattlePassExpeditionReward(ctx, g, "a", "1", old, cfg, 1, now); err != nil {
			t.Fatal(err)
		}
		if _, err := CommitBattlePassExpeditionReward(ctx, g, "a", "1", old, cfg, 1, now); err == nil {
			t.Fatal("远征奖励重放未拒绝")
		}
		ch, _, _ := g.Character.Load(ctx, "1")
		want := int64(min(uint32(20), 100-before))
		if ch.Stats[fmt.Sprintf("%s%s:29:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(now))] != want {
			t.Fatal(before, ch.Stats)
		}
		a, _, _ = g.Account.Load(ctx, "a")
		if a.Metadata["保留"] != "权益" {
			t.Fatal("损坏其他账号项")
		}
	}
}
