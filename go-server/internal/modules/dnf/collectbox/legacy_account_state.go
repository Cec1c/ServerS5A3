package collectbox

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// LegacyAccountState is the account-level JSON shape used before collectbox
// moved to the account inventory pseudo-container.
type LegacyAccountState struct {
	Boxes map[string]map[string]uint32 `json:"boxes"`
}

type LegacyRegistration struct {
	BoxIndex  int32
	SlotIndex int32
	ItemID    uint32
	EntryKey  string
}

// ParseLegacyAccountState converts the old {"boxes":{"5":{"0":item}}}
// payload into deterministic account-inventory rows. It validates every box
// and slot before returning any rows, so a corrupt account cannot be partially
// migrated.
func ParseLegacyAccountState(raw string) ([]LegacyRegistration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var state LegacyAccountState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("decode legacy collectbox state: %w", err)
	}
	rows := make([]LegacyRegistration, 0)
	for boxText, slots := range state.Boxes {
		box64, err := strconv.ParseInt(strings.TrimSpace(boxText), 10, 32)
		if err != nil || box64 <= 0 || box64 > int64(MaxBoxIndex) {
			return nil, fmt.Errorf("legacy collectbox has invalid box index %q", boxText)
		}
		for slotText, itemID := range slots {
			slot64, err := strconv.ParseInt(strings.TrimSpace(slotText), 10, 32)
			if err != nil || slot64 < 0 || slot64 > int64(MaxSlotIndex) {
				return nil, fmt.Errorf("legacy collectbox box %s has invalid slot index %q", boxText, slotText)
			}
			if itemID == 0 || uint64(itemID) > math.MaxInt32 {
				return nil, fmt.Errorf("legacy collectbox box %s slot %s has invalid item id %d", boxText, slotText, itemID)
			}
			boxIndex, slotIndex := int32(box64), int32(slot64)
			rows = append(rows, LegacyRegistration{
				BoxIndex:  boxIndex,
				SlotIndex: slotIndex,
				ItemID:    itemID,
				EntryKey:  RegisteredSlotKey(boxIndex, slotIndex),
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BoxIndex != rows[j].BoxIndex {
			return rows[i].BoxIndex < rows[j].BoxIndex
		}
		return rows[i].SlotIndex < rows[j].SlotIndex
	})
	return rows, nil
}
