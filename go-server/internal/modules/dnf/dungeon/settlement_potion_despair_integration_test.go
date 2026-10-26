package dungeon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
)

// The potion and tower patches both change CommitSettlement. Exercise their
// shared transaction and receipt after resolving that source conflict.
func TestDespairPotionSettlementCommitsAndReplaysTogether(t *testing.T) {
	for _, saveFailure := range []bool{false, true} {
		name := "commit_and_expired_replay"
		if saveFailure {
			name = "inventory_failure_rolls_back_both"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			g, cmd := despairRewardFixture(t)
			now := time.Unix(1700000000, 0).UTC()
			before, _, err := g.Character.Load(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			if err := experiencebuff.Record(&before, experiencebuff.Effect{Kind: experiencebuff.Clear, BasisPoints: 10000, DurationMillis: 60000}, 7440, now); err != nil {
				t.Fatal(err)
			}
			if err := g.Character.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			cmd.UpdatedAt, cmd.ClearPotionBase = now, 10
			if saveFailure {
				g.CharacterSettlement = despairFailUOW{g.CharacterSettlement}
			}
			owner, _ := NewOwner(g)
			first, err := owner.CommitSettlement(ctx, cmd)
			if saveFailure {
				after, _, _ := g.Character.Load(ctx, "c")
				bag, _, _ := g.Inventory.Load(ctx, "c")
				if err == nil || !reflect.DeepEqual(after, before) || len(bag.Slots) != 0 {
					t.Fatalf("failed grant left potion gain, floor or assets: err=%v character=%+v", err, after)
				}
				return
			}
			if err != nil || first.ExperienceGain != 30 || first.PotionBonus != 10 || len(first.DespairItems) != 3 || first.Character.Stats[DespairHighestClearedStatKey] != 1 {
				t.Fatalf("combined settlement: result=%+v err=%v", first, err)
			}
			cmd.UpdatedAt = now.Add(time.Hour)
			cmd.DespairRewards.Items[0].Stack.ItemID = 999
			replay, err := owner.CommitSettlement(ctx, cmd)
			if err != nil || !replay.Replayed || replay.PotionBonus != 10 || replay.ExperienceGain != 30 || !reflect.DeepEqual(replay.DespairItems, first.DespairItems) || !reflect.DeepEqual(replay.Inventory, first.Inventory) || !reflect.DeepEqual(replay.Character, first.Character) {
				t.Fatalf("expired replay changed committed result: err=%v replay=%+v", err, replay)
			}
		})
	}
}
