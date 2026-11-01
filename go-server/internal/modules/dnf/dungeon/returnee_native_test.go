package dungeon

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestReturneeNativeDimensionCompletionAtomicAndReplay(t *testing.T) {
	for _, kind := range []uint32{8, 24, 44} {
		g, cmd := luckyStarSettlementFixture(t, "0")
		cmd.LuckyStar = nil
		cmd.ReturneeDimensionClear = kind == 44
		cmd.UpdatedAt = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		ch, _, _ := g.Character.Load(nil, cmd.CharacterID)
		ch.Stats["is_return_user"] = 1
		ch.Stats["seria_returnee_active_until"] = cmd.UpdatedAt.Add(time.Hour).Unix()
		if err := g.Character.Save(nil, ch); err != nil {
			t.Fatal(err)
		}
		owner, _ := NewOwner(g)
		for i := 0; i < 2; i++ {
			if _, err := owner.CommitSettlement(nil, cmd); err != nil {
				t.Fatal(err)
			}
		}
		want := int64(0)
		if kind == 44 {
			want = 1
		}
		ch, _, _ = g.Character.Load(nil, cmd.CharacterID)
		if ch.Stats[repo.ReturneeDimensionCountKey] != want {
			t.Fatal(kind, ch.Stats)
		}
		cmd.CompletionKey = "回归失败结算"
		g.CharacterSettlement = failDespairPotionCommit{g.CharacterSettlement}
		bad, _ := NewOwner(g)
		if _, err := bad.CommitSettlement(nil, cmd); err == nil {
			t.Fatal("预期失败")
		}
		ch, _, _ = g.Character.Load(nil, cmd.CharacterID)
		if ch.Stats[repo.ReturneeDimensionCountKey] != want {
			t.Fatal("失败结算增加回归进度")
		}
	}
}
