package inventory

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 仅对带明确初始PVF来源的旧槽号作已证映射；运行时行优先使用原位移动标记。
func wornEnchantEntry(record dnfrepo.EquipmentRecord, slot int16) (string, dnfrepo.EquipmentEntry, bool) {
	key := ""
	var found dnfrepo.EquipmentEntry
	for candidate, entry := range record.Entries {
		actor := entry.SlotIndex
		initial := strings.EqualFold(strings.TrimSpace(entry.Extra["source"]), "pvf_create_equipment_list")
		runtime := strings.TrimSpace(strings.ToLower(entry.Extra["current_exe_runtime_move"]))
		legacy := initial && (runtime == "" || runtime == "0" || runtime == "false" || runtime == "no" || runtime == "off")
		if legacy {
			switch {
			case actor == 11:
				actor = 12
			case actor >= 13 && actor <= 22:
				actor++
			case actor == 23:
				actor = 25
			default:
				continue
			}
		} else if value, err := strconv.ParseInt(entry.Extra["current_exe_equipment_type"], 10, 16); err == nil {
			actor = int16(value)
		}
		if actor != slot || entry.ItemID <= 0 {
			continue
		}
		if key != "" {
			return "", dnfrepo.EquipmentEntry{}, false
		}
		key, found = candidate, entry
	}
	return key, found, key != ""
}

// 穿戴端点必须与PVF部位一致；宠物保持原有独立事务路径。
func wornEnchantSlotMatches(slot int16, equipmentType string) bool {
	part := strings.ToLower(strings.Trim(strings.TrimSpace(equipmentType), "`[]"))
	parts := map[string]int16{"weapon": 12, "title name": 13, "coat": 14, "shoulder": 15, "pants": 16,
		"shoes": 17, "waist": 18, "amulet": 19, "wrist": 20, "ring": 21, "support": 22, "magic stone": 23, "earring": 25}
	want, ok := parts[part]
	return ok && (slot == want || (part == "weapon" && slot == 24))
}

// 同时支持现用119字节行及旧穿戴摘要，不扩展旧行、不改品级种子和其它属性。
func setWornEquipmentEnchantFields(entry dnfrepo.EquipmentEntry, card int64, upgrade byte) dnfrepo.EquipmentEntry {
	stack := forgingStackFromEquipmentEntry(entry)
	setEnchantFields(&stack, card, upgrade)
	entry.Extra = stack.Extra
	raw := append([]byte(nil), entry.RawEntry...)
	if len(raw) == currentItemListEntrySize && binary.LittleEndian.Uint32(raw[2:6]) == uint32(entry.ItemID) {
		binary.LittleEndian.PutUint32(raw[0x0e:0x12], uint32(card))
		raw[0x12] = upgrade
	} else if len(raw) >= 0x15 && len(raw) != currentItemListEntrySize {
		binary.LittleEndian.PutUint32(raw[0x10:0x14], uint32(card))
		raw[0x14] = upgrade
	}
	entry.RawEntry = raw
	if len(raw) != 0 {
		entry.Extra["raw_entry_hex"] = hex.EncodeToString(raw)
	}
	entry.Extra["enchant_card_id"] = strconv.FormatInt(card, 10)
	return entry
}
