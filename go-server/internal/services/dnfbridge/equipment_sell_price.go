package dnfbridge

import (
	"fmt"
	"math"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Equipment value is a pricing base, not the amount paid by an NPC. Keep
// selling and disassembly on the same PVF rate and integer rounding.
func loadCurrentEquipmentSellRate(source dnfpvf.Source) (int64, error) {
	table, err := parseDungeonCardPVFDocument(source, currentNPCShopPriceTablePath)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", errCurrentNPCShopCatalogUnavailable, currentNPCShopPriceTablePath, err)
	}
	rates := table.Ints("rate")
	if len(rates) == 0 || rates[0] <= 0 || rates[0] > math.MaxInt32 {
		return 0, fmt.Errorf("%w: %s [rate] is invalid", errCurrentNPCShopCatalogUnavailable, currentNPCShopPriceTablePath)
	}
	return rates[0], nil
}

func currentEquipmentSellGold(base, ratePermille int64) (int64, error) {
	if base < 0 || ratePermille <= 0 || ratePermille > math.MaxInt32 || base > math.MaxInt64/ratePermille {
		return 0, fmt.Errorf("%w: invalid equipment sell price", errCurrentNPCShopProductUnavailable)
	}
	return max(int64(1), base*ratePermille/1000), nil
}
