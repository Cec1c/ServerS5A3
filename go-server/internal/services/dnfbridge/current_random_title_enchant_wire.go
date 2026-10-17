package dnfbridge

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// Current v115 op14 reads the full 0x77 row into sub_2436FE0. At
// 2437CA1 raw+0x61 is copied into record+0xAB; 27E12F4/27E130A
// read config/row as separate u16 values and call 3DB0620 with both keys.
const (
	currentRandomTitleEnchantConfigOffset = 0x61
	currentRandomTitleEnchantRowOffset    = 0x63
	currentRandomTitleEnchantFieldEnd     = currentRandomTitleEnchantRowOffset + 2
)

// Project after ordinary fields so synthesized and stored rows agree.
func overlayCurrentRandomTitleEnchantEntry(entry *currentItemListEntry, extra map[string]string) {
	if entry == nil || len(extra) == 0 {
		return
	}
	configID, configOK := currentRandomTitleEnchantExtraUint16(extra, "random_title_enchant_config_id")
	rowID, rowOK := currentRandomTitleEnchantExtraUint16(extra, "random_title_enchant_row_id")
	// Legacy worn summaries may retain an older random pair after an ordinary
	// card replacement. Explicit durable card metadata owns that title's enchant.
	if !(configOK && rowOK && configID != 0 && rowID != 0) && strings.EqualFold(strings.TrimSpace(extra["equipment_type"]), "[title name]") {
		card, err := strconv.ParseUint(strings.TrimSpace(extra["enchant_card_id"]), 0, 32)
		if err == nil && card != 0 {
			clear(entry.data[0x61:0x65])
			return
		}
	}
	if !configOK || !rowOK || configID == 0 || rowID == 0 {
		return
	}
	for index := 0; index < 4; index++ {
		entry.data[0x0e+index] = 0
	}
	entry.data[0x12] = 0
	binary.LittleEndian.PutUint16(entry.data[currentRandomTitleEnchantConfigOffset:currentRandomTitleEnchantRowOffset], configID)
	binary.LittleEndian.PutUint16(entry.data[currentRandomTitleEnchantRowOffset:currentRandomTitleEnchantFieldEnd], rowID)
}

func currentRandomTitleEnchantExtraUint16(extra map[string]string, key string) (uint16, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(extra[key]), 0, 16)
	return uint16(value), err == nil
}

func currentItemListEntryCarriesRandomTitleEnchant(entry currentItemListEntry) bool {
	return binary.LittleEndian.Uint16(entry.data[0x61:0x63]) != 0 && binary.LittleEndian.Uint16(entry.data[0x63:0x65]) != 0
}
