package raid

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestBattlePassRaidSharesFinalReceiptAndRollsBack(t *testing.T) {
	for mode := byte(1); mode <= 3; mode++ {
		g, cmd := weeklyFixture(t)
		cmd.Mode = mode
		ctx := context.Background()
		for range 2 {
			if _, err := CommitWeeklyClear(ctx, g, cmd); err != nil {
				t.Fatal(err)
			}
		}
		check := func() {
			t.Helper()
			for _, id := range []string{"1", "2", "3"} {
				ch, _, _ := g.Character.Load(ctx, id)
				for _, kind := range []int{32, 33, 34} {
					want := int64(0)
					if id != "3" && (kind == 32 || kind == 33 && mode == 3 || kind == 34 && mode == 2) {
						want = 1
					}
					key := fmt.Sprintf("%s%s:%d:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(cmd.CompletedAt), kind)
					if ch.Stats[key] != want {
						t.Fatal(mode, id, kind, ch.Stats)
					}
				}
			}
		}
		check()
		cmd.InstanceID = "10112233445566778899aabbccddeeff"
		cmd.Members[1].AccountID = "他人账号"
		if _, err := CommitWeeklyClear(ctx, g, cmd); err == nil {
			t.Fatal("整组归属错误没有拒绝")
		}
		check()
	}
}
