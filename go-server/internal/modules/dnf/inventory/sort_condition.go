package inventory

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Current native labels: 0 slot, 1 rarity, 2 level, 3 part/type, 4 expiry.
// Unsupported tab/condition pairs retain that tab's default ordering.
func sortAlgorithm(cmd Command) byte {
	if cmd.SourceListType == listTypeAvatar && cmd.Category == 8 {
		if cmd.Condition == 3 {
			return 3
		}
		return 1
	}
	if cmd.SourceListType != listTypeMain {
		return 0
	}
	switch cmd.Category {
	case 1:
		if cmd.Condition <= 3 {
			return cmd.Condition
		}
	case 2:
		if cmd.Condition == 4 {
			return 4
		}
		if cmd.Condition == 3 {
			return 5
		}
	case 3, 10:
		if cmd.Condition == 3 {
			return 5
		}
	}
	return 0
}

func orderSortCandidates(items []sortCandidate, cmd Command) error {
	algorithm := sortAlgorithm(cmd)
	keys := make(map[int16]alignedcmd.InventorySortKeys, len(items))
	if algorithm != 0 && algorithm != 4 {
		for _, item := range items {
			// Domain-only callers can use already resolved metadata. Production always
			// provides a PVF resolver; a failed lookup aborts before any slot is changed.
			key := alignedcmd.InventorySortKeys{StackableType: item.stack.Extra["stackable_type"], PartRank: math.MaxInt16}
			if cmd.SortKeys != nil {
				var err error
				key, err = cmd.SortKeys(item.stack)
				if err != nil {
					return fmt.Errorf("sort item %d: %w", item.stack.ItemID, err)
				}
			}
			key.StackableType = strings.ToLower(strings.Trim(strings.TrimSpace(key.StackableType), "`"))
			keys[item.slot] = key
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		ka, kb := keys[a.slot], keys[b.slot]
		switch algorithm {
		case 1:
			if x, y := sortRarityRank(ka.Rarity), sortRarityRank(kb.Rarity); x != y {
				return x > y
			}
		case 2:
			if ka.MinimumLevel != kb.MinimumLevel {
				return ka.MinimumLevel > kb.MinimumLevel
			}
		case 3:
			if ka.PartRank != kb.PartRank {
				return ka.PartRank < kb.PartRank
			}
		case 4:
			if x, y := sortExpiry(a.stack), sortExpiry(b.stack); x != y {
				return x < y
			}
		case 5:
			if ka.StackableType != kb.StackableType {
				return ka.StackableType < kb.StackableType
			}
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.stack.ItemID != b.stack.ItemID {
			return a.stack.ItemID < b.stack.ItemID
		}
		return a.slot < b.slot
	})
	return nil
}

func sortRarityRank(rarity int64) int64 {
	switch rarity {
	case 4:
		return 6
	case 6:
		return 5
	case 3:
		return 4
	case 5:
		return 3
	}
	return rarity
}

func sortExpiry(stack r.ItemStack) int64 {
	// Match the existing wire projection's persistent deadline precedence.
	for _, key := range []string{"expire_time", "expire_unix"} {
		if v, err := strconv.ParseUint(strings.TrimSpace(stack.Extra[key]), 10, 32); err == nil && v > 0 {
			return int64(v)
		}
	}
	if !stack.ExpireAt.IsZero() && stack.ExpireAt.Unix() > 0 {
		return stack.ExpireAt.Unix()
	}
	return math.MaxInt64
}
