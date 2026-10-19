package dungeon

import (
	"context"
	"fmt"
	"math"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
)

func TestAdventureTowerHundredCountsCommitReplayAndRollback(t *testing.T) {
	for _, grief := range []bool{false, true} {
		repos, cmd := luckyStarSettlementFixture(t, "0")
		cmd.LuckyStar = nil
		key, progress := adventuregroup.DespairHundredClearStatKey, DespairHighestClearedStatKey
		if grief {
			key, progress = adventuregroup.GriefHundredClearStatKey, GriefHighestClearedStatKey
			cmd.GriefRewards = &GriefRewards{}
		}
		ctx := context.Background()
		character, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
		character.Stats[progress] = 98
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		owner, _ := NewOwner(repos)
		for _, floor := range []int{99, 100} {
			cmd.CompletionKey = fmt.Sprintf("adventure-content-floor-%d", floor)
			if grief {
				cmd.GriefClearedFloor = floor
			} else {
				cmd.DespairClearedFloor = floor
			}
			result, err := owner.CommitSettlement(ctx, cmd)
			want := int64(0)
			if floor == 100 {
				want = 1
			}
			if err != nil || result.Replayed || result.Character.Stats[key] != want {
				t.Fatalf("第%d层累计错误：%+v/%v", floor, result, err)
			}
			replay, err := owner.CommitSettlement(ctx, cmd)
			if err != nil || !replay.Replayed || replay.Character.Stats[key] != want {
				t.Fatalf("重放重复计数：%+v/%v", replay, err)
			}
		}
		cmd.CompletionKey = "adventure-content-next-completed-run"
		next, err := owner.CommitSettlement(ctx, cmd)
		if err != nil || next.Replayed || next.Character.Stats[key] != 2 {
			t.Fatalf("独立新结算没有继续累计：%+v/%v", next, err)
		}
		cmd.CompletionKey = "回滚的第100层"
		repos.CharacterSettlement = failDespairPotionCommit{repos.CharacterSettlement}
		failing, _ := NewOwner(repos)
		if _, err := failing.CommitSettlement(ctx, cmd); err == nil {
			t.Fatal("预期事务失败")
		}
		loaded, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
		if loaded.Stats[key] != 2 {
			t.Fatal("失败事务保存了累计次数")
		}
	}
}

func TestAdventureRecommendedCounterUsesNativeU32(t *testing.T) {
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.RecommendedDungeonClear = true
	ctx := context.Background()
	character, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	character.Stats[adventuregroup.RecommendedDungeonClearStatKey] = math.MaxUint16
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	got, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || got.Character.Stats[adventuregroup.RecommendedDungeonClearStatKey] != 65536 {
		t.Fatalf("超过65535的通关次数丢失：%v", err)
	}
}
