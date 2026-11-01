package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
)

func TestGrantedRaidRewardDisplayDoesNotInheritPoolLimitFlag(t *testing.T) {
	for _, flag := range []byte{0, 1} {
		plan := currentAntonRewardPlan{Choice: raid.AntonRewardChoice{Flags: flag}, Rewards: []currentBoosterReward{
			{Definition: dungeonDropItemDefinition{ItemID: 600}, Count: 1},
			{Definition: dungeonDropItemDefinition{ItemID: 2681917}, Count: 2000},
		}}
		for _, kind := range []byte{1, 3} {
			items := currentAntonPartyDisplayItems(plan)
			body, err := raid.BuildRewardListBody(kind, []raid.RewardDisplayCard{{CharacterID: 19, CardType: 1, Items: items}})
			if err != nil || len(body) != 22 || body[13] != 0 || body[21] != 0 {
				t.Fatalf("pool flag=%d kind=%d native display=%x err=%v", flag, kind, body, err)
			}
			if items[0].ItemID != 600 || items[0].Quantity != 1 || items[1].ItemID != 2681917 || items[1].Quantity != 2000 || plan.Choice.Flags != flag {
				t.Fatal("display changed entitlement contents or configured pool")
			}
		}
	}
}

func TestGrantedRaidRewardDisplayStillUsesCommittedFateQuantity(t *testing.T) {
	for _, committed := range []uint32{0, 7} {
		plan := currentAntonRewardPlan{Choice: raid.AntonRewardChoice{Flags: 1}, FateGranted: &committed, Rewards: []currentBoosterReward{
			{Definition: dungeonDropItemDefinition{ItemID: raidcurrency.FateItemID}, Count: 45},
		}}
		items := currentAntonPartyDisplayItems(plan)
		if committed == 0 && len(items) != 0 {
			t.Fatal("zero granted currency displayed as a new reward")
		}
		if committed > 0 && (len(items) != 1 || items[0].Quantity != uint16(committed) || items[0].Flags != 0) {
			t.Fatal("committed currency quantity hidden or replaced", items)
		}
	}
}
