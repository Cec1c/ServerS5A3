package guild

import (
	"errors"
	"maps"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrCargoHistory = errors.New("guild cargo event cannot be represented by the native history")

// A bounded recent view matching the 50 visible rows, not a durable replay
// journal or a claim about the original server's retention policy.
const CargoHistoryLimit = 50
const (
	CargoHistoryDeposit  byte = 1
	CargoHistoryWithdraw byte = 2
)

type CargoHistoryEntry struct {
	At            time.Time `json:"at"`
	CharacterID   string    `json:"character_id"`
	CharacterName string    `json:"character_name"`
	Kind          byte      `json:"kind"`
	// Snapshot at transfer time; Count is the quantity actually moved.
	Item r.ItemStack `json:"item"`
}

func ValidateCargoHistoryEntry(e CargoHistoryEntry) error {
	if (e.Kind != CargoHistoryDeposit && e.Kind != CargoHistoryWithdraw) ||
		e.At.Unix() <= 0 || e.At.Unix() > math.MaxInt32 || strings.TrimSpace(e.CharacterID) == "" ||
		strings.TrimSpace(e.CharacterName) == "" || !utf8.ValidString(e.CharacterName) || strings.ContainsRune(e.CharacterName, '\x00') ||
		e.Item.ItemID <= 0 || e.Item.ItemID > math.MaxUint32 || e.Item.Count <= 0 || e.Item.Count > math.MaxInt32 {
		return ErrCargoHistory
	}
	return nil
}

// AppendHistory must run in the same transaction as the transfer. Internal
// rearrangement is not a deposit/withdrawal and has no such history row.
func (c *Cargo) AppendHistory(e CargoHistoryEntry) error {
	if c == nil {
		return ErrCargoUninitialized
	}
	if err := ValidateCargoHistoryEntry(e); err != nil {
		return err
	}
	e.At = e.At.UTC()
	e.Item.RawEntry = append([]byte(nil), e.Item.RawEntry...)
	e.Item.Extra = maps.Clone(e.Item.Extra)
	c.History = append(c.History, e)
	if len(c.History) > CargoHistoryLimit {
		c.History = append([]CargoHistoryEntry(nil), c.History[len(c.History)-CargoHistoryLimit:]...)
	}
	return nil
}
