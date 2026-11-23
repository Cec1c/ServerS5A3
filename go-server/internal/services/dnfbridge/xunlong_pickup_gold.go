package dnfbridge

import (
	"math"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The paired private PVF's 100341606 support item promises +30% dungeon
// gold in both descriptions but declares no native gold modifier. Apply that
// one rule to newly credited ground gold; never to wallet snapshots, replay,
// discarded items, mail, trades, or quest rewards.
func currentXunlongPickupGold(equipment dnfrepo.EquipmentRecord, amount uint32, now time.Time) (uint32, error) {
	entry, worn := equipment.Entries["22"]
	if !worn || entry.SlotIndex != 22 || entry.ItemID != 100341606 ||
		(!entry.ExpireAt.IsZero() && !entry.ExpireAt.After(now)) {
		return amount, nil
	}
	value := uint64(amount) * 130 / 100
	if value > math.MaxInt32 {
		return 0, errDungeonPickupResponseInvalid
	}
	return uint32(value), nil
}
