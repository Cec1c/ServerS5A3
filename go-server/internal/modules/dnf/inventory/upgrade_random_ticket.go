package inventory

import (
	"slices"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func upgradeTicketAllowsTarget(r alignedcmd.UpgradeTicketResolution, target dnfrepo.ItemStack) bool {
	if r.UsableItemIDs != nil && !slices.Contains(r.UsableItemIDs, target.ItemID) {
		return false
	}
	if r.MaterialUsableItemLevelSet {
		level := r.TargetMinimumLevel // Base PVF level; a wearable-level contract does not change this.
		if level < 0 || (r.MaterialUsableItemLevelMin >= 0 && level < r.MaterialUsableItemLevelMin) || (r.MaterialUsableItemLevelMax >= 0 && level > r.MaterialUsableItemLevelMax) {
			return false
		}
	}
	if !r.TicketRandom {
		return true
	}
	rule := r.RandomTicket
	if rule == nil || len(rule.Chances) == 0 || rule.Slot < 0 || rule.Slot > 3 || rule.Seal < 0 || rule.Seal > 2 {
		return false
	}
	if len(rule.Rarities) > 0 && !slices.Contains(rule.Rarities, r.TargetRarity) {
		return false
	}
	kind := strings.ToLower(strings.Trim(r.TargetEquipmentType, " `[]\t\r\n"))
	switch rule.Slot {
	case 1:
		if kind != "weapon" {
			return false
		}
	case 2:
		if !slices.Contains([]string{"coat", "shoulder", "pants", "shoes", "waist"}, kind) {
			return false
		}
	case 3:
		if !slices.Contains([]string{"amulet", "wrist", "ring"}, kind) {
			return false
		}
	}
	seal := upgradeTicketSealFlag(target)
	if (rule.Seal == 1 && seal != 1) || (rule.Seal == 2 && seal != 0) {
		return false
	}
	var total int64
	for _, chance := range rule.Chances {
		if chance.Level < 0 || chance.Level > 31 || chance.Weight < 0 || chance.Weight > 100000 {
			return false
		}
		total += chance.Weight
		if total > 100000 {
			return false
		}
	}
	return total > 0
}

// Binding is not sealing. Explicit first-equip removal and current seal metadata
// take precedence over a stale cached wire row.
func upgradeTicketSealFlag(s dnfrepo.ItemStack) byte {
	for _, key := range []string{"seal_removed_by_first_equip", "trade_locked_by_first_equip"} {
		if value := strings.TrimSpace(s.Extra[key]); value != "" && value != "0" && !strings.EqualFold(value, "false") {
			return 0
		}
	}
	for _, key := range []string{"seal_flag", "seal"} {
		if value, found := s.Extra[key]; found {
			flag, err := strconv.ParseUint(strings.TrimSpace(value), 10, 8)
			if err != nil || flag > 1 {
				return 255
			}
			return byte(flag)
		}
	}
	if len(s.RawEntry) == currentItemListEntrySize {
		return s.RawEntry[0x0d]
	}
	return 0
}

func rollUpgradeRandomTicket(rule *alignedcmd.UpgradeRandomTicket, roll int64) (int64, bool) {
	for _, chance := range rule.Chances {
		if roll < chance.Weight {
			return chance.Level, true
		}
		roll -= chance.Weight
	}
	return 0, false // Unassigned probability retains the old level, as with a fixed ticket.
}
