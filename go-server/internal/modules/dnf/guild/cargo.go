package guild

import (
	"errors"
	"math"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const CargoPermission uint32 = 1 << 15

var ErrCargoUninitialized = errors.New("guild cargo capacity has not been established")

// A nil Cargo preserves legacy registry state without inventing an initial
// capacity. Initialization/migration must use the matching rules separately.
type Cargo struct {
	Capacity uint32                 `json:"capacity"`
	Slots    map[uint16]r.ItemStack `json:"slots,omitempty"`
	History  []CargoHistoryEntry    `json:"history,omitempty"`
}

func ValidateCargo(c *Cargo) error {
	if c == nil {
		return nil
	}
	// Slots are u16 on the wire. The lower PVF capacity limit is checked by
	// the operation's trusted rules, not frozen into the persistence format.
	if c.Capacity > math.MaxUint16+1 {
		return ErrInvalidRegistry
	}
	for slot, item := range c.Slots {
		if uint32(slot) >= c.Capacity || item.ItemID <= 0 || item.ItemID > math.MaxUint32 || item.Count <= 0 || item.Count > math.MaxUint32 {
			return ErrInvalidRegistry
		}
	}
	if len(c.History) > CargoHistoryLimit {
		return ErrInvalidRegistry
	}
	for _, entry := range c.History {
		if ValidateCargoHistoryEntry(entry) != nil {
			return ErrInvalidRegistry
		}
	}
	return nil
}
