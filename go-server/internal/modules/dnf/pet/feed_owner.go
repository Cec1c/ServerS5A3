package pet

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type FeedCommand struct {
	AccountID, CharacterID string
	ListType               byte
	Slot                   int16
	ItemID                 int64
	Now                    time.Time
	Expired                func(repo.ItemStack, time.Time) bool
	SetCount               func(repo.ItemStack, int64) (repo.ItemStack, error)
}

type FeedResult struct {
	PetKey         uint32
	Before, After  PetGrowthState
	Remaining      int64
	RemainingStack repo.ItemStack
	Definition     FeedDefinition
}

type FeedOwner struct {
	assets  repo.AccountCharacterPetAssetUnitOfWork
	catalog *PVFCatalog
}

func NewFeedOwner(repositories repo.Group, catalog *PVFCatalog) (*FeedOwner, error) {
	uow, ok := repositories.AccountAssets.(repo.AccountCharacterPetAssetUnitOfWork)
	if !ok || uow == nil {
		return nil, ErrPetTransactionUnavailable
	}
	if catalog == nil {
		return nil, ErrPetPVFSourceRequired
	}
	return &FeedOwner{assets: uow, catalog: catalog}, nil
}

// Feed binds both debit and satiety to the current durable slot-26 creature,
// after validating character/account ownership inside the same transaction.
func (o *FeedOwner) Feed(ctx context.Context, c FeedCommand) (FeedResult, error) {
	var committed FeedResult
	if o == nil || o.assets == nil || o.catalog == nil || strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(c.CharacterID) == "" || c.ListType != 7 || c.Slot < 189 || c.Slot > 238 || c.Expired == nil || c.SetCount == nil {
		return committed, ErrPetFeedInvalid
	}
	definition, err := o.catalog.ResolveFeed(c.ItemID)
	if err != nil {
		return committed, err
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	err = o.assets.WithinAccountCharacterPetAssets(ctx, c.AccountID, c.CharacterID, func(_ repo.AccountInventoryRepository, chars repo.CharacterRepository, invs repo.InventoryRepository, equips repo.EquipmentRepository, pets repo.PetRepository) error {
		ch, ok, err := chars.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !ok || ch.CharacterID != c.CharacterID || ch.AccountID != c.AccountID {
			return ErrPetRecordOwnerMismatch
		}
		inv, ok, err := invs.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !ok || inv.CharacterID != c.CharacterID {
			return ErrInventoryNotFound
		}
		inv = repo.CloneInventory(inv)
		key := fmt.Sprintf("7:%d", c.Slot)
		stack, ok := inv.Slots[key]
		if !ok || stack.Count <= 0 || stack.ItemID != c.ItemID || itemlock.IsLockedAt(stack, c.Now) || c.Expired(stack, c.Now) {
			return ErrPetFeedInvalid
		}
		// Reject textual endpoint aliases even if they contain another template.
		for k := range inv.Slots {
			if k == key {
				continue
			}
			list, slot, ok := strings.Cut(k, ":")
			if !ok {
				continue
			}
			l, e1 := strconv.ParseInt(strings.TrimSpace(list), 10, 16)
			n, e2 := strconv.ParseInt(strings.TrimSpace(slot), 10, 16)
			if e1 == nil && e2 == nil && l == 7 && n == int64(c.Slot) {
				return ErrPetFeedInvalid
			}
		}
		equipment, ok, err := equips.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !ok || equipment.CharacterID != c.CharacterID {
			return ErrPetEquippedAbsent
		}
		petRecord, ok, err := pets.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !ok || petRecord.CharacterID != c.CharacterID {
			return ErrPetRecordNotFound
		}
		petRecord = repo.ClonePet(petRecord)
		petKey := petRecord.EquippedKey
		entry, ok := petRecord.Entries[petKey]
		if !ok || entry.PetKey != petKey {
			return ErrPetEquippedAbsent
		}
		worn, ok := equipment.Entries["26"]
		if !ok {
			return ErrPetEquippedAbsent
		}
		if c.Expired(repo.ItemStack{ItemID: worn.ItemID, Count: 1, ExpireAt: worn.ExpireAt, RawEntry: worn.RawEntry, Extra: worn.Extra}, c.Now) {
			return ErrPetEquippedAbsent
		}
		if err := validateWornPetEntry(petKey, entry, worn); err != nil {
			return err
		}
		if _, err := o.catalog.ResolveCreature(entry.ItemID); err != nil {
			return err
		}
		before, err := petGrowthState(entry)
		if err != nil {
			return err
		}
		after := before
		after.SatietyMicros = petSatietyMicros(before) + int64(definition.satiety)*petSatietyScale
		if after.SatietyMicros > 100*petSatietyScale {
			after.SatietyMicros = 100 * petSatietyScale
		}
		after.Satiety = visiblePetSatietyFromMicros(after.SatietyMicros, false)
		remaining := stack.Count - 1
		if remaining == 0 {
			delete(inv.Slots, key)
		} else {
			updated, err := c.SetCount(stack, remaining)
			if err != nil {
				return err
			}
			if updated.ItemID != stack.ItemID || updated.Count != remaining {
				return ErrPetFeedInvalid
			}
			inv.Slots[key] = updated
		}
		entry.Satiety = byte(after.Satiety)
		entry.SatietyMicros = after.SatietyMicros
		petRecord.Entries[petKey] = entry
		inv.UpdatedAt = c.Now
		petRecord.UpdatedAt = c.Now
		if err := repo.SaveInventoryFields(ctx, invs, inv, repo.InventoryFieldSlots); err != nil {
			return err
		}
		if err := repo.SavePetFields(ctx, pets, petRecord, repo.PetFieldEntries); err != nil {
			return err
		}
		committed = FeedResult{PetKey: entry.CreatureKey, Before: before, After: after, Remaining: remaining, RemainingStack: inv.Slots[key], Definition: definition}
		return nil
	})
	if err != nil {
		return FeedResult{}, err
	}
	return committed, nil
}
