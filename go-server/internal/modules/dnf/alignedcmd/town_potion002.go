package alignedcmd

import "time"

// TownPotion002Definition is supplied only by the runtime PVF resolver. The
// mixed expert-town family is not an op44 debit-only consumable family.
type TownPotion002Definition struct {
	ExpirationDate time.Time
	ItemID         int64
	PVFPath        string
	MinimumLevel   int64
	CooldownMillis int64
	Buff           UpgradeRateBuffResolution
}
