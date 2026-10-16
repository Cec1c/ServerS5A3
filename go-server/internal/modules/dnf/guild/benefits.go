package guild

import (
	"context"
	"errors"
	"math"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrPermanentBenefitOwned = errors.New("permanent guild benefit already owned")
var ErrContractMemberLimit = errors.New("guild contract member tier does not match")
var ErrContractDuration = errors.New("guild contract exceeds thirty days")

// Benefit is an entitlement, not an inventory grant. Its type selects the
// actual consumer: 1 monster EXP, 3 temporary stats, 5 permanent stats, 21 daily reward.
type Benefit struct {
	Type      byte      `json:"type"`
	Value     uint32    `json:"value"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

func (b Benefit) Active(now time.Time) bool {
	return b.valid() && (b.ExpiresAt.IsZero() || now.Before(b.ExpiresAt))
}

func (b Benefit) valid() bool {
	switch b.Type {
	case 1, 3:
		return ((b.Type == 1 && b.Value == 10) || (b.Type == 3 && b.Value == 167)) && !b.ExpiresAt.IsZero() && b.ExpiresAt.Unix() > 0 && b.ExpiresAt.Unix() <= math.MaxInt32
	case 5:
		return b.Value == 169 && b.ExpiresAt.IsZero()
	case 21:
		return (b.Value == 300 || b.Value == 500 || b.Value == 700 || b.Value == 1000) && !b.ExpiresAt.IsZero() && b.ExpiresAt.Unix() > 0 && b.ExpiresAt.Unix() <= math.MaxInt32
	}
	return false
}

type BenefitPurchaseReceipt struct {
	Owner     r.GuildOwner `json:"owner"`
	GuildID   uint32       `json:"guild_id"`
	ContentID uint32       `json:"content_id"`
	Benefit   Benefit      `json:"benefit"`
	Price     uint32       `json:"price"`
	At        time.Time    `json:"at"`
	Gold      bool         `json:"gold,omitempty"`
}

func validateBenefits(reg Registry) error {
	for id, receipt := range reg.BenefitPurchases {
		_, cargo := reg.CargoPurchases[id]
		_, members := reg.MemberExpansions[id]
		_, claim := reg.ContractClaims[id]
		if cargo || members || claim || receipt.Gold != (receipt.Benefit.Type == 21) || !validPurchaseID(id) || receipt.Owner.CharacterID == "" || receipt.Owner.AccountID == "" || receipt.GuildID == 0 || receipt.ContentID == 0 || !receipt.Benefit.valid() || receipt.Price == 0 || receipt.At.IsZero() {
			return ErrInvalidRegistry
		}
	}
	for _, g := range reg.Guilds {
		if g == nil {
			continue
		}
		for kind, b := range g.Benefits {
			if kind != b.Type || !b.valid() {
				return ErrInvalidRegistry
			}
		}
	}
	return validateContractClaims(reg)
}

// PurchaseBenefit extends active time, starting from now only when expired.
// Membership, permission, charge, entitlement and durable receipt share the
// Guild transaction. It never reports success for an unimplemented consumer.
func PurchaseBenefit(ctx context.Context, uow r.GuildUnitOfWork, query CargoPurchase, catalog ContentCatalog, now time.Time) (BenefitPurchaseReceipt, error) {
	var result BenefitPurchaseReceipt
	if uow == nil || query.Revalidate == nil {
		return result, ErrRepositoryUnavailable
	}
	if !validPurchaseID(query.RequestID) {
		return result, ErrPurchaseIdentity
	}
	if now.IsZero() || now.Unix() <= 0 || now.Unix() > math.MaxInt32 {
		return result, ErrInvalidContent
	}
	err := uow.WithinGuild(ctx, []r.GuildOwner{query.Owner}, func(tx r.Group) error {
		if !query.Revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, query.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != query.Owner.CharacterID || actor.AccountID != query.Owner.AccountID {
			return ErrCharacterNotFound
		}
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, member, found := reg.GuildForOwner(query.Owner)
		if !found {
			return ErrNotMember
		}
		if GradePermissionsFor(g, member.Grade)&PurchasePermission == 0 {
			return ErrPermissionDenied
		}
		_, cargo := reg.CargoPurchases[query.RequestID]
		_, expansion := reg.MemberExpansions[query.RequestID]
		_, claim := reg.ContractClaims[query.RequestID]
		if cargo || expansion || claim {
			return ErrPurchaseIdentity
		}
		if old, found := reg.BenefitPurchases[query.RequestID]; found {
			if old.Owner != query.Owner || old.GuildID != g.ID || old.ContentID != query.ContentID {
				return ErrPurchaseIdentity
			}
			if !query.Revalidate() {
				return ErrCharacterNotFound
			}
			result = old
			return nil
		}
		c, found := catalog.Contents[query.ContentID]
		if !found || c.ID != query.ContentID || c.Gold != (c.Type == 21) || c.Mode != 0 || c.Price == 0 || c.Value <= 0 {
			return ErrInvalidContent
		}
		next := Benefit{Type: c.Type, Value: uint32(c.Value)}
		if c.Type == 5 {
			if c.Value != 169 || c.Days != -1 {
				return ErrInvalidContent
			}
			if g.Benefits[5].Active(now) {
				return ErrPermanentBenefitOwned
			}
		} else if c.Type == 21 {
			if catalog.ContractRewardItemID == 0 || catalog.ContractRewardCount != 1 || c.Days != 1 {
				return ErrInvalidContent
			}
			if int(c.Value) != g.MemberLimit {
				return ErrContractMemberLimit
			}
			start := now.UTC().Truncate(time.Second)
			if old := g.Benefits[21]; old.Active(now) {
				start = old.ExpiresAt
			}
			next.ExpiresAt = start.Add(24 * time.Hour)
			if next.ExpiresAt.After(now.UTC().Truncate(time.Second).Add(30 * 24 * time.Hour)) {
				return ErrContractDuration
			}
		} else if c.Type == 1 || c.Type == 3 {
			if c.Days != 1 && c.Days != 7 && c.Days != 30 {
				return ErrInvalidContent
			}
			start := now.UTC().Truncate(time.Second)
			if old := g.Benefits[c.Type]; old.Active(now) {
				if old.Value != next.Value {
					return ErrInvalidContent
				}
				start = old.ExpiresAt
			}
			next.ExpiresAt = start.Add(time.Duration(c.Days) * 24 * time.Hour)
		} else {
			return ErrInvalidContent
		}
		if !next.valid() {
			return ErrInvalidContent
		}
		if c.Gold {
			if actor.Stats["gold"] < int64(c.Price) {
				return ErrInsufficientGold
			}
			actor = r.CloneCharacter(actor)
			actor.Stats["gold"] -= int64(c.Price)
			actor.UpdatedAt = now
			if err := r.SaveCharacterFields(ctx, tx.Character, actor, r.CharacterFieldStats); err != nil {
				return err
			}
		} else {
			if g.Mileage < c.Price {
				return ErrInsufficientMileage
			}
			if err := g.appendMileageHistory(MileageHistoryEntry{At: now, CharacterID: actor.CharacterID, CharacterName: actor.Name, Kind: MileageHistoryPurchase, Amount: c.Price, ContentID: c.ID}); err != nil {
				return err
			}
			g.Mileage -= c.Price
		}
		g.UpdatedAt = now
		if g.Benefits == nil {
			g.Benefits = make(map[byte]Benefit)
		}
		g.Benefits[c.Type] = next
		result = BenefitPurchaseReceipt{Owner: query.Owner, GuildID: g.ID, ContentID: c.ID, Benefit: next, Price: c.Price, At: now, Gold: c.Gold}
		if reg.BenefitPurchases == nil {
			reg.BenefitPurchases = make(map[string]BenefitPurchaseReceipt)
		}
		reg.BenefitPurchases[query.RequestID] = result
		if !query.Revalidate() {
			return ErrCharacterNotFound
		}
		if err := Save(ctx, tx.Settings, reg, now); err != nil {
			return err
		}
		if !query.Revalidate() {
			return ErrCharacterNotFound
		}
		return nil
	})
	if err != nil {
		return BenefitPurchaseReceipt{}, err
	}
	return result, nil
}

// MonsterExperienceBonus applies only to a confirmed monster-death award.
// Consumers must resolve the owner in the authoritative registry at the event.
func MonsterExperienceBonus(g *Guild, base uint32, now time.Time) uint32 {
	if g == nil || !g.Benefits[1].Active(now) {
		return 0
	}
	bonus := uint64(base) * uint64(g.Benefits[1].Value) / 100
	return uint32(min(bonus, uint64(math.MaxUint32-base)))
}
