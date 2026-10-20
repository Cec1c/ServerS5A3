package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

var ErrBuffOwnedSnapshotUnavailable = errors.New("buff owned snapshot transaction unavailable")
var ErrBuffLegacyOwnerMismatch = errors.New("buff legacy item ownership mismatch")
var ErrBuffLegacyAmbiguous = errors.New("buff legacy item uid ambiguous")

// Optional capability of the existing CeraShopAssets implementation. It must
// expose only repositories belonging to this transaction and exact owner.
type BuffOwnedSnapshotUnitOfWork interface {
	WithinBuffOwnedSnapshot(context.Context, string, string, string,
		func(CharacterRepository, InventoryRepository, EquipmentRepository, SettingsRepository, BuffPetMetadataReader, OwnedLegacyInventoryRepository) error) error
}

// This separate read interface never manufactures current Inventory assets.
type OwnedLegacyInventoryRepository interface {
	SelectOwnedBuffItems(context.Context, string, []byte) ([]OwnedLegacyItem, error)
}

type OwnedLegacyItem struct {
	ItemUID     int64
	OwnerScope  string
	OwnerID     string
	CharacterID string
	ItemKind    string
	Item        LegacyInventoryItem
}

// ParseCurrentInventoryLocation preserves the existing current-list parser.
func ParseCurrentInventoryLocation(key string) (byte, int16, bool) {
	listRaw, slotRaw, ok := strings.Cut(key, ":")
	if !ok {
		return 0, 0, false
	}
	list, err := strconv.ParseInt(strings.TrimSpace(listRaw), 10, 16)
	if err != nil || list < 0 || list > 255 {
		return 0, 0, false
	}
	slot, err := strconv.ParseInt(strings.TrimSpace(slotRaw), 10, 16)
	if err != nil || slot < 0 || slot > 32767 {
		return 0, 0, false
	}
	return byte(list), int16(slot), true
}

// Projection is independent of BUFF registration eligibility: Count is not a
// filter, and a list-0 gold wallet row also makes this container current.
func CurrentInventoryItemProjects(key string, item ItemStack, wanted byte) bool {
	list, slot, ok := ParseCurrentInventoryLocation(key)
	return ok && list == wanted && (item.ItemID > 0 || (list == 0 && slot == 0 && item.ItemID == 0))
}

// No Save or full PetRepository escapes this optional metadata hook. Only
// creature entries/Extra needed by the existing enchant projection are read.
type BuffPetMetadataReader interface {
	LoadBuffPetEntries(context.Context, string) (PetRecord, bool, error)
}

// Only this wrapper passes the scoped transaction repositories to the domain.
// Lack of the optional capability retains current-only registration behavior.
func WithinBuffOwnedSnapshot(ctx context.Context, base Group, characterID, accountID string, apply func(Group) error) error {
	characterID, accountID = strings.TrimSpace(characterID), strings.TrimSpace(accountID)
	if characterID == "" || accountID == "" || apply == nil {
		return ErrBuffOwnedSnapshotUnavailable
	}
	u, ok := base.CeraShopAssets.(BuffOwnedSnapshotUnitOfWork)
	if !ok {
		return WithinBuffRegistration(ctx, base, characterID, accountID, apply)
	}
	return u.WithinBuffOwnedSnapshot(ctx, accountID, characterID, "character:"+characterID+":buff_swap_native", func(c CharacterRepository, i InventoryRepository, e EquipmentRepository, s SettingsRepository, p BuffPetMetadataReader, l OwnedLegacyInventoryRepository) error {
		if c == nil || s == nil {
			return ErrBuffOwnedSnapshotUnavailable
		}
		owner, found, err := c.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || owner.CharacterID != characterID || owner.AccountID != accountID {
			return ErrBuffLegacyOwnerMismatch
		}
		return apply(Group{Character: c, Inventory: i, Equipment: e, Settings: s, BuffPetMetadata: p, OwnedLegacyInventory: l})
	})
}

func CloneOwnedLegacyItem(row OwnedLegacyItem) OwnedLegacyItem {
	if row.Item.Extra != nil {
		extra := make(map[string]string, len(row.Item.Extra))
		for k, v := range row.Item.Extra {
			extra[k] = v
		}
		row.Item.Extra = extra
	}
	return row
}
