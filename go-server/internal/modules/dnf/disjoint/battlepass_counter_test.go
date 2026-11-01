package disjoint

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestBattlePassDisjointAndEmblemConsumeOnly(t *testing.T) {
	for _, kind := range []uint32{19, 42} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			ctx := context.Background()
			g := newOwnerTestRepositories(t, "a")
			o, _ := NewOwner(g)
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("CST", 28800))
			cmd := Command{AccountID: "a", CharacterID: "77", UpdatedAt: now, Project: func(a *Assets) (Changes, error) {
				delete(a.Inventory.Slots, "0:5")
				a.Inventory.Slots["0:6"] = repo.ItemStack{ItemID: 9002, Count: 1}
				return Changes{Inventory: true, EmblemUpgradeFrom: 2}, nil
			}}
			apply := o.CompoundEmblems
			if kind == 42 {
				apply = o.DisjointAvatar
			}
			if err := apply(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			if err := apply(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			ch, _, _ := g.Character.Load(ctx, "77")
			key := fmt.Sprintf("%s%s:%d:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(now), kind)
			if ch.Stats[key] != 1 {
				t.Fatal("无消耗操作重复计数", ch.Stats)
			}
			upgraded := ch.Stats[fmt.Sprintf("%s%s:20:2", repo.BattlePassCounterPrefix, repo.ActivityDay(now))]
			if (kind == 19 && upgraded != 1) || (kind == 42 && upgraded != 0) {
				t.Fatal("徽章升级参数或分解串计数", ch.Stats)
			}
		})
	}
}
