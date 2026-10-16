package guild

import (
	"errors"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrMileageOverflow = errors.New("guild mileage exceeds native uint32 balance")
var ErrMileageHistory = errors.New("guild mileage event cannot be represented by the native history")

// This is a bounded recent-history view, not a replay/deduplication journal.
// Future automatic grants must keep their durable event receipts separately.
const MileageHistoryLimit = 50
const MileageHistoryItem byte = 8

type MileageHistoryEntry struct {
	At            time.Time `json:"at"`
	CharacterID   string    `json:"character_id"`
	CharacterName string    `json:"character_name"`
	Kind          byte      `json:"kind"`
	Amount        uint32    `json:"amount"`
}

// GrantItemMileage applies the PVF [add guildmileage] reward to the guild pool.
// The caller must debit the physical item in the same GuildInventory UoW.
// This action does not award member mileage, hongbao contribution or guild EXP.
func (r *Registry) GrantItemMileage(owner dnfrepo.GuildOwner, amount uint32, name string, now time.Time) (*Guild, error) {
	g, m, found := r.GuildForCharacter(owner.CharacterID)
	if !found || g == nil {
		return nil, ErrNotMember
	}
	if m.AccountID != owner.AccountID {
		return nil, ErrPermissionDenied
	}
	if amount == 0 || amount > ^uint32(0)-g.Mileage {
		return nil, ErrMileageOverflow
	}
	// The history renderer uses a signed decimal delta and localtime32.
	if amount > math.MaxInt32 || now.Unix() <= 0 || now.Unix() > math.MaxInt32 || strings.TrimSpace(name) == "" || strings.ContainsRune(name, '\x00') {
		return nil, ErrMileageHistory
	}
	g.Mileage += amount
	g.MileageHistory = append(g.MileageHistory, MileageHistoryEntry{At: now.UTC(), CharacterID: owner.CharacterID, CharacterName: name, Kind: MileageHistoryItem, Amount: amount})
	if len(g.MileageHistory) > MileageHistoryLimit {
		g.MileageHistory = append([]MileageHistoryEntry(nil), g.MileageHistory[len(g.MileageHistory)-MileageHistoryLimit:]...)
	}
	g.UpdatedAt = now
	return g, nil
}
