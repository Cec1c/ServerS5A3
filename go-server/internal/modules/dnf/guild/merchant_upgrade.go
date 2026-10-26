package guild

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const (
	MerchantMinLevel          = uint8(1)
	MerchantMaxLevel          = uint8(10)
	MerchantUpgradePermission = uint32(1 << 16)
	merchantUpgradeKeyPrefix  = "merchant_upgrade:"
)

var (
	ErrMerchantState       = errors.New("invalid guild merchant state")
	ErrMerchantCatalog     = errors.New("invalid guild merchant upgrade catalog")
	ErrMerchantNPC         = errors.New("invalid guild merchant NPC")
	ErrMerchantMaxLevel    = errors.New("guild merchant is at maximum level")
	ErrMerchantLevelStep   = errors.New("guild merchant upgrade must advance one level")
	ErrMerchantStaleLevel  = errors.New("guild merchant level is stale")
	ErrMerchantEssence     = errors.New("insufficient guild merchant essence")
	ErrMerchantInteraction = errors.New("guild merchant interaction is unavailable")
)

// MerchantState is ordered as defence/building then mercenary. A missing
// object means the native initial state {1,1}; it is not materialized on read.
type MerchantState struct {
	Levels [2]uint8 `json:"levels"`
}

func (s *MerchantState) Validate() error {
	if s == nil {
		return nil
	}
	for _, level := range s.Levels {
		if level < MerchantMinLevel || level > MerchantMaxLevel {
			return ErrMerchantState
		}
	}
	return nil
}

// MerchantLevel returns the durable level. nil guild state intentionally maps
// to the initial level for both merchants.
func MerchantLevel(g *Guild, index uint8) (uint8, error) {
	if g == nil || index > 1 {
		return 0, ErrMerchantState
	}
	if err := g.Merchants.Validate(); err != nil {
		return 0, err
	}
	if g.Merchants == nil {
		return MerchantMinLevel, nil
	}
	return g.Merchants.Levels[index], nil
}

type MerchantUpgradeCatalog struct {
	costs [2][MerchantMaxLevel]EssenceBalance
}

func LoadMerchantUpgradeCatalog(source pvf.Source) (*MerchantUpgradeCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(EssenceCatalogPath)
	if err != nil {
		return nil, err
	}
	return ParseMerchantUpgradeCatalog(text)
}

// ParseMerchantUpgradeCatalog reads the two native 10-row level tables. Each
// row is AP then GP; target level 2 uses row 1 and target 10 uses row 9.
func ParseMerchantUpgradeCatalog(text string) (*MerchantUpgradeCatalog, error) {
	doc, err := pvf.Parse(EssenceCatalogPath, text)
	if err != nil {
		return nil, err
	}
	result := &MerchantUpgradeCatalog{}
	inUpgrade := false
	upgradeSeen := false
	openComponent := -1
	var seen [2]bool
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		tokens := doc.Tokens[section.Start:section.End]
		switch name {
		case "building upgrade":
			if upgradeSeen || inUpgrade || len(tokens) != 0 {
				return nil, ErrMerchantCatalog
			}
			upgradeSeen, inUpgrade = true, true
		case "/building upgrade":
			if !inUpgrade || openComponent != -1 || !seen[0] || !seen[1] || len(tokens) != 0 {
				return nil, ErrMerchantCatalog
			}
			inUpgrade = false
		case "building", "mecenary":
			if !inUpgrade || openComponent != -1 || len(tokens) != 20 {
				return nil, ErrMerchantCatalog
			}
			index := uint8(0)
			if name == "mecenary" {
				index = 1
			}
			if seen[index] {
				return nil, ErrMerchantCatalog
			}
			for i := 0; i < len(tokens); i += 2 {
				ap, gp := tokens[i], tokens[i+1]
				if ap.Kind != pvf.TokenInt || gp.Kind != pvf.TokenInt ||
					ap.Int < 0 || ap.Int > math.MaxUint32 ||
					gp.Int < 0 || gp.Int > math.MaxUint32 {
					return nil, ErrMerchantCatalog
				}
				result.costs[index][i/2] = EssenceBalance{AP: uint32(ap.Int), GP: uint32(gp.Int)}
			}
			seen[index] = true
			openComponent = int(index)
		case "/building", "/mecenary":
			index := 0
			if name == "/mecenary" {
				index = 1
			}
			if !inUpgrade || openComponent != index || len(tokens) != 0 {
				return nil, ErrMerchantCatalog
			}
			openComponent = -1
		default:
			if inUpgrade {
				return nil, ErrMerchantCatalog
			}
		}
	}
	if !upgradeSeen || inUpgrade || openComponent != -1 || !seen[0] || !seen[1] {
		return nil, ErrMerchantCatalog
	}
	return result, nil
}

func (c *MerchantUpgradeCatalog) Cost(index uint8, target uint8) (EssenceBalance, error) {
	if c == nil || index > 1 || target < MerchantMinLevel+1 || target > MerchantMaxLevel {
		return EssenceBalance{}, ErrMerchantCatalog
	}
	return c.costs[index][target-1], nil
}

type MerchantUpgradeRequest struct {
	Owner        r.GuildOwner
	GuildID      uint32
	RequestID    string
	NPCID        uint32
	CurrentLevel uint32
	TargetLevel  uint32
	Revalidate   func() bool
	CanInteract  func(r.CharacterRecord, *Guild, bool) bool
}

type MerchantUpgradeReceipt struct {
	Owner         r.GuildOwner   `json:"owner"`
	GuildID       uint32         `json:"guild_id"`
	NPCID         uint32         `json:"npc_id"`
	MerchantIndex uint8          `json:"merchant_index"`
	FromLevel     uint8          `json:"from_level"`
	ToLevel       uint8          `json:"to_level"`
	Cost          EssenceBalance `json:"cost"`
	BalanceAfter  EssenceBalance `json:"balance_after"`
	At            time.Time      `json:"at"`
}

func validateMerchantUpgradeReceipts(reg Registry) error {
	for key, receipt := range reg.MerchantUpgradeReceipts {
		requestID, ok := strings.CutPrefix(key, merchantUpgradeKeyPrefix)
		if !ok || !validPurchaseID(requestID) ||
			receipt.Owner.AccountID == "" || receipt.Owner.CharacterID == "" ||
			receipt.GuildID == 0 || receipt.FromLevel < MerchantMinLevel ||
			receipt.ToLevel != receipt.FromLevel+1 || receipt.ToLevel > MerchantMaxLevel ||
			receipt.MerchantIndex > 1 ||
			!merchantNPCForIndex(receipt.MerchantIndex, receipt.NPCID) ||
			receipt.At.IsZero() {
			return ErrInvalidRegistry
		}
	}
	return nil
}

func merchantIndexForNPC(npc uint32) (uint8, error) {
	switch npc {
	case 390:
		return 0, nil
	case 389:
		return 1, nil
	default:
		return 0, ErrMerchantNPC
	}
}

func merchantNPCForIndex(index uint8, npc uint32) bool {
	return (index == 0 && npc == 390) || (index == 1 && npc == 389)
}

func validateMerchantUpgradeLevels(current, target uint32) error {
	if current < uint32(MerchantMinLevel) || current > uint32(MerchantMaxLevel) {
		return ErrMerchantState
	}
	if current == uint32(MerchantMaxLevel) {
		return ErrMerchantMaxLevel
	}
	if target != current+1 || target > uint32(MerchantMaxLevel) {
		return ErrMerchantLevelStep
	}
	return nil
}

// ApplyMerchantUpgrade atomically advances one merchant, debits guild essence,
// and stores a permanent request receipt. Replays return the original receipt
// without requiring a new NPC map or depending on the current merchant state.
func ApplyMerchantUpgrade(ctx context.Context, uow r.GuildUnitOfWork, q MerchantUpgradeRequest, catalog *MerchantUpgradeCatalog, now time.Time) (MerchantUpgradeReceipt, error) {
	var result MerchantUpgradeReceipt
	if uow == nil || q.Revalidate == nil || q.CanInteract == nil {
		return result, ErrRepositoryUnavailable
	}
	if !validPurchaseID(q.RequestID) || q.GuildID == 0 || now.IsZero() {
		return result, ErrPurchaseIdentity
	}
	index, err := merchantIndexForNPC(q.NPCID)
	if err != nil {
		return result, err
	}
	if err = validateMerchantUpgradeLevels(q.CurrentLevel, q.TargetLevel); err != nil {
		return result, err
	}
	if catalog == nil {
		return result, ErrMerchantCatalog
	}

	key := merchantUpgradeKeyPrefix + q.RequestID
	err = uow.WithinGuild(ctx, []r.GuildOwner{q.Owner}, func(tx r.Group) error {
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != q.Owner.CharacterID || actor.AccountID != q.Owner.AccountID || actor.Stats["delete_flag"] != 0 {
			return ErrCharacterNotFound
		}
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, member, found := reg.GuildForOwner(q.Owner)
		if !found || g.ID != q.GuildID {
			return ErrNotMember
		}
		if GradePermissionsFor(g, member.Grade)&(PurchasePermission|MerchantUpgradePermission) != PurchasePermission|MerchantUpgradePermission {
			return ErrPermissionDenied
		}

		old, replay := reg.MerchantUpgradeReceipts[key]
		if replay && (old.Owner != q.Owner || old.GuildID != q.GuildID ||
			old.NPCID != q.NPCID || old.MerchantIndex != index ||
			old.FromLevel != uint8(q.CurrentLevel) || old.ToLevel != uint8(q.TargetLevel)) {
			return ErrPurchaseIdentity
		}
		if !q.CanInteract(actor, g, replay) {
			return ErrMerchantInteraction
		}
		if replay {
			if !q.Revalidate() {
				return ErrCharacterNotFound
			}
			result = old
			return nil
		}

		current, err := MerchantLevel(g, index)
		if err != nil {
			return err
		}
		if uint32(current) != q.CurrentLevel {
			return ErrMerchantStaleLevel
		}
		target := uint8(q.TargetLevel)
		cost, err := catalog.Cost(index, target)
		if err != nil {
			return err
		}
		if g.Essence.AP < cost.AP || g.Essence.GP < cost.GP {
			return ErrMerchantEssence
		}
		g.Essence.AP -= cost.AP
		g.Essence.GP -= cost.GP
		if g.Merchants == nil {
			g.Merchants = &MerchantState{Levels: [2]uint8{MerchantMinLevel, MerchantMinLevel}}
		}
		g.Merchants.Levels[index] = target
		g.UpdatedAt = now
		result = MerchantUpgradeReceipt{
			Owner: q.Owner, GuildID: g.ID, NPCID: q.NPCID, MerchantIndex: index,
			FromLevel: current, ToLevel: target, Cost: cost, BalanceAfter: g.Essence, At: now,
		}
		if reg.MerchantUpgradeReceipts == nil {
			reg.MerchantUpgradeReceipts = make(map[string]MerchantUpgradeReceipt)
		}
		reg.MerchantUpgradeReceipts[key] = result
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		if err := Save(ctx, tx.Settings, reg, now); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		return nil
	})
	if err != nil {
		return MerchantUpgradeReceipt{}, err
	}
	return result, nil
}
