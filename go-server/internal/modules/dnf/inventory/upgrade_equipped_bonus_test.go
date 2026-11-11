package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestUpgradeEquippedProbabilityAndActualPayment(t *testing.T) {
	for _, tc := range []struct {
		name, mode          string
		pet, title, expired bool
		weight              int
		gold                int64
	}{
		{"bag does not count", "reinforce", false, false, false, 25000, 1000},
		{"equipped pet", "reinforce", true, false, false, 26000, 960},
		{"equipped title", "reinforce", false, true, false, 25250, 1000},
		{"both", "reinforce", true, true, false, 26250, 960},
		{"expired equipped pet", "reinforce", true, false, true, 25000, 1000},
		{"reinforce effects do not boost amplify", "amplify", true, true, false, 25000, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			saveTestCharacter(t, ctx, g, repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345}})
			saveTestInventory(t, ctx, g, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:9": protectTicketEquipment(tc.mode, 10), "0:10": {ItemID: 400990076, Count: 1, Extra: map[string]string{"increase upgrade compound rate": "999999"}}}})
			eq := repo.EquipmentRecord{CharacterID: "77", Entries: map[string]repo.EquipmentEntry{}}
			if tc.pet {
				eq.Entries["26"] = repo.EquipmentEntry{ItemID: 400990076, SlotIndex: 26}
				if tc.expired {
					e := eq.Entries["26"]
					e.ExpireAt = time.Now().Add(-time.Second)
					eq.Entries["26"] = e
				}
			}
			if tc.title {
				eq.Entries["13"] = repo.EquipmentEntry{ItemID: 26699, SlotIndex: 13}
			}
			if err := g.Equipment.Save(ctx, eq); err != nil {
				t.Fatal(err)
			}
			o, _ := NewOwner(g)
			cmd := NewUpgradeCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77}, UpgradeItemRequest{Mode: tc.mode, TargetSlotIndex: 9, TargetItemTemplateID: 700, MaterialSlotIndex: -1, OptionalTicketSlotIndex: -1})
			cmd.UpgradeSuccessWeight = 25000
			cmd.UpgradeEquippedBonusResolver = func(id int64) (alignedcmd.UpgradeEquippedBonus, error) {
				if id == 400990076 {
					return alignedcmd.UpgradeEquippedBonus{Slot: 26, CompoundRate: 400, GoldDiscount: 4}, nil
				}
				if id == 26699 {
					return alignedcmd.UpgradeEquippedBonus{Slot: 13, CompoundRate: 100}, nil
				}
				return alignedcmd.UpgradeEquippedBonus{}, errors.New("unrecognized equipment")
			}
			cmd.UpgradeCostResolver = func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
				return alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 1000, NativeGoldCost: 7000, GoldDivisor: 7}, nil
			}
			r, err := o.Upgrade(ctx, cmd)
			if err != nil || !r.Success {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if r.UpgradeSuccessWeight != tc.weight || r.GoldCost != tc.gold {
				t.Fatalf("weight=%d fee=%d want %d/%d", r.UpgradeSuccessWeight, r.GoldCost, tc.weight, tc.gold)
			}
			c, _, _ := g.Character.Load(ctx, "77")
			if c.Stats["gold"] != 12345-tc.gold {
				t.Fatalf("actual payment=%d", c.Stats["gold"])
			}
		})
	}
}

func TestUpgradeDiscountConversionAndRounding(t *testing.T) {
	q := alignedcmd.UpgradeCostResolution{Resolved: true, NativeGoldCost: 104001, ConversionItemID: 10158964, ConversionUnitGold: 100000, ConversionCount: 2}
	if got := discountedUpgradeQuote(q, 4); got.ConversionCount != 1 || got.GoldCost != 0 {
		t.Fatalf("discounted NPC conversion=%+v", got)
	}
	for _, percent := range []int{0, 4, 100} {
		bad := alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: -1}
		if got := discountedUpgradeQuote(bad, percent); !reflect.DeepEqual(got, bad) {
			t.Fatal("discount hid invalid quote")
		}
	}
}

func TestUpgradeBuffSourcesKeepTheirOwnDurations(t *testing.T) {
	t.Setenv(UpgradeRateBuffBoostEnv, "")
	now := time.Now().UTC()
	c := repo.CharacterRecord{Stats: map[string]int64{}}
	// A weak long-lived source must never extend the stronger 30-second source.
	recordUpgradeRateBuff(&c, rateBuffResolution(10007343, "reinforce", 10000, 30000), 10007343, now)
	recordUpgradeRateBuff(&c, rateBuffResolution(490006368, "reinforce", 5000, 1800000), 490006368, now.Add(time.Second))
	if b, _ := upgradeRateBuffActiveBonus(c, "reinforce", now.Add(31*time.Second)); b != 5000 {
		t.Fatalf("short stronger buff borrowed duration: %d", b)
	}
	// The inverse order must not shorten the long source, either.
	c = repo.CharacterRecord{Stats: map[string]int64{}}
	recordUpgradeRateBuff(&c, rateBuffResolution(1, "reinforce", 10000, 1800000), 1, now)
	recordUpgradeRateBuff(&c, rateBuffResolution(2, "reinforce", 10000, 30000), 2, now.Add(time.Second))
	if b, _ := upgradeRateBuffActiveBonus(c, "reinforce", now.Add(time.Minute)); b != 10000 {
		t.Fatal("short source shortened long source")
	}
	if b, _ := upgradeRateBuffActiveBonus(c, "amplify", now); b != 0 {
		t.Fatal("mode leakage")
	}
}
