package alignedcmd

import r "longheng.io/server/internal/modules/dnf/repository"

// InventorySortKeys are resolved from the active PVF, never from packet hints.
type InventorySortKeys struct {
	MinimumLevel, Rarity int64
	PartRank             int16
	StackableType        string
}

// AccountCargoMoveProjection freezes only the committed move endpoints. A nil
// Stack clears that row. It is returned only after the account/item transaction
// commits, and avoids reloading/replacing both containers after every op19.
type AccountCargoMoveProjection struct {
	AccountID, CharacterID string
	Rows                   []CommittedItemRow
}

type CommittedItemRow struct {
	ListType  byte
	SlotIndex int16
	Stack     *r.ItemStack
}
