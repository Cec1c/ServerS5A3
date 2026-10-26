package itemexpiration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable  = errors.New("item expiration owner is unavailable")
	ErrAccountRequired   = errors.New("item expiration account id is required")
	ErrCharacterRequired = errors.New("item expiration character id is required")
	ErrProjectorRequired = errors.New("item expiration projector is required")
	ErrCharacterNotFound = errors.New("item expiration character is missing")
	ErrAccountMismatch   = errors.New("item expiration character does not belong to account")
)

type Summary struct {
	Inventory        int
	Warehouse        int
	Account          int
	Equipment        int
	Pets             int
	DeletedInventory int
	DeletedWarehouse int
	DeletedAccount   int
}

func (summary Summary) Total() int {
	return summary.Inventory + summary.Warehouse + summary.Account + summary.Equipment + summary.Pets
}

type Assets struct {
	Inventory        *dnfrepo.InventoryRecord
	AccountInventory *dnfrepo.AccountInventoryRecord
	Equipment        *dnfrepo.EquipmentRecord
	Pets             *dnfrepo.PetRecord
	PetsAvailable    bool
}

type Projector func(*Assets) (Summary, error)

type Command struct {
	AccountID   string
	CharacterID string
	UpdatedAt   time.Time
	Project     Projector
}

// Owner owns the atomic reconciliation commit for every durable item
// container affected by the shared PVF expiration projection. PVF lookup and
// current-client row encoding remain in the bridge.
type Owner struct {
	assets dnfrepo.AccountCharacterAssetUnitOfWork
}

func NewOwner(repositories dnfrepo.Group) (*Owner, error) {
	if repositories.AccountAssets == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{assets: repositories.AccountAssets}, nil
}

func (o *Owner) Reconcile(ctx context.Context, command Command) (Summary, error) {
	if o == nil || o.assets == nil {
		return Summary{}, ErrOwnerUnavailable
	}
	accountID := strings.TrimSpace(command.AccountID)
	if accountID == "" {
		return Summary{}, ErrAccountRequired
	}
	characterID := strings.TrimSpace(command.CharacterID)
	if characterID == "" {
		return Summary{}, ErrCharacterRequired
	}
	if command.Project == nil {
		return Summary{}, ErrProjectorRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	now := command.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var result Summary
	apply := func(
		accountInventories dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		equipmentRepo dnfrepo.EquipmentRepository,
		pets dnfrepo.PetRepository,
	) error {
		assets, err := loadAssets(ctx, accountID, characterID, accountInventories, characters, inventories, equipmentRepo)
		if err != nil {
			return err
		}
		// Load only after character/account ownership has been established,
		// using the same transaction as every other container.
		if pets != nil {
			assets.PetsAvailable = true
			record, found, err := pets.Load(ctx, characterID)
			if err != nil {
				return fmt.Errorf("load item expiration pets: %w", err)
			}
			if found {
				record = dnfrepo.ClonePet(record)
				assets.Pets = &record
			}
		}
		result, err = command.Project(&assets)
		if err != nil {
			return err
		}
		if assets.Inventory != nil && (result.Inventory > 0 || result.Warehouse > 0) {
			fields := make([]dnfrepo.InventoryField, 0, 2)
			if result.Inventory > 0 {
				fields = append(fields, dnfrepo.InventoryFieldSlots)
			}
			if result.Warehouse > 0 {
				fields = append(fields, dnfrepo.InventoryFieldWarehouse)
			}
			assets.Inventory.UpdatedAt = now
			if err := dnfrepo.SaveInventoryFields(ctx, inventories, *assets.Inventory, fields...); err != nil {
				return fmt.Errorf("save item expiration inventory: %w", err)
			}
		}
		if assets.AccountInventory != nil && result.Account > 0 {
			assets.AccountInventory.UpdatedAt = now
			if err := accountInventories.Save(ctx, *assets.AccountInventory); err != nil {
				return fmt.Errorf("save item expiration account inventory: %w", err)
			}
		}
		if assets.Equipment != nil && result.Equipment > 0 {
			assets.Equipment.UpdatedAt = now
			if err := dnfrepo.SaveEquipmentFields(ctx, equipmentRepo, *assets.Equipment, dnfrepo.EquipmentFieldEntries); err != nil {
				return fmt.Errorf("save item expiration equipment: %w", err)
			}
		}
		if assets.Pets != nil && result.Pets > 0 {
			assets.Pets.UpdatedAt = now
			if err := dnfrepo.SavePetFields(ctx, pets, *assets.Pets, dnfrepo.PetFieldEntries, dnfrepo.PetFieldEquipped); err != nil {
				return fmt.Errorf("save item expiration pets: %w", err)
			}
		}
		return nil
	}
	var err error
	if withPets, ok := o.assets.(dnfrepo.AccountCharacterPetAssetUnitOfWork); ok {
		err = withPets.WithinAccountCharacterPetAssets(ctx, accountID, characterID, apply)
	} else {
		err = o.assets.WithinAccountCharacterAssets(ctx, accountID, characterID, func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
			return apply(a, c, i, e, nil)
		})
	}
	return result, err
}

func loadAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	accountInventories dnfrepo.AccountInventoryRepository,
	characters dnfrepo.CharacterRepository,
	inventories dnfrepo.InventoryRepository,
	equipmentRepo dnfrepo.EquipmentRepository,
) (Assets, error) {
	if accountInventories == nil || characters == nil || inventories == nil || equipmentRepo == nil {
		return Assets{}, ErrOwnerUnavailable
	}
	var assets Assets
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return Assets{}, fmt.Errorf("load item expiration character: %w", err)
	}
	if !found || strings.TrimSpace(character.CharacterID) != characterID {
		return Assets{}, fmt.Errorf("%w: character=%s", ErrCharacterNotFound, characterID)
	}
	if strings.TrimSpace(character.AccountID) != accountID {
		return Assets{}, fmt.Errorf("%w: account=%s character=%s", ErrAccountMismatch, accountID, characterID)
	}
	inventory, found, err := inventories.Load(ctx, characterID)
	if err != nil {
		return Assets{}, fmt.Errorf("load item expiration inventory: %w", err)
	}
	if found {
		inventory = dnfrepo.CloneInventory(inventory)
		assets.Inventory = &inventory
	}
	accountInventory, found, err := accountInventories.Load(ctx, accountID)
	if err != nil {
		return Assets{}, fmt.Errorf("load item expiration account inventory: %w", err)
	}
	if found {
		accountInventory = dnfrepo.CloneAccountInventory(accountInventory)
		assets.AccountInventory = &accountInventory
	}
	equipment, found, err := equipmentRepo.Load(ctx, characterID)
	if err != nil {
		return Assets{}, fmt.Errorf("load item expiration equipment: %w", err)
	}
	if found {
		equipment = dnfrepo.CloneEquipment(equipment)
		assets.Equipment = &equipment
	}
	return assets, nil
}
