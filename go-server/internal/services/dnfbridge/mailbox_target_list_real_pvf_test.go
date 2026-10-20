package dnfbridge

import (
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 国庆光环是创建期两孔（[emblem socket default]，与 cera_shop_grant.go 同一条规则）。
// 以邮件方式发放/领取时，dnfbridge 必须给出带孔的条目模板并指向装扮栏 list 1，
// 否则玩家收到的光环在客户端"徽章皮肤没有徽章孔"。
func TestRealPVFMailboxAvatarResolutionKeepsAuraSockets(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify mailbox avatar sockets")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	booster, err := resolveCurrentBoosterDefinition(catalog, 490701319, currentBoosterRequestSelection)
	if err != nil {
		t.Skipf("National Day aura box is unavailable in this PVF: %v", err)
	}
	checked := 0
	for _, candidate := range booster.Selection {
		definition, resolveErr := catalog.ResolveItem(candidate.ItemID)
		if resolveErr != nil {
			continue
		}
		listType, slotStart, slotEnd, socketData, ok := currentMailboxAvatarResolution(catalog, definition)
		if !ok {
			continue
		}
		if listType != currentExeInventoryAvatarListType {
			t.Fatalf("avatar item=%d target list=%d, want %d", candidate.ItemID, listType, currentExeInventoryAvatarListType)
		}
		if slotStart != 0 || slotEnd < slotStart {
			t.Fatalf("avatar item=%d slots=%d..%d", candidate.ItemID, slotStart, slotEnd)
		}
		if currentAvatarSocketOpenCountFromBytes(socketData) == 0 {
			t.Fatalf("aura item=%d has no creation-time socket vector", candidate.ItemID)
		}
		checked++
		break
	}
	if checked == 0 {
		t.Skip("no avatar-family candidate in the live National Day pool")
	}
}

// 邮件领取的目标容器必须跟 PVF 的 stackable 类型一致：[creature]（宠物更名卡）
// 与 [feed] 属于宠物背包 list 7 的消耗品段，放进主背包玩家既看不到也用不了
// （rename.go 只接受 list 7）。这条真实 PVF 回归钉住该映射，避免以后又被改回主背包。
func TestRealPVFMailboxTargetListForPetFamilies(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the runtime pet-container mapping")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		itemID   uint32
		wantList byte
		wantLow  int16
		wantHigh int16
	}{
		{itemID: 25, wantList: currentPetInventoryListType, wantLow: currentCeraShopPetConsumableSlotStart, wantHigh: currentCeraShopPetConsumableSlotEnd},
		{itemID: 24, wantList: currentPetInventoryListType, wantLow: currentCeraShopPetConsumableSlotStart, wantHigh: currentCeraShopPetConsumableSlotEnd},
		{itemID: 490701730, wantList: 0, wantLow: 65, wantHigh: 120},
	} {
		definition, err := catalog.ResolveItem(test.itemID)
		if err != nil {
			t.Fatalf("item=%d resolve: %v", test.itemID, err)
		}
		resolution, err := mailboxItemResolutionFromPVF(definition, false)
		if err != nil {
			t.Fatalf("item=%d resolution: %v", test.itemID, err)
		}
		if resolution.TargetListType != test.wantList ||
			resolution.SlotStart != test.wantLow ||
			resolution.SlotEnd != test.wantHigh {
			t.Fatalf("item=%d type=%q target_list=%d slots=%d..%d, want list=%d slots=%d..%d",
				test.itemID, definition.StackableType, resolution.TargetListType,
				resolution.SlotStart, resolution.SlotEnd,
				test.wantList, test.wantLow, test.wantHigh)
		}
	}
}
