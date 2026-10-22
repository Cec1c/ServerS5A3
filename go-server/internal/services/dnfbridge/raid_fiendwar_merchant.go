package dnfbridge

import "fmt"

const currentFiendWarMerchantNPC uint32 = 20433
const currentFiendWarMerchantShop uint32 = 400000000

// This port does not activate the dynamic merchant. Reject its explicit context
// and context-free exclusive goods before assets mutate. Goods also sold by a
// normal NPC continue through the existing shop pricing/ownership transaction.
func rejectCurrentFiendWarMerchantPurchase(shop *currentNPCShopCatalog, q currentNPCShopBuyRequest) error {
	if q.ActorContext == currentFiendWarMerchantNPC || q.ShopContext == currentFiendWarMerchantShop {
		return fmt.Errorf("超时空商人暂未开放")
	}
	if shop == nil {
		return fmt.Errorf("商店商品目录不可用")
	}
	if _, merchantProduct := shop.npcItems[currentFiendWarMerchantNPC][q.ItemID]; !merchantProduct {
		return nil
	}
	for npc, items := range shop.npcItems {
		if npc != currentFiendWarMerchantNPC {
			if _, shared := items[q.ItemID]; shared {
				return nil
			}
		}
	}
	return fmt.Errorf("超时空商人专属商品暂未开放")
}
