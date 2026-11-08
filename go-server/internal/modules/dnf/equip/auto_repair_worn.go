package equip

import (
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Called only after an existing automatic-repair trigger consumes one quota,
// inside the same asset transaction. Bag/warehouse items are not visited.
func repairAllWornDurability(record *dnfrepo.EquipmentRecord, triggerSlot int16, resolve alignedcmd.RepairCostResolver, now time.Time) ([]dnfrepo.EquipmentEntry, error) {
	var repaired []dnfrepo.EquipmentEntry
	for key, entry := range record.Entries {
		// The caller already validated and restored the trigger using its
		// own current/legacy row contract. Do not reinterpret that row.
		if key == entryKey(triggerSlot) {
			continue
		}
		if entry.ItemID <= 0 || entry.SlotIndex < 0 || entry.SlotIndex > 32 || key != entryKey(entry.SlotIndex) {
			continue
		}
		valid, err := durabilityEntryExpiryValid(entry, now)
		if err != nil {
			return nil, err
		}
		if !valid {
			continue
		}
		evidence, err := resolve(entry.ItemID)
		if err != nil {
			return nil, err
		}
		if evidence.MaxDurability <= 0 || (!RepairAllEligible(evidence.EquipmentType) && evidence.EquipmentType != "[support weapon]") {
			continue
		}
		if evidence.MaxDurability > 65535 {
			return nil, ErrDurabilityInvalid
		}
		offset := 10
		if len(entry.RawEntry) == currentEquipmentRawEntryWireSize {
			if !RepairDurabilityRawEntryIsCurrentRaw77(entry.RawEntry, entry.ItemID) {
				return nil, ErrDurabilityInvalid
			}
			offset = RepairDurabilityOffsetCurrentRaw77
		} else if (len(entry.RawEntry) != 43 && len(entry.RawEntry) != 82) || int16(entry.RawEntry[0]) != entry.SlotIndex || int64(binary.LittleEndian.Uint32(entry.RawEntry[1:5])) != entry.ItemID {
			return nil, ErrDurabilityInvalid
		}
		rawCurrent := binary.LittleEndian.Uint16(entry.RawEntry[offset : offset+2])
		current, err := durabilityEntryCurrent(entry, rawCurrent)
		if err != nil {
			return nil, err
		}
		if int64(current) > evidence.MaxDurability || int64(rawCurrent) > evidence.MaxDurability {
			return nil, ErrDurabilityInvalid
		}
		if int64(current) == evidence.MaxDurability && current == rawCurrent {
			continue
		}
		binary.LittleEndian.PutUint16(entry.RawEntry[offset:offset+2], uint16(evidence.MaxDurability))
		if entry.Extra == nil {
			entry.Extra = make(map[string]string)
		}
		entry.Extra["durability"] = strconv.FormatInt(evidence.MaxDurability, 10)
		for _, alias := range []string{"current_durability", "value_b_word"} {
			if _, exists := entry.Extra[alias]; exists {
				entry.Extra[alias] = entry.Extra["durability"]
			}
		}
		for _, alias := range []string{"raw_entry_hex", "equipment_raw_entry_hex"} {
			if _, exists := entry.Extra[alias]; exists {
				entry.Extra[alias] = hex.EncodeToString(entry.RawEntry)
			}
		}
		record.Entries[key] = entry
		repaired = append(repaired, entry)
	}
	sort.Slice(repaired, func(i, j int) bool { return repaired[i].SlotIndex < repaired[j].SlotIndex })
	return repaired, nil
}
