package equip

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// ProjectUpgradeFields overlays durable ordinary-equipment attributes on a
// current 0x77 item row. Missing values preserve the row; explicit zero clears
// stale cached attributes. Callers exclude avatars and creatures, whose bytes
// have different meanings. No persistent data or input metadata is modified.
func ProjectUpgradeFields(raw []byte, extra map[string]string) {
	if len(raw) != 119 {
		return
	}
	if value, ok := upgradeProjectionUint(extra, 8, "ext_data0", "ext0", "packed_flag_byte", "packed_flag", "packed"); ok {
		raw[10] = byte(value)
	} else if level, ok := upgradeProjectionUint(extra, 5, "reinforce", "upgrade_level"); ok {
		raw[10] = (raw[10] & 0xe0) | byte(level)
	}
	if value, ok := upgradeProjectionUint(extra, 8, "byte_13", "value_13", "value_c", "amplify_type", "amplification_type", "amplify_attr", "amplification_attr"); ok {
		raw[19] = byte(value)
	}
	if value, ok := upgradeProjectionUint(extra, 16, "marker_16", "marker16", "value_d", "amplify_value", "amplification_value", "amplify_bonus", "amplification_bonus"); ok {
		binary.LittleEndian.PutUint16(raw[20:22], uint16(value))
	}
}

func upgradeProjectionUint(extra map[string]string, bits int, keys ...string) (uint64, bool) {
	for _, key := range keys {
		text := strings.TrimSpace(extra[key])
		if text == "" {
			continue
		}
		value, err := strconv.ParseUint(text, 10, bits)
		if err == nil {
			return value, true
		}
	}
	return 0, false
}
