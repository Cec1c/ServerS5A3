package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
)

func currentBoosterSelectionHasEquipmentAttributes(rows []currentBoosterSelectionCandidate) bool {
	for _, row := range rows {
		if row.EquipmentUpgrade != 0 || row.EquipmentSeparate != 0 {
			return true
		}
	}
	return false
}

// Bind the server-parsed category metadata to the exact selected rewards.
// Request option bytes never choose reinforcement. Other placements, unresolved
// definitions and unsupported grade/random tables remain rejected.
func applyCurrentBoosterSelectionUpgrades(def currentBoosterDefinition, req currentBoosterOpenRequest, rewards []currentBoosterReward) error {
	if req.Kind != currentBoosterRequestSelection {
		return nil
	}
	selected, err := currentBoosterSelectedCandidates(def, req)
	if err != nil {
		return err
	}
	byID := make(map[uint32]currentBoosterSelectionCandidate, len(selected))
	for _, row := range selected {
		byID[row.ItemID] = row
	}
	for i := range rewards {
		row := byID[rewards[i].Definition.ItemID]
		level, forging := row.EquipmentUpgrade, row.EquipmentSeparate
		if level == 0 && forging == 0 {
			continue
		}
		if level > 31 || forging > 8 || rewards[i].Definition.Kind != dungeonDropItemEquipment || rewards[i].Placement != currentBoosterRewardMain {
			return errCurrentBoosterPVFInvalid
		}
		rewards[i].EquipmentUpgrade = level
		rewards[i].EquipmentSeparate = forging
	}
	return nil
}

func currentBoosterUpgradedStack(key string, stack dnfrepo.ItemStack, level, forging byte) (dnfrepo.ItemStack, error) {
	list, slot, ok := parseSceneInventorySlotKey(key)
	if !ok || list != dnfrepo.MainInventoryListType || (level == 0 && forging == 0) || level > 31 || forging > 8 || stack.Extra["item_kind"] != "equipment" || stack.Count != 1 {
		return dnfrepo.ItemStack{}, errCurrentBoosterPVFInvalid
	}
	// Use the same authoritative equipment projection as op13/op14. Keep the
	// high flag bits, generated quality seed, durability, seal and expiry bytes.
	entry := currentItemListEntryFromStack(list, slot, stack)
	packed := (entry.data[0x0a] & 0xe0) | (level & 0x1f)
	entry.data[0x0a] = packed
	entry.data[0x57] = forging
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	stack.Extra["ext_data0"] = strconv.Itoa(int(packed))
	stack.Extra["packed_flag_byte"] = strconv.Itoa(int(packed))
	stack.Extra["reinforce"] = strconv.Itoa(int(level))
	stack.Extra["upgrade_level"] = strconv.Itoa(int(level))
	stack.Extra["byte_57"] = strconv.Itoa(int(forging))
	stack.Extra["value_57"] = strconv.Itoa(int(forging))
	return stack, nil
}
