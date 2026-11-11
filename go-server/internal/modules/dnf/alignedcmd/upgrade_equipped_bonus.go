package alignedcmd

import "time"

// Values are resolved from the server PVF, never from instance Extra or packets.
// CompoundRate uses 100 = 1%; AddWeight uses the legacy 100000 probability scale.
type UpgradeEquippedBonus struct {
	Slot         int16
	CompoundRate int
	AddWeight    int
	GoldDiscount int
	ExpiresAt    time.Time
}

type UpgradeEquippedBonusResolver func(itemID int64) (UpgradeEquippedBonus, error)
