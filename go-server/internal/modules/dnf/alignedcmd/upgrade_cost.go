package alignedcmd

// UpgradeCostResolution is a server-derived quote evaluated inside the asset
// transaction against the stored target identity and current upgrade level.
// Conversion material is additional to the normal upgrade-table material.
type UpgradeCostResolution struct {
	Resolved         bool
	GoldCost         int64
	ConversionItemID int64
	ConversionCount  int64
}

type UpgradeCostResolver func(mode string, targetItemID int64, currentLevel int, npcPresent bool, npcID uint16) (UpgradeCostResolution, error)
