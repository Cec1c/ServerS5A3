package inventory

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func randomArmorTicket() alignedcmd.UpgradeTicketResolution {
	return alignedcmd.UpgradeTicketResolution{TicketMode: "reinforce", TicketRandom: true, TargetKind: "equipment", TargetRarity: 1, TargetMinimumLevel: 90, TargetEquipmentType: "[coat]", RandomTicket: &alignedcmd.UpgradeRandomTicket{
		Rarities: []int64{0, 1, 2, 3, 5, 6}, Slot: 2, Seal: 2,
		Chances: []alignedcmd.UpgradeTicketChance{{Level: 6, Weight: 15285}, {Level: 7, Weight: 30805}, {Level: 8, Weight: 32150}, {Level: 9, Weight: 11201}, {Level: 10, Weight: 8600}, {Level: 11, Weight: 1440}, {Level: 12, Weight: 437}, {Level: 13, Weight: 82}},
	}}
}

func TestRandomTicketWeightedIntervals(t *testing.T) {
	rule := randomArmorTicket().RandomTicket
	counts := map[int64]int{}
	for roll := int64(0); roll < 100000; roll++ {
		level, ok := rollUpgradeRandomTicket(rule, roll)
		if !ok {
			t.Fatalf("unassigned roll %d", roll)
		}
		counts[level]++
	}
	want := map[int64]int{6: 15285, 7: 30805, 8: 32150, 9: 11201, 10: 8600, 11: 1440, 12: 437, 13: 82}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("weights=%v", counts)
	}
	if _, ok := rollUpgradeRandomTicket(&alignedcmd.UpgradeRandomTicket{Chances: []alignedcmd.UpgradeTicketChance{{Level: 10, Weight: 50000}}}, 50000); ok {
		t.Fatal("unassigned probability succeeded")
	}
}

func TestRandomTicketRestrictionsAndSingleConsumption(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*alignedcmd.UpgradeTicketResolution, *dnfrepo.ItemStack)
		allowed bool
	}{
		{"level90 unsealed armor", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) { s.Bind = true }, true},
		{"wrong part", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) { r.TargetEquipmentType = "[weapon]" }, false},
		{"epic excluded", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) { r.TargetRarity = 4 }, false},
		{"sealed", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) { s.RawEntry[13] = 1 }, false},
		{"explicit unsealed", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) {
			s.RawEntry[13] = 1
			s.Extra["seal_flag"] = "0"
		}, true},
		{"first equipped", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) {
			s.RawEntry[13] = 1
			s.Extra["seal_flag"] = "1"
			s.Extra["seal_removed_by_first_equip"] = "1"
		}, true},
		{"malformed seal", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) { s.Extra["seal_flag"] = "invalid" }, false},
		{"contract cannot lower base level", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) {
			r.MaterialUsableItemLevelSet = true
			r.MaterialUsableItemLevelMin = 1
			r.MaterialUsableItemLevelMax = 80
			s.Extra["required_level"] = "80"
		}, false},
		{"base level inclusive", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) {
			r.MaterialUsableItemLevelSet = true
			r.MaterialUsableItemLevelMin = 90
			r.MaterialUsableItemLevelMax = 90
		}, true},
		{"wrong allowed ID", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) { r.UsableItemIDs = []int64{701} }, false},
		{"empty explicit list", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) { r.UsableItemIDs = []int64{} }, false},
		{"invalid random weights", func(r *alignedcmd.UpgradeTicketResolution, s *dnfrepo.ItemStack) {
			r.RandomTicket.Chances[0].Weight = 100001
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			saveUpgradeTicketFixture(t, ctx, repos, nil, 14, 1)
			r := randomArmorTicket()
			record := loadTestInventory(t, ctx, repos, "77")
			target := record.Slots[slotKey(listTypeMain, 9)]
			tc.change(&r, &target)
			record.Slots[slotKey(listTypeMain, 9)] = target
			saveTestInventory(t, ctx, repos, record)
			before := loadTestInventory(t, ctx, repos, "77")
			owner, _ := NewOwner(repos)
			result, err := owner.UpgradeTicket(ctx, newUpgradeTicketCommand("reinforce", 9, 700, 121), staticUpgradeTicketResolver(r))
			if err != nil || result.Success != tc.allowed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			after := loadTestInventory(t, ctx, repos, "77")
			if !tc.allowed {
				if !reflect.DeepEqual(before, after) || result.ErrorCode != 19 {
					t.Fatal("rejection mutated assets or wrong code")
				}
				return
			}
			if result.NewLevel < 6 || result.NewLevel > 13 || !result.UpgradeSucceeded {
				t.Fatalf("random result=%+v", result)
			}
			if _, exists := after.Slots[slotKey(listTypeMain, 121)]; exists {
				t.Fatal("ticket not consumed")
			}
			if after.Slots[slotKey(listTypeMain, 9)].Count != 1 {
				t.Fatal("target destroyed")
			}
			replay, err := owner.UpgradeTicket(ctx, newUpgradeTicketCommand("reinforce", 9, 700, 121), staticUpgradeTicketResolver(r))
			if err != nil || replay.Success || !reflect.DeepEqual(after, loadTestInventory(t, ctx, repos, "77")) {
				t.Fatal("empty-ticket replay mutated assets")
			}
		})
	}
}
