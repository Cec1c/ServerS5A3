package packageitem

import (
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// Recording at each actual map mutation avoids reloading or cloning all bags
// merely to discover a few changed slots. No projection escapes an error.
type magicBoxSlotChanges map[string]struct{}

func (c magicBoxSlotChanges) record(key string) { c[key] = struct{}{} }

func (c magicBoxSlotChanges) slots() []alignedcmd.ItemSlotRefresh {
	out := make([]alignedcmd.ItemSlotRefresh, 0, len(c))
	for key := range c {
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		list, listErr := strconv.ParseUint(parts[0], 10, 8)
		slot, slotErr := strconv.ParseInt(parts[1], 10, 16)
		if listErr != nil || slotErr != nil || slot < 0 {
			continue
		}
		out = append(out, alignedcmd.ItemSlotRefresh{ListType: byte(list), SlotIndex: int16(slot)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ListType != out[j].ListType {
			return out[i].ListType < out[j].ListType
		}
		return out[i].SlotIndex < out[j].SlotIndex
	})
	return out
}
