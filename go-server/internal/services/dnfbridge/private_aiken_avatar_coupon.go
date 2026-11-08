package dnfbridge

import (
	"fmt"
	"strings"
)

// The shelf has a private price, but avatar checkout and the native client
// identify permanent advanced-avatar vouchers by the original item number.
// Resolve the delivered definition separately; never change the original price.
func privateAikenAvatarCouponGrant(items *pvfDungeonDropCatalog, pricing currentNPCShopPricing, request currentNPCShopBuyRequest) (currentNPCShopPricing, currentNPCShopBuyRequest, error) {
	if request.ItemID != 990031104 {
		return pricing, request, nil
	}
	if request.ShopContext != 247 || (request.ActorContext != 0 && request.ActorContext != 486) ||
		!pricing.MaterialExchange || pricing.NeedMaterialItem != 10161538 || pricing.NeedMaterialCount != 40 || pricing.ExchangeGold != 0 ||
		pricing.Definition.PVFPath != "stackable/private_aiken_exchange/990031104.stk" {
		return pricing, request, fmt.Errorf("%w: private Aiken avatar voucher shelf or price mismatch", errCurrentNPCShopProductUnavailable)
	}
	const deliveredID = 2681593
	definition, err := items.ResolveItem(deliveredID)
	if err != nil {
		return pricing, request, err
	}
	if definition.Kind != dungeonDropItemStackable ||
		strings.ToLower(definition.PVFPath) != "stackable/cash/chn_avatar_gift_certificate/chn_gift_certificate_upper_avatar_unlimited.stk" ||
		!definition.ExpirationDate.IsZero() || definition.UsablePeriodDays != 0 || definition.DailyDeleteItem {
		return pricing, request, fmt.Errorf("%w: original permanent advanced avatar voucher mismatch", errCurrentNPCShopProductUnavailable)
	}
	pricing.Definition = definition
	request.ItemID = deliveredID
	return pricing, request, nil
}
