package dnfbridge

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 装扮栏扩展券在结账时的应用规则：持久化的 avatar_list_param16 只能按
// 0,7,14..56 逐级推进（每个阶段 +7，与客户端档位表一致）。跳级、超档、以及
// 同样落在同一档位的重复购买都必须在结账时被拒绝——不能扣了点券却不生效。
func TestCurrentCeraShopPrepareContainerUpgradesAppliesAvatarStageBySeven(t *testing.T) {
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	record := newCharacterContainerStateSettings("1", now)
	avatarPurchase := func(stage uint16, commodity uint32) currentCeraShopResolvedPurchase {
		return currentCeraShopResolvedPurchase{
			Product:                     currentCeraShopProduct{CommodityID: commodity, ItemID: 2683901, Count: 1},
			AvatarInventoryUpgradeStage: stage,
		}
	}

	after, result, hasUpgrade, err := currentCeraShopPrepareContainerUpgrades(
		&record, true, "1", []currentCeraShopResolvedPurchase{avatarPurchase(1, 7001)}, now)
	if err != nil || !hasUpgrade {
		t.Fatalf("stage 1: err=%v hasUpgrade=%t", err, hasUpgrade)
	}
	if result.AvatarInventoryExpansion != 7 || after.Values["avatar_list_param16"] != "7" {
		t.Fatalf("stage 1 -> expansion=%d value=%q", result.AvatarInventoryExpansion, after.Values["avatar_list_param16"])
	}
	if after.Values["source"] != "current_exe_cera_shop_avatar_inventory_upgrade" {
		t.Fatalf("stage 1 source=%q", after.Values["source"])
	}
	if after.Values["avatar_inventory_last_upgrade_from"] != "0" || after.Values["avatar_inventory_last_upgrade_to"] != "7" {
		t.Fatalf("stage 1 audit from=%q to=%q",
			after.Values["avatar_inventory_last_upgrade_from"], after.Values["avatar_inventory_last_upgrade_to"])
	}
	// 推进后的记录必须仍然能通过容器状态校验（档位表 0..56 步长 7）。
	if _, err := dnfrepo.CharacterContainerStateFromSettings(after, "1"); err != nil {
		t.Fatalf("stage 1 produced an invalid container state: %v", err)
	}

	after2, result2, _, err := currentCeraShopPrepareContainerUpgrades(
		&after, true, "1", []currentCeraShopResolvedPurchase{avatarPurchase(2, 7002)}, now)
	if err != nil {
		t.Fatalf("stage 2: %v", err)
	}
	if result2.AvatarInventoryExpansion != 14 || after2.Values["avatar_list_param16"] != "14" {
		t.Fatalf("stage 2 -> expansion=%d value=%q", result2.AvatarInventoryExpansion, after2.Values["avatar_list_param16"])
	}

	// 跳级（当前 14 直接买阶段 4）与超档（阶段 9）都必须拒绝。
	for _, stage := range []uint16{4, 9} {
		if _, _, _, err := currentCeraShopPrepareContainerUpgrades(
			&after2, true, "1", []currentCeraShopResolvedPurchase{avatarPurchase(stage, 7003)}, now,
		); err == nil {
			t.Fatalf("stage %d must be rejected at avatar expansion %q",
				stage, after2.Values["avatar_list_param16"])
		}
	}

	// 主背包与装扮栏同一次结账里同时升级：两边都要落，source 标成容器升级。
	mixed, mixedResult, hasMixed, err := currentCeraShopPrepareContainerUpgrades(
		&record, true, "1", []currentCeraShopResolvedPurchase{
			{Product: currentCeraShopProduct{CommodityID: 7100, ItemID: 1, Count: 1}, MainInventoryUpgradeStage: 1},
			avatarPurchase(1, 7101),
		}, now)
	if err != nil || !hasMixed {
		t.Fatalf("mixed: err=%v hasMixed=%t", err, hasMixed)
	}
	if mixedResult.MainInventoryExpansion != 8 || mixedResult.AvatarInventoryExpansion != 7 {
		t.Fatalf("mixed result = %+v", mixedResult)
	}
	if mixed.Values["main_list_param16"] != "8" || mixed.Values["avatar_list_param16"] != "7" {
		t.Fatalf("mixed values main=%q avatar=%q", mixed.Values["main_list_param16"], mixed.Values["avatar_list_param16"])
	}
	if mixed.Values["source"] != "current_exe_cera_shop_container_upgrade" {
		t.Fatalf("mixed source=%q", mixed.Values["source"])
	}
}
