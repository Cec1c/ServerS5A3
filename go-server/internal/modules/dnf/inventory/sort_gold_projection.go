package inventory

import (
	"context"
	"math"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// sortRefreshGold is the authoritative wallet row read BEFORE owner.Sort
// commits. It deliberately carries no repository handle: once the sort has been
// persisted the response is built from this value alone, so a failed read can
// never follow a committed sort or leave the refresh without its wallet row.
type sortRefreshGold struct {
	ready  bool
	amount int64
}

// A native full main-list refresh replaces the client's wallet row as well as
// its item rows. Gold is authoritative in Character.Stats, not Inventory.Slots;
// match the login projection without persisting a synthetic inventory item.
func loadSortRefreshGold(ctx context.Context, characters dnfrepo.CharacterRepository, listType byte, characterID string) (sortRefreshGold, error) {
	if listType != listTypeMain || characters == nil {
		return sortRefreshGold{}, nil
	}
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return sortRefreshGold{}, err
	}
	if !found {
		return sortRefreshGold{}, nil
	}
	gold, known := character.Stats["gold"]
	if !known {
		return sortRefreshGold{}, nil
	}
	if gold < 0 {
		gold = 0
	} else if gold > math.MaxInt32 {
		gold = math.MaxInt32
	}
	return sortRefreshGold{ready: true, amount: gold}, nil
}

// applySortRefreshGold writes the prepared wallet row into the frozen op13
// snapshot. It performs no I/O. A real item occupying the reserved slot wins,
// exactly like native login.
func applySortRefreshGold(result SortResult, gold sortRefreshGold) SortResult {
	if !gold.ready || result.ListType != listTypeMain {
		return result
	}
	walletKey := slotKey(listTypeMain, 0)
	if row, exists := result.Refresh[walletKey]; exists && row.ItemID != 0 {
		// Preserve an actual conflicting item, just as native login does.
		return result
	}
	result.Refresh = cloneItemMap(result.Refresh)
	if result.Refresh == nil {
		result.Refresh = make(map[string]dnfrepo.ItemStack)
	}
	result.Refresh[walletKey] = dnfrepo.ItemStack{ItemID: 0, Count: gold.amount}
	return result
}

// projectSortRefreshGold keeps the earlier load-and-apply shape used by the
// existing boundary tests. Production sorts load the wallet before committing
// (see the Sort handler), so a read failure can never follow a persisted sort.
func projectSortRefreshGold(ctx context.Context, characters dnfrepo.CharacterRepository, result SortResult) (SortResult, error) {
	gold, err := loadSortRefreshGold(ctx, characters, result.ListType, result.CharacterID)
	if err != nil {
		return result, err
	}
	return applySortRefreshGold(result, gold), nil
}
