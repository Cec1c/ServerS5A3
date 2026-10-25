package guild

import (
	"context"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const HongbaoVisibleLimit = 500
const HongbaoContributionDailyLimit uint32 = 500

// HongbaoItemGrant runs inside the caller's transaction. Its last argument is
// the granted item's expireAt, not the purchase time.
type HongbaoItemGrant func(context.Context, r.Group, *r.CharacterRecord, *r.InventoryRecord, HongbaoProduct, time.Time) (string, error)
type HongbaoMailAward struct{ RecordID, Coins, RefundTokenCera uint32 }

// HongbaoMailDeliver creates new attachment mail inside the caller's
// transaction. Its last argument is the settlement time (now).
type HongbaoMailDeliver func(context.Context, r.Group, r.CharacterRecord, HongbaoMailAward, time.Time) ([]string, error)
type HongbaoContract struct {
	Catalog     *HongbaoCatalog
	GrantItem   HongbaoItemGrant
	DeliverMail HongbaoMailDeliver
	RandIntn    func(uint32) (uint32, error)
}
type HongbaoRequest struct {
	Owner       r.GuildOwner
	GuildID     uint32
	OperationID string
	Revalidate  func() bool
}
type HongbaoPurchaseRequest struct {
	HongbaoRequest
	CommodityID uint32
}
type HongbaoIssueRequest struct {
	HongbaoRequest
	ItemID   int64
	Portions uint8
}
type HongbaoClaimRequest struct {
	HongbaoRequest
	RecordID uint32
}
type HongbaoPurchaseResult struct {
	Product  HongbaoProduct
	Slot     string
	ExpireAt time.Time
	Wallet   cerashop.Wallet
	Replayed bool
}
type HongbaoIssueResult struct {
	RecordID     uint32
	ItemID       int64
	Portions     uint8
	ConsumedSlot string
	Replayed     bool
}
type HongbaoClaimResult struct {
	RecordID, Amount uint32
	Wallet           cerashop.Wallet
	Replayed         bool
}
type HongbaoSettlementResult struct {
	Sender                                   r.GuildOwner
	RecordID, Points, Coins, RefundTokenCera uint32
	MailIDs                                  []string
	Replayed                                 bool
}
type HongbaoClaimEntry struct {
	Owner  r.GuildOwner
	Name   string
	Level  uint8
	Amount uint32
	At     time.Time
}
type HongbaoRecord struct {
	ID, GuildID          uint32
	Sender               r.GuildOwner
	SenderName           string
	JoinedAt             time.Time
	Product              HongbaoProduct
	CreatedAt, ExpiresAt time.Time
	Shares               []uint32
	Claims               []HongbaoClaimEntry
	Settlement           *HongbaoSettlementResult
	SettledAt            time.Time
}

// Kept at registry level: dissolving a guild must not discard money or receipts.
type HongbaoState struct {
	NextID  uint32
	Records map[uint32]HongbaoRecord
}
type HongbaoView struct {
	GuildID, MemberCount, PurchasedToday, TakenToday uint32
	Records                                          []HongbaoRecord
}

func (h HongbaoRecord) RemainingPortions() uint8 { return uint8(len(h.Shares) - len(h.Claims)) }
func (h HongbaoRecord) RemainingAmount() uint32 {
	var v uint32
	for _, s := range h.Shares[len(h.Claims):] {
		v += s
	}
	return v
}
func (h HongbaoRecord) closedAt() time.Time {
	if len(h.Claims) == len(h.Shares) && len(h.Claims) > 0 {
		return h.Claims[len(h.Claims)-1].At
	}
	return h.ExpiresAt
}
func (h HongbaoRecord) visible(now time.Time) bool {
	return now.Before(h.closedAt().Add(24 * time.Hour))
}
func (h HongbaoRecord) ready(now time.Time) bool {
	return len(h.Claims) == len(h.Shares) || !now.Before(h.ExpiresAt)
}
func (h HongbaoRecord) copy() HongbaoRecord {
	h.Shares = append([]uint32(nil), h.Shares...)
	h.Claims = append([]HongbaoClaimEntry(nil), h.Claims...)
	if h.Settlement != nil {
		s := *h.Settlement
		s.MailIDs = append([]string(nil), s.MailIDs...)
		h.Settlement = &s
	}
	return h
}
func validHongbaoOwner(owner r.GuildOwner) bool {
	return owner.AccountID != "" && owner.CharacterID != ""
}
func validateHongbao(s *HongbaoState) error {
	if s == nil {
		return nil
	}
	if s.NextID == 0 {
		return ErrHongbaoState
	}
	for id, h := range s.Records {
		if id == 0 || id >= s.NextID || id != h.ID || h.GuildID == 0 || !validHongbaoOwner(h.Sender) || h.SenderName == "" || h.JoinedAt.IsZero() || h.JoinedAt.After(h.CreatedAt) || !validHongbaoProduct(h.Product) || h.CreatedAt.IsZero() || !h.ExpiresAt.Equal(h.CreatedAt.Add(24*time.Hour)) || len(h.Shares) < 5 || len(h.Shares) > 30 || len(h.Claims) > len(h.Shares) {
			return ErrHongbaoState
		}
		var total uint64
		for _, v := range h.Shares {
			if v == 0 {
				return ErrHongbaoState
			}
			total += uint64(v)
		}
		if total != uint64(h.Product.Price) {
			return ErrHongbaoState
		}
		accounts := map[string]bool{}
		last := h.CreatedAt
		for i, c := range h.Claims {
			if !validHongbaoOwner(c.Owner) || c.Name == "" || c.Level == 0 || c.Amount != h.Shares[i] || accounts[c.Owner.AccountID] || c.At.Before(last) || !c.At.Before(h.ExpiresAt) {
				return ErrHongbaoState
			}
			accounts[c.Owner.AccountID] = true
			last = c.At
		}
		if (h.Settlement == nil) != h.SettledAt.IsZero() {
			return ErrHongbaoState
		}
		if h.Settlement != nil {
			x := h.Settlement
			cap := uint32(uint64(h.Product.Price-h.RemainingAmount()) * uint64(h.Product.ContributionRate) / 10000)
			if !h.ready(h.SettledAt) || h.SettledAt.Before(last) || x.Sender != h.Sender || x.RecordID != id || x.Replayed || x.Points > x.Coins || x.Coins > cap || x.Coins > HongbaoContributionDailyLimit || (x.RefundTokenCera != 0 && x.RefundTokenCera != h.RemainingAmount()) {
				return ErrHongbaoState
			}
			ids := map[string]bool{}
			for _, m := range x.MailIDs {
				if m == "" || ids[m] {
					return ErrHongbaoState
				}
				ids[m] = true
			}
			if (x.Coins != 0 || x.RefundTokenCera != 0) != (len(x.MailIDs) > 0) {
				return ErrHongbaoState
			}
		}
	}
	return nil
}
