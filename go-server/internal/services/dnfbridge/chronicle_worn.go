package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"

	dnfchronicle "longheng.io/server/internal/modules/dnf/chronicle"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 按当前EXE投影后的槽找目标，保留旧初始穿戴行与现用槽号的区别，不回落到同号背包。
func currentWornChronicleTarget(record dnfrepo.EquipmentRecord, slot int16) (string, dnfrepo.ItemStack, error) {
	key := ""
	var result dnfrepo.ItemStack
	for candidate, equipped := range record.Entries {
		actorSlot, ok := currentEXEActorEquipmentSlot(equipped)
		if !ok || actorSlot != uint64(slot) || equipped.ItemID <= 0 {
			continue
		}
		if key != "" {
			return "", result, dnfchronicle.ErrTarget
		}
		entry, ok := currentItemListEntryFromEquipment(equipped)
		if !ok {
			return "", result, dnfchronicle.ErrTarget
		}
		extra := make(map[string]string, len(equipped.Extra))
		for name, value := range equipped.Extra {
			extra[name] = value
		}
		if _, explicit := extra["durability"]; !explicit {
			extra["durability"] = strconv.Itoa(int(binary.LittleEndian.Uint16(entry.data[0x0b:0x0d])))
		}
		extra["ext_data0"] = strconv.Itoa(int(entry.data[0x0a]))
		extra["value_a"] = strconv.FormatUint(uint64(binary.LittleEndian.Uint32(entry.data[0x0e:0x12])), 10)
		extra["byte_12"] = strconv.Itoa(int(entry.data[0x12]))
		extra["byte_13"] = strconv.Itoa(int(entry.data[0x13]))
		key = candidate
		result = dnfrepo.ItemStack{ItemID: equipped.ItemID, Count: 1, Bind: equipped.Bind,
			ExpireAt: equipped.ExpireAt, Extra: extra, RawEntry: append([]byte(nil), entry.data[:]...)}
	}
	if key == "" {
		return "", result, dnfchronicle.ErrTarget
	}
	return key, result, nil
}

func currentWornChronicleUpdate(equipped dnfrepo.EquipmentEntry, stack dnfrepo.ItemStack) dnfrepo.EquipmentEntry {
	equipped.Extra = stack.Extra
	entry := currentItemListEntryFromRaw(stack.RawEntry, stack.Extra)
	currentApplyChronicleOptionsToEntry(&entry, stack.Extra)
	if durability, ok := sceneInventoryExtraUint(stack.Extra, "durability"); ok && durability <= 65535 {
		entry.setUint16(0x0b, uint16(durability))
	}
	equipped.RawEntry = append([]byte(nil), entry.data[:]...)
	equipped.Extra["raw_entry_hex"] = hex.EncodeToString(equipped.RawEntry)
	return equipped
}
