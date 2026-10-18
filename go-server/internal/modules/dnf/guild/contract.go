package guild

import (
	"errors"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrContractInactive = errors.New("guild contract is not active")
var ErrContractAlreadyClaimed = errors.New("guild contract reward already claimed today")

type ContractClaimReceipt struct {
	Owner   r.GuildOwner `json:"owner"`
	GuildID uint32       `json:"guild_id"`
	ItemID  uint32       `json:"item_id"`
	Count   uint32       `json:"count"`
	At      time.Time    `json:"at"`
	DayEnd  time.Time    `json:"day_end"`
	Slots   []int16      `json:"slots"`
}

func validateContractClaims(reg Registry) error {
	for id, claim := range reg.ContractClaims {
		_, cargo := reg.CargoPurchases[id]
		_, member := reg.MemberExpansions[id]
		_, benefit := reg.BenefitPurchases[id]
		if cargo || member || benefit || !validPurchaseID(id) || claim.Owner.AccountID == "" || claim.Owner.CharacterID == "" || claim.GuildID == 0 || claim.ItemID == 0 || claim.Count != 1 || claim.At.IsZero() || !claim.DayEnd.Equal(itemexpiration.NextDailyDeleteAt(claim.At)) || len(claim.Slots) != 1 || claim.Slots[0] < 0 {
			return ErrInvalidRegistry
		}
	}
	return nil
}

// RecordContractClaim runs inside GuildInventory UoW. The caller must grant
// the real reward, fill Slots and save this registry in that same transaction.
// Receipts retain request identity; the service-day check spans guild changes.
func (reg *Registry) RecordContractClaim(owner r.GuildOwner, requestID string, catalog ContentCatalog, now time.Time) (ContractClaimReceipt, bool, error) {
	var empty ContractClaimReceipt
	if !validPurchaseID(requestID) || catalog.ContractRewardItemID == 0 || catalog.ContractRewardCount != 1 || now.IsZero() {
		return empty, false, ErrInvalidContent
	}
	g, _, found := reg.GuildForOwner(owner)
	if !found {
		return empty, false, ErrNotMember
	}
	_, cargo := reg.CargoPurchases[requestID]
	_, expansion := reg.MemberExpansions[requestID]
	_, benefit := reg.BenefitPurchases[requestID]
	if cargo || expansion || benefit {
		return empty, false, ErrPurchaseIdentity
	}
	if old, found := reg.ContractClaims[requestID]; found {
		if old.Owner != owner || old.GuildID != g.ID {
			return empty, false, ErrPurchaseIdentity
		}
		return old, true, nil
	}
	if !g.Benefits[21].Active(now) {
		return empty, false, ErrContractInactive
	}
	dayEnd := itemexpiration.NextDailyDeleteAt(now)
	for _, old := range reg.ContractClaims {
		if old.Owner == owner && old.DayEnd.Equal(dayEnd) {
			return empty, false, ErrContractAlreadyClaimed
		}
	}
	claim := ContractClaimReceipt{Owner: owner, GuildID: g.ID, ItemID: catalog.ContractRewardItemID, Count: catalog.ContractRewardCount, At: now.UTC(), DayEnd: dayEnd}
	if reg.ContractClaims == nil {
		reg.ContractClaims = make(map[string]ContractClaimReceipt)
	}
	reg.ContractClaims[requestID] = claim
	return claim, false, nil
}
