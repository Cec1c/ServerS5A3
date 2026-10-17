package dungeon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func griefRewardFixture(t *testing.T) (dnfrepo.Group, SettlementCommand) {
	t.Helper()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.GriefClearedFloor = 1
	cmd.GriefRewards = &GriefRewards{}
	cmd.GriefRewards.Items[2] = CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 10160931, Count: 1}, Stackable: true, SlotStart: 121, SlotEnd: 152, StackLimit: 100}
	cmd.GriefRewards.Items[9] = CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 10303266, Count: 1}, Stackable: true, SlotStart: 65, SlotEnd: 96, StackLimit: 1}
	return repos, cmd
}

func TestGriefRewardsPreserveTenPositionsAndReplayOriginalReceipt(t *testing.T) {
	ctx := context.Background()
	repos, cmd := griefRewardFixture(t)
	owner, _ := NewOwner(repos)
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.GriefItems) != 10 || first.GriefItems[2].ItemID != 10160931 || first.GriefItems[9].ItemID != 10303266 || first.GriefItems[0].ItemID != 0 || first.GriefItems[0].Slot != -1 {
		t.Fatalf("cards=%+v", first.GriefItems)
	}
	if first.Character.Stats[GriefHighestClearedStatKey] != 1 || first.Inventory.Slots["0:121"].ItemID != 10160931 || first.Inventory.Slots["0:65"].ItemID != 10303266 {
		t.Fatal("reward and floor were not committed together")
	}
	cmd.GriefRewards.Items[2].Stack.ItemID = 999
	replayed, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.GriefItems, first.GriefItems) || !reflect.DeepEqual(replayed.Inventory, first.Inventory) {
		t.Fatalf("replay changed committed reward: %v", err)
	}
	cmd.GriefClearedFloor = 0
	cmd.GriefRewards = nil
	cmd.CompletionKey = "ordinary-after-grief"
	if ordinary, err := owner.CommitSettlement(ctx, cmd); err != nil || len(ordinary.GriefItems) != 0 {
		t.Fatalf("ordinary receipt used previous grief cards: %v", err)
	}
}

func TestGriefRewardsFullPageAndLateSaveFailureRollBackWholeClear(t *testing.T) {
	for _, name := range []string{"full consumables", "inventory save", "skill save after inventory and character"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos, cmd := griefRewardFixture(t)
			if name == "full consumables" {
				inv, _, _ := repos.Inventory.Load(ctx, "c")
				inv.Slots = map[string]dnfrepo.ItemStack{}
				for slot := 65; slot <= 96; slot++ {
					inv.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 444, Count: 1}
				}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			} else if name == "inventory save" {
				repos.CharacterSettlement = despairFailUOW{repos.CharacterSettlement}
			} else {
				repos.CharacterSettlement = griefFailSkillUOW{repos.CharacterSettlement}
			}
			before, _, _ := repos.Character.Load(ctx, "c")
			invBefore, _, _ := repos.Inventory.Load(ctx, "c")
			skillBefore, _, _ := repos.Skill.Load(ctx, "c")
			owner, _ := NewOwner(repos)
			_, err := owner.CommitSettlement(ctx, cmd)
			if err == nil || (name == "full consumables" && !errors.Is(err, ErrInventoryFull)) {
				t.Fatalf("err=%v", err)
			}
			after, _, _ := repos.Character.Load(ctx, "c")
			invAfter, _, _ := repos.Inventory.Load(ctx, "c")
			skillAfter, _, _ := repos.Skill.Load(ctx, "c")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(invBefore, invAfter) || !reflect.DeepEqual(skillBefore, skillAfter) {
				t.Fatal("failed clear partially saved reward, floor, experience, skill or receipt")
			}
		})
	}
}

func TestGriefRewardReceiptRejectsMissingAndCorruptEmptyCards(t *testing.T) {
	ctx := context.Background()
	repos, cmd := griefRewardFixture(t)
	owner, _ := NewOwner(repos)
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"0_item", "0_count", "0_slot", "9_slot", "floor", "version"} {
		stats := map[string]int64{}
		for k, v := range first.Character.Stats {
			stats[k] = v
		}
		delete(stats, griefRewardReceiptPrefix+field)
		if _, err := readGriefRewardReceipt(stats, 1, true); !errors.Is(err, ErrSettlementStateInvalid) {
			t.Fatalf("missing %s accepted: %v", field, err)
		}
	}
}
