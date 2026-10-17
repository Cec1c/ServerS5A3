package inventory

import (
	"encoding/hex"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// The durable cache is a second representation of the same current item row.
// Rebuild it before changing either enchant kind, rather than allowing stale
// random fields to reappear when an Extra-only instance is later projected.
func restoreEnchantRawCache(stack *dnfrepo.ItemStack) {
	if stack == nil || len(stack.RawEntry) != 0 || len(stack.Extra) == 0 {
		return
	}
	raw, err := hex.DecodeString(strings.TrimSpace(stack.Extra["raw_entry_hex"]))
	if err == nil && len(raw) == currentItemListEntrySize {
		stack.RawEntry = raw
	}
}
