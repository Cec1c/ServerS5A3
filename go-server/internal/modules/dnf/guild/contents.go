package guild

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const PurchasePermission uint32 = 1 << 8

var (
	ErrInvalidContent              = errors.New("invalid or unsupported guild content")
	ErrCargoLimitReached           = errors.New("guild cargo limit reached")
	ErrPurchaseIdentity            = errors.New("guild purchase identity mismatch")
	ErrMemberExpansionUsed         = errors.New("guild member expansion already applied")
	ErrMemberExpansionPrerequisite = errors.New("guild member expansion prerequisite missing")
)

type Content struct {
	ID            uint32
	Type          byte
	Value, Days   int32
	Mode          byte
	ItemID, Price uint32
	Gold          bool
}

type ContentCatalog struct {
	Contents   map[uint32]Content
	CargoLimit uint32
}

// LoadContentCatalog preserves the native nine/ten-column layouts. A display
// item is not an inventory grant; each content type needs its own consumer.
func LoadContentCatalog(source pvf.Source) (ContentCatalog, error) {
	c := ContentCatalog{Contents: make(map[uint32]Content)}
	if source == nil {
		return c, ErrRepositoryUnavailable
	}
	text, err := source.ReadText("etc/(r)guild.etc")
	if err != nil {
		return c, err
	}
	doc, err := pvf.Parse("etc/(r)guild.etc", text)
	if err != nil {
		return c, err
	}
	for _, section := range []struct {
		name  string
		width int
	}{{"guild contents list", 9}, {"additional guild contents list", 10}} {
		values, ok := doc.Section(section.name)
		if !ok || len(values)%section.width != 0 {
			return c, ErrInvalidContent
		}
		for start := 0; start < len(values); start += section.width {
			row := values[start : start+section.width]
			for i, v := range row {
				if i == 8 {
					if v.Kind != pvf.TokenString {
						return c, ErrInvalidContent
					}
					continue
				}
				if v.Kind != pvf.TokenInt || v.Int < -1 || v.Int > math.MaxInt32 {
					return c, ErrInvalidContent
				}
			}
			if row[0].Int <= 0 || row[1].Int <= 0 || row[1].Int > 255 || row[4].Int < 0 || row[4].Int > 1 || row[5].Int <= 0 || row[7].Int <= 0 {
				return c, ErrInvalidContent
			}
			v := Content{ID: uint32(row[0].Int), Type: byte(row[1].Int), Value: int32(row[2].Int), Days: int32(row[3].Int), Mode: byte(row[4].Int), ItemID: uint32(row[5].Int), Price: uint32(row[7].Int)}
			if section.width == 10 {
				if row[9].Int < 0 || row[9].Int > 1 {
					return c, ErrInvalidContent
				}
				v.Gold = row[9].Int == 1
			}
			if _, duplicate := c.Contents[v.ID]; duplicate {
				return c, ErrInvalidContent
			}
			c.Contents[v.ID] = v
		}
	}
	limit, ok := doc.Section("guild cargo size limit")
	if !ok || len(limit) != 1 || limit[0].Kind != pvf.TokenInt || limit[0].Int <= 0 || limit[0].Int > math.MaxInt16+1 {
		return c, ErrInvalidContent
	}
	c.CargoLimit = uint32(limit[0].Int)
	return c, nil
}

type CargoPurchase struct {
	Owner     r.GuildOwner
	ContentID uint32
	// RequestID is supplied by the transport, never read from the content body.
	RequestID       string
	InitialCapacity uint32
	Revalidate      func() bool
}

// Receipts are retained with the registry, rather than in a connection cache.
// Expansion is bounded by capacity; reconnects/readback retain paid operations.
type CargoPurchaseReceipt struct {
	Owner         r.GuildOwner `json:"owner"`
	GuildID       uint32       `json:"guild_id"`
	ContentID     uint32       `json:"content_id"`
	Before, After uint32
	Price         uint32    `json:"price"`
	At            time.Time `json:"at"`
}

func validPurchaseID(id string) bool {
	if len(id) != 64 {
		return false
	}
	b, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(b) == id
}

func validateCargoPurchases(receipts map[string]CargoPurchaseReceipt) error {
	for id, receipt := range receipts {
		if !validPurchaseID(id) || receipt.Owner.CharacterID == "" || receipt.Owner.AccountID == "" || receipt.GuildID == 0 || receipt.ContentID == 0 || receipt.Before == 0 || receipt.After <= receipt.Before || receipt.After > math.MaxUint16+1 || receipt.Price == 0 || receipt.At.IsZero() {
			return ErrInvalidRegistry
		}
	}
	return nil
}

// PurchaseCargo expands the real cargo and debits guild mileage atomically.
// Other types/currencies remain unsupported until their effects are connected.
func PurchaseCargo(ctx context.Context, uow r.GuildUnitOfWork, query CargoPurchase, catalog ContentCatalog, now time.Time) (CargoPurchaseReceipt, error) {
	return purchaseExpansion(ctx, uow, query, catalog, now, 4)
}

// PurchaseMemberExpansion changes the same limit used by all membership paths.
// Existing guilds keep their current capacity; purchases may only increase it.
func PurchaseMemberExpansion(ctx context.Context, uow r.GuildUnitOfWork, query CargoPurchase, catalog ContentCatalog, now time.Time) (CargoPurchaseReceipt, error) {
	return purchaseExpansion(ctx, uow, query, catalog, now, 6)
}

func previousMemberLimit(target uint32) int {
	switch target {
	case 500:
		return InitialMemberLimit
	case 700:
		return 500
	case 1000:
		return 700
	default:
		return -1
	}
}

func purchaseExpansion(ctx context.Context, uow r.GuildUnitOfWork, query CargoPurchase, catalog ContentCatalog, now time.Time, kind byte) (CargoPurchaseReceipt, error) {
	var result CargoPurchaseReceipt
	if uow == nil || query.Revalidate == nil {
		return result, ErrRepositoryUnavailable
	}
	if !validPurchaseID(query.RequestID) {
		return result, ErrPurchaseIdentity
	}
	if now.IsZero() || (kind == 4 && (query.InitialCapacity == 0 || query.InitialCapacity > catalog.CargoLimit || catalog.CargoLimit > math.MaxInt16+1)) {
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
		receipts, other := reg.CargoPurchases, reg.MemberExpansions
		if kind == 6 {
			receipts, other = other, receipts
		}
		if _, reused := other[query.RequestID]; reused {
			return ErrPurchaseIdentity
		}
		if previous, exists := receipts[query.RequestID]; exists {
			if previous.Owner != query.Owner || previous.GuildID != g.ID || previous.ContentID != query.ContentID {
				return ErrPurchaseIdentity
			}
			if !query.Revalidate() {
				return ErrCharacterNotFound
			}
			result = previous
			return nil
		}
		content, found := catalog.Contents[query.ContentID]
		if !found || content.ID != query.ContentID || content.Type != kind || content.Gold || content.Value <= 0 || content.Days != -1 || content.Mode != 0 || content.Price == 0 {
			return ErrInvalidContent
		}
		var capacity, after uint32
		if kind == 6 {
			after = uint32(content.Value)
			previous := previousMemberLimit(after)
			if previous < 0 {
				return ErrInvalidContent
			}
			if g.MemberLimit >= int(after) {
				return ErrMemberExpansionUsed
			}
			if g.MemberLimit != previous {
				return ErrMemberExpansionPrerequisite
			}
			capacity = uint32(g.MemberLimit)
		} else {
			capacity = query.InitialCapacity
			if g.Cargo != nil {
				capacity = g.Cargo.Capacity
			}
			if capacity == 0 || capacity > catalog.CargoLimit || uint32(content.Value) > catalog.CargoLimit-capacity {
				return ErrCargoLimitReached
			}
			after = capacity + uint32(content.Value)
		}
		if g.Mileage < content.Price {
			return ErrInsufficientMileage
		}
		if kind == 6 {
			g.MemberLimit = int(after)
		} else {
			if g.Cargo == nil {
				g.Cargo = &Cargo{}
			}
			g.Cargo.Capacity = after
		}
		g.Mileage -= content.Price
		g.UpdatedAt = now
		result = CargoPurchaseReceipt{Owner: query.Owner, GuildID: g.ID, ContentID: content.ID, Before: capacity, After: after, Price: content.Price, At: now}
		if receipts == nil {
			receipts = make(map[string]CargoPurchaseReceipt)
		}
		receipts[query.RequestID] = result
		if kind == 6 {
			reg.MemberExpansions = receipts
		} else {
			reg.CargoPurchases = receipts
		}
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
		return CargoPurchaseReceipt{}, err
	}
	return result, nil
}
