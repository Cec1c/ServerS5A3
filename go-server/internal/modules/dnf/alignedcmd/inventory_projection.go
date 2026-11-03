package alignedcmd

// InventorySortKeys are resolved from the active PVF, never from packet hints.
type InventorySortKeys struct {
	MinimumLevel, Rarity int64
	PartRank             int16
	StackableType        string
}
