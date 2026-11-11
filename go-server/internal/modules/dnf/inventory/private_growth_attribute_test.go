package inventory

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Independent client enum: vitality=1, spirit=2, strength=3, intelligence=4.
func TestPrivateGrowthUnifiedBookThenUseAndReload(t *testing.T) {
	for attr := byte(1); attr <= 4; attr++ {
		for _, targetAttr := range []byte{0, 1, 2, 3, 4} {
			t.Run(fmt.Sprintf("book=%d/target=%d", attr, targetAttr), func(t *testing.T) {
				g, owner, q := growthFixture(t, 990033101, 0)
				ctx := context.Background()
				before := loadTestInventory(t, ctx, g, "77")
				before.Slots["0:12"] = repo.ItemStack{ItemID: 1286, Count: 1}
				if targetAttr != 0 {
					setAmplifyState(&q.Target, targetAttr, 6)
				}
				before.Slots["0:10"] = q.Target
				saveTestInventory(t, ctx, g, before)
				resolution := validAmplifyResolution()
				resolution.InvestOption = amplifyOptionAll
				resolution.InvestMaterialCount = 1
				cmd := investAmplifyCommand(investAmplifyActionInvest, attr, 1286)
				cmd.TargetItemTemplateID = 990033101
				result, err := owner.InvestAmplifyOption(ctx, cmd, staticAmplifyResolver(resolution))
				if err != nil || !result.Success || result.AmplifyType != attr {
					t.Fatalf("real book failed: %+v %v", result, err)
				}
				bag := loadTestInventory(t, ctx, g, "77")
				if _, exists := bag.Slots["0:12"]; exists {
					t.Fatal("book not consumed")
				}
				q.Ticket = bag.Slots["0:9"]
				setUpgradeLevel(&q.Ticket, 12)
				bag.Slots["0:9"] = q.Ticket
				saveTestInventory(t, ctx, g, bag)
				mode, actual, valid := GrowthTicketMode(q.Ticket)
				if !valid || mode != "amplify" || actual != attr {
					t.Fatal("instance state lost")
				}
				out, err := owner.ApplyGrowthTicket(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				stored := loadTestInventory(t, ctx, g, "77")
				if _, exists := stored.Slots["0:9"]; exists {
					t.Fatal("coupon survived")
				}
				if !reflect.DeepEqual(out, stored.Slots["0:10"]) || out.RawEntry[0x13] != attr || out.RawEntry[0x14] != 9 || upgradeLevelOf(out) != 12 || out.RawEntry[0x57] != 6 || out.Extra["custom_keep"] != "unchanged" {
					t.Fatalf("attribute transfer lost: %+v", out)
				}
				for _, key := range []string{"amplify_type", "amplification_type", "byte_13", "value_13", "value_c"} {
					if out.Extra[key] != fmt.Sprint(attr) {
						t.Fatal("stale alias", key)
					}
				}
			})
		}
	}
}

func TestPrivateGrowthUnifiedNewGrantsAlwaysWhite(t *testing.T) {
	for id := int64(990033101); id <= 990033105; id++ {
		stack := repo.ItemStack{ItemID: id, Count: 1, RawEntry: make([]byte, 119)}
		if err := InitializeGrowthTicket(&stack, 7); err != nil {
			t.Fatal(err)
		}
		mode, attr, ok := GrowthTicketMode(stack)
		if !ok || mode != "reinforce" || attr != 0 || stack.RawEntry[0x14] != 0 || GrowthTicketLevel(stack) != 0 {
			t.Fatalf("new grant has book effect: %+v", stack)
		}
	}
}

func TestPrivateGrowthUnifiedReinforcedNeedsGoldBook(t *testing.T) {
	for _, gold := range []bool{false, true} {
		t.Run(fmt.Sprint(gold), func(t *testing.T) {
			g, owner, _ := growthFixture(t, 990033101, 12)
			ctx := context.Background()
			action, item := investAmplifyActionInvest, int32(1286)
			if gold {
				action, item = investAmplifyActionPureGold, 8238
			}
			before := loadTestInventory(t, ctx, g, "77")
			before.Slots["0:12"] = repo.ItemStack{ItemID: int64(item), Count: 1}
			saveTestInventory(t, ctx, g, before)
			resolution := validAmplifyResolution()
			resolution.InvestOption, resolution.PureGoldOption = amplifyOptionAll, amplifyOptionAll
			resolution.InvestMaterialCount, resolution.PureGoldMaterialCount = 1, 1
			resolution.PureGoldLevels = []alignedcmd.AmplifyWeightedLevel{{Level: 9, Weight: 1}}
			cmd := investAmplifyCommand(action, 3, item)
			cmd.TargetItemTemplateID = 990033101
			out, err := owner.InvestAmplifyOption(ctx, cmd, staticAmplifyResolver(resolution))
			if err != nil || out.Success != gold {
				t.Fatalf("wrong conversion: %+v %v", out, err)
			}
			after := loadTestInventory(t, ctx, g, "77")
			if !gold && !reflect.DeepEqual(before, after) {
				t.Fatal("rejected pure book changed inventory")
			}
			if gold {
				mode, attr, ok := GrowthTicketMode(after.Slots["0:9"])
				if !ok || mode != "amplify" || attr != 3 || GrowthTicketLevel(after.Slots["0:9"]) != 9 {
					t.Fatal("gold book state lost")
				}
				if _, exists := after.Slots["0:12"]; exists {
					t.Fatal("gold book not consumed")
				}
			}
		})
	}
}

func TestPrivateGrowthUnifiedTrainingUsesInstanceAttribute(t *testing.T) {
	for attr := byte(1); attr <= 4; attr++ {
		g, owner, q := growthFixture(t, 990033101, 3)
		ctx := context.Background()
		setAmplifyState(&q.Ticket, attr, 7)
		bag := loadTestInventory(t, ctx, g, "77")
		bag.Slots["0:9"] = q.Ticket
		bag.Slots["0:121"] = repo.ItemStack{ItemID: 9100, Count: 1, Extra: map[string]string{"item_kind": "stackable"}}
		saveTestInventory(t, ctx, g, bag)
		r := alignedcmd.UpgradeTicketResolution{TicketMode: "amplify", TargetKind: "equipment", TargetMinimumLevel: 90, TargetLevel: 7, SuccessWeight: 100000}
		out, err := owner.UpgradeTicket(ctx, newUpgradeTicketCommand("amplify", 9, 990033101, 121), staticUpgradeTicketResolver(r))
		if err != nil || !out.Success {
			t.Fatalf("training failed: %+v %v", out, err)
		}
		stack := loadTestInventory(t, ctx, g, "77").Slots["0:9"]
		if stack.RawEntry[0x13] != attr || upgradeLevelOf(stack) != 7 {
			t.Fatal("training reset instance attribute")
		}
	}
}

func TestPrivateGrowthUnifiedNormalAmplifyKeepsInstanceAttribute(t *testing.T) {
	for attr := byte(1); attr <= 4; attr++ {
		g, owner, q := growthFixture(t, 990033101, 3)
		ctx := context.Background()
		seedUpgradeLateFailureWalletFixture(t, ctx, g)
		bag := loadTestInventory(t, ctx, g, "77")
		setAmplifyState(&q.Ticket, attr, 7)
		bag.Slots["0:9"] = q.Ticket
		saveTestInventory(t, ctx, g, bag)
		out, err := owner.Upgrade(ctx, Command{Mode: "amplify", AccountID: "acc", SelectedCharacterID: 77, TargetSlotIndex: 9, TargetItemTemplateID: 990033101, MaterialSlotIndex: 10, OptionalTicketSlot: -1, UpgradeSuccessWeight: 100000, UpgradeMaterialItemID: 3242, UpgradeMaterialCount: 10})
		if err != nil || !out.Success {
			t.Fatalf("normal amplify failed: %+v %v", out, err)
		}
		stack := loadTestInventory(t, ctx, g, "77").Slots["0:9"]
		if stack.RawEntry[0x13] != attr || upgradeLevelOf(stack) != 4 {
			t.Fatal("normal amplify reset state")
		}
	}
}
