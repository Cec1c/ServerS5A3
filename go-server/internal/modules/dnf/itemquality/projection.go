package itemquality

import (
	"encoding/binary"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// FromStack reads existing equipment quality without generating or persisting
// a seed. Login and inventory refreshes must use the same precedence: dedicated
// metadata, equipment-only legacy aliases, then a matching current-client row.
func FromStack(stack dnfrepo.ItemStack) uint32 {
	if seed := seedFromExtra(stack.Extra, "quality_seed"); Valid(seed) {
		return seed
	}
	if !strings.EqualFold(strings.TrimSpace(stack.Extra["item_kind"]), "equipment") {
		return 0
	}
	if seed := seedFromExtra(stack.Extra, "quality_seed", "amount_or_count", "count_or_instance"); Valid(seed) {
		return seed
	}
	if len(stack.RawEntry) == 0x77 && stack.ItemID > 0 &&
		binary.LittleEndian.Uint32(stack.RawEntry[2:6]) == uint32(stack.ItemID) {
		if seed := binary.LittleEndian.Uint32(stack.RawEntry[6:10]); Valid(seed) {
			return seed
		}
	}
	return 0
}

func seedFromExtra(extra map[string]string, keys ...string) uint32 {
	for _, key := range keys {
		value, err := strconv.ParseUint(strings.TrimSpace(extra[key]), 0, 64)
		if err != nil {
			continue
		}
		if value > uint64(^uint32(0)) {
			return 0
		}
		return uint32(value)
	}
	return 0
}
