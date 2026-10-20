package dnfbridge

import (
	"os"
	"sort"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealPVFCeraShopListsAvatarExpansionCoupons 回答"装扮栏扩展券到底买不买得到"：
// 结账时应用扩展（avatar_list_param16 = 阶段×7）只在**点券行购买**这条路上成立，
// 所以必须在真实 etc/cerashop.etc 里找到卖这个券的商品行（1..8 阶段 = 2683901..2683908）。
// 如果这里查不到阶段 1，玩家就只能靠 GM 发券，点了也不会扩容——那才是"扩展券无效"。
func TestRealPVFCeraShopListsAvatarExpansionCoupons(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the live cera shop catalog")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.products) == 0 {
		t.Fatal("live cera shop catalog is empty")
	}

	stageCommodity := map[uint16]uint32{}
	stageItem := map[uint16]uint32{}
	commodityIDs := make([]uint32, 0, len(catalog.products))
	for commodityID := range catalog.products {
		commodityIDs = append(commodityIDs, commodityID)
	}
	sort.Slice(commodityIDs, func(i, j int) bool { return commodityIDs[i] < commodityIDs[j] })
	for _, commodityID := range commodityIDs {
		product := catalog.products[commodityID]
		definition, resolveErr := catalog.items.ResolveItem(product.ItemID)
		if resolveErr != nil {
			continue
		}
		stage, isFamily := currentCeraShopAvatarInventoryUpgradeStage(definition)
		if !isFamily {
			continue
		}
		t.Logf("commodity=%d item=%d stage=%d count=%d cera=%d section=%q path=%s",
			commodityID, product.ItemID, stage, product.Count, product.CeraPrice, product.Section, definition.PVFPath)
		if stage == 0 {
			// 2683909..2683915：目录对、但超出当前客户端 0..56 档位表，购买时必须拒绝。
			continue
		}
		if _, dup := stageCommodity[stage]; !dup {
			stageCommodity[stage] = commodityID
			stageItem[stage] = product.ItemID
		}
	}

	if len(stageCommodity) == 0 {
		t.Fatal("live cera shop sells no avatar-inventory expansion coupon; a mailed coupon cannot expand anything")
	}
	if commodity, ok := stageCommodity[1]; !ok {
		t.Fatalf("live cera shop has no stage-1 coupon (stages on sale: %v)", stageCommodity)
	} else if stageItem[1] != 2683901 {
		t.Fatalf("stage 1 commodity=%d grants item=%d, want 2683901", commodity, stageItem[1])
	}
	stages := make([]int, 0, len(stageCommodity))
	for stage := range stageCommodity {
		stages = append(stages, int(stage))
	}
	sort.Ints(stages)
	t.Logf("avatar expansion stages on sale=%v (commodities=%v)", stages, stageCommodity)
}
