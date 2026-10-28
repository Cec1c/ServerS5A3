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
const MileageHistoryDungeon byte = 3
const MileageHistoryPurchase byte = 5
const MileageHistoryItem byte = 8

type MileageHistoryEntry struct {
	At            time.Time `json:"at"`
	CharacterID   string    `json:"character_id"`
	CharacterName string    `json:"character_name"`
	Kind          byte      `json:"kind"`
	Amount        uint32    `json:"amount"`
	ContentID     uint32    `json:"content_id,omitempty"`
}

// Valid checks the native signed amount/time fields and known renderer kinds.
// Purchase amounts stay positive; the native kind selects the minus sign.
func (entry MileageHistoryEntry) Valid() bool {
	if entry.At.Unix() <= 0 || entry.At.Unix() > math.MaxInt32 || entry.Amount == 0 || entry.Amount > math.MaxInt32 || strings.TrimSpace(entry.CharacterName) == "" || strings.ContainsRune(entry.CharacterName, '\x00') {
		return false
	}
	switch entry.Kind {
	case MileageHistoryDungeon, MileageHistoryItem:
		return entry.ContentID == 0
	case MileageHistoryPurchase:
		return entry.ContentID > 0 && entry.ContentID <= math.MaxInt32
	}
	return false
}

func (g *Guild) appendMileageHistory(entry MileageHistoryEntry) error {
	if !entry.Valid() {
		return ErrMileageHistory
	}
	entry.At = entry.At.UTC()
	g.MileageHistory = append(g.MileageHistory, entry)
	if len(g.MileageHistory) > MileageHistoryLimit {
		g.MileageHistory = append([]MileageHistoryEntry(nil), g.MileageHistory[len(g.MileageHistory)-MileageHistoryLimit:]...)
	}
	return nil
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
	if err := g.appendMileageHistory(MileageHistoryEntry{At: now, CharacterID: owner.CharacterID, CharacterName: name, Kind: MileageHistoryItem, Amount: amount}); err != nil {
		return nil, err
	}
	if err := addMemberRankingMileage(g, m, amount, now); err != nil {
		return nil, err
	}
	g.Mileage += amount
	g.UpdatedAt = now
	return g, nil
}
