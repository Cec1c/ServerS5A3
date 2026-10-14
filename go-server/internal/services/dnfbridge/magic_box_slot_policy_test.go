package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/packageitem"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strconv"
	"testing"
)

func TestMagicBoxRewardResolverSharesQuickSlotEligibility(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	for _, id := range []int64{24, 2002, 2600014} {
		reward, err := resolveCurrentMagicBoxRewardItem(catalog, source, id)
		if err != nil {
			t.Fatal(err)
		}
		definition, err := catalog.ResolveItem(uint32(id))
		if err != nil {
			t.Fatal(err)
		}
		policy := currentPVFItemPlacementPolicy(definition)
		if reward.AllowQuickSlots != policy.AllowQuickSlots || reward.PreferQuickSlots != policy.PreferQuickSlots {
			t.Fatalf("魔盒与其他发放分类不同：item=%d reward=%+v policy=%+v", id, reward, policy)
		}
	}
}

func TestRealPVFMagicBoxRewardsUseSharedSlotPolicy(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置 DNFBRIDGE_REAL_PVF_SMOKE 后核对现场魔盒物品")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{8, 15, 3175, 101010855} {
		reward, err := resolveCurrentMagicBoxRewardItem(catalog, archive, id)
		if err != nil {
			t.Fatal(err)
		}
		if reward.TargetListType != 0 || !reward.AllowQuickSlots || reward.PreferQuickSlots != (id == 8 || id == 15) {
			t.Fatalf("现场物品分类不符：%+v", reward)
		}
		for _, full := range []bool{false, true} {
			name := strconv.FormatInt(id, 10)
			if full {
				name += "/分类页满"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				boxSlot := int16(9)
				if reward.Kind == "equipment" {
					boxSlot = 70
				}
				slots := map[string]dnfrepo.ItemStack{currentDungeonPickupMainSlotKey(boxSlot): {ItemID: 2682272, Count: 2}}
				if full {
					for slot := reward.SlotStart; slot <= reward.SlotEnd; slot++ {
						slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
					}
				}
				if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
					t.Fatal(err)
				}
				owner, err := packageitem.NewOwner(repos)
				if err != nil {
					t.Fatal(err)
				}
				// 固定抽取夹具只选择待核对奖励；类型、容器、上限与实例期限均取真实PVF。
				boxResolver := func(int64) (alignedcmd.MagicBoxResolution, error) {
					return alignedcmd.MagicBoxResolution{Kind: "random", Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: id, Count: 1, Weight: 1}}}}}, nil
				}
				rewardResolver := func(itemID int64) (alignedcmd.MagicBoxRewardItem, error) {
					return resolveCurrentMagicBoxRewardItem(catalog, archive, itemID)
				}
				result, err := owner.ApplyMagicBoxExpand(ctx, packageitem.MagicBoxExpandCommand{SelectedCharacterID: 77, RawListType: 4, ListType: 0, SlotIndex: boxSlot, BoxItemID: 2682272, MaterialSlotIndex: -1, OpenCount: 2}, boxResolver, rewardResolver)
				if err != nil || !result.Success {
					t.Fatalf("连开发放失败：%+v err=%v", result, err)
				}
				stored, _, err := repos.Inventory.Load(ctx, "77")
				if err != nil {
					t.Fatal(err)
				}
				want := "0:3"
				if id == 3175 && !full {
					want = "0:121"
				}
				if reward.Kind == "equipment" {
					first := int16(9)
					if full {
						first = 3
					}
					for slot := first; slot <= first+1; slot++ {
						got := stored.Slots[currentDungeonPickupMainSlotKey(slot)]
						if got.ItemID != id || got.Count != 1 || got.Extra["item_kind"] != "equipment" || got.Extra["quality_seed"] == "" {
							t.Fatalf("真实PVF装备没有独立入格：slot=%d stack=%+v", slot, got)
						}
					}
					if result.OverflowMailID != "" {
						t.Fatal("装备栏满时未先用空快捷栏")
					}
					return
				}
				if got := stored.Slots[want]; got.ItemID != id || got.Count != 2 {
					t.Fatalf("真实PVF魔盒落点不符：item=%d want=%s slots=%+v", id, want, stored.Slots)
				}
			})
		}
	}
}
