package inventory

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func (o *Owner) equippedUpgradeBonus(ctx context.Context, characterID string, cmd Command) (alignedcmd.UpgradeEquippedBonus, error) {
	var total alignedcmd.UpgradeEquippedBonus
	if cmd.Mode != "reinforce" || cmd.UpgradeEquippedBonusResolver == nil {
		return total, nil
	}
	if o.equipment == nil {
		return total, fmt.Errorf("equipped upgrade bonus requires asset equipment owner")
	}
	worn, found, err := o.equipment.Load(ctx, characterID)
	if err != nil || !found {
		return total, err
	}
	now := time.Now().UTC()
	for _, slot := range []int16{13, 26} {
		entry, ok := worn.Entries[strconv.Itoa(int(slot))]
		if !ok || entry.ItemID <= 0 {
			continue
		}
		if entry.SlotIndex != 0 && entry.SlotIndex != slot {
			return total, fmt.Errorf("inconsistent equipped upgrade slot")
		}
		if !entry.ExpireAt.IsZero() && !now.Before(entry.ExpireAt) {
			continue
		}
		bonus, err := cmd.UpgradeEquippedBonusResolver(entry.ItemID)
		if err != nil {
			return total, err
		}
		if bonus.Slot != slot || (!bonus.ExpiresAt.IsZero() && !now.Before(bonus.ExpiresAt)) {
			continue
		}
		total.CompoundRate += bonus.CompoundRate
		total.AddWeight += bonus.AddWeight
		total.GoldDiscount += bonus.GoldDiscount
	}
	total.CompoundRate = min(100000, max(0, total.CompoundRate))
	total.AddWeight = min(100000, max(0, total.AddWeight))
	total.GoldDiscount = min(100, max(0, total.GoldDiscount))
	return total, nil
}

// Apply discounts before the existing group-price divisor or NPC conversion.
func discountedUpgradeQuote(q alignedcmd.UpgradeCostResolution, percent int) alignedcmd.UpgradeCostResolution {
	if percent <= 0 || !q.Resolved || q.GoldCost < 0 || q.ConversionCount < 0 {
		return q
	}
	base := q.NativeGoldCost
	if base <= 0 {
		base = q.GoldCost
	}
	percent = min(100, percent)
	base = base/100*int64(100-percent) + base%100*int64(100-percent)/100
	if q.ConversionItemID > 0 && q.ConversionUnitGold > 0 {
		q.ConversionCount = base / q.ConversionUnitGold
		if base%q.ConversionUnitGold != 0 {
			q.ConversionCount++
		}
		return q
	}
	divisor := max(int64(1), q.GoldDivisor)
	q.GoldCost = base / divisor
	if base%divisor != 0 {
		q.GoldCost++
	}
	return q
}
