package npcshop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable  = errors.New("NPC shop owner is unavailable")
	ErrAccountRequired   = errors.New("NPC shop account id is required")
	ErrCharacterRequired = errors.New("NPC shop character id is required")
	ErrCharacterNotFound = errors.New("NPC shop character is not found")
	ErrAccountMismatch   = errors.New("NPC shop character account mismatch")
	ErrAccountNotFound   = errors.New("NPC shop account is not found")
	ErrInventoryNotFound = errors.New("NPC shop inventory is not found")
	ErrProjectorRequired = errors.New("NPC shop mutation projector is required")
)

type Assets struct {
	Account          *dnfrepo.AccountRecord
	AccountInventory *dnfrepo.AccountInventoryRecord
	Character        *dnfrepo.CharacterRecord
	Inventory        *dnfrepo.InventoryRecord
}

type Changes struct {
	Account          bool
	AccountInventory bool
	Character        bool
	Inventory        bool
}

type Projector func(*Assets) (Changes, error)

type Command struct {
	AccountID     string
	CharacterID   string
	UpdatedAt     time.Time
	PurchaseLimit *PurchaseLimit
	// AccountLedger requires the account-first ledger transaction. When that
	// capability exists it is also preferred for ordinary purchases; the legacy
	// character settlement path is used only when no ledger capability exists.
	AccountLedger bool
	Project       Projector
}

// Owner owns NPC-shop authorization and the atomic account warehouse,
// character wallet, and character inventory commit. Current-client packet
// rows and PVF projections remain in the bridge.
type Owner struct {
	settings     dnfrepo.SettingsRepository
	settlements  dnfrepo.CharacterSettlementUnitOfWork
	ledgerAssets dnfrepo.AccountCharacterLedgerAssetUnitOfWork
}

func NewOwner(repositories dnfrepo.Group) (*Owner, error) {
	ledgerAssets, _ := repositories.AccountAssets.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork)
	if repositories.CharacterSettlement == nil && ledgerAssets == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{
		settings: repositories.Settings, settlements: repositories.CharacterSettlement,
		ledgerAssets: ledgerAssets,
	}, nil
}

func (o *Owner) Mutate(ctx context.Context, command Command) error {
	if o == nil || (o.settlements == nil && o.ledgerAssets == nil) || (command.AccountLedger && o.ledgerAssets == nil) {
		return ErrOwnerUnavailable
	}
	accountID := strings.TrimSpace(command.AccountID)
	characterID := strings.TrimSpace(command.CharacterID)
	if accountID == "" {
		return ErrAccountRequired
	}
	if characterID == "" {
		return ErrCharacterRequired
	}
	if command.Project == nil {
		return ErrProjectorRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := command.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	useLedger := o.ledgerAssets != nil

	mutate := func(
		accountRepo dnfrepo.AccountRepository,
		accountInventoryRepo dnfrepo.AccountInventoryRepository,
		characterRepo dnfrepo.CharacterRepository,
		inventoryRepo dnfrepo.InventoryRepository,
	) error {
		accounts, characters, inventories := accountInventoryRepo, characterRepo, inventoryRepo
		if accounts == nil || characters == nil || inventories == nil {
			return ErrOwnerUnavailable
		}
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(character.CharacterID) != characterID {
			return fmt.Errorf("%w: character=%s", ErrCharacterNotFound, characterID)
		}
		if strings.TrimSpace(character.AccountID) != accountID {
			return fmt.Errorf("%w: account=%s character=%s", ErrAccountMismatch, accountID, characterID)
		}
		if accountRepo == nil {
			return ErrOwnerUnavailable
		}
		account, accountFound, err := accountRepo.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !accountFound {
			return ErrAccountNotFound
		}
		if strings.TrimSpace(account.AccountID) != accountID {
			return ErrAccountMismatch
		}
		account = dnfrepo.CloneAccount(account)
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != characterID {
			return fmt.Errorf("%w: character=%s", ErrInventoryNotFound, characterID)
		}
		accountInventory, _, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}

		character = dnfrepo.CloneCharacter(character)
		if err := dnfrepo.BindInventoryContainerState(ctx, o.settings, &inventory); err != nil {
			return err
		}
		inventory = dnfrepo.CloneInventory(inventory)
		accountInventory = dnfrepo.CloneAccountInventory(accountInventory)
		accountInventory.AccountID = accountID
		if inventory.Slots == nil {
			inventory.Slots = make(map[string]dnfrepo.ItemStack)
		}
		if accountInventory.Slots == nil {
			accountInventory.Slots = make(map[string]dnfrepo.ItemStack)
		}

		assets := &Assets{
			Account:          &account,
			AccountInventory: &accountInventory,
			Character:        &character,
			Inventory:        &inventory,
		}
		limitChanges, err := consumePurchaseLimit(assets, command.PurchaseLimit, now)
		if err != nil {
			return err
		}
		changes, err := command.Project(assets)
		if err != nil {
			return err
		}
		changes.Account = changes.Account || limitChanges.Account
		changes.Character = changes.Character || limitChanges.Character
		if changes.Account {
			account.UpdatedAt = now
			if err := accountRepo.Save(ctx, account); err != nil {
				return err
			}
		}
		if changes.Character {
			character.UpdatedAt = now
			if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
		}
		if changes.Inventory {
			inventory.UpdatedAt = now
			if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if changes.AccountInventory {
			accountInventory.UpdatedAt = now
			if err := accounts.Save(ctx, accountInventory); err != nil {
				return err
			}
		}
		return nil
	}

	if useLedger {
		return o.ledgerAssets.WithinAccountCharacterLedgerAssets(ctx, accountID, characterID, func(
			account dnfrepo.AccountRepository,
			accountInventory dnfrepo.AccountInventoryRepository,
			character dnfrepo.CharacterRepository,
			inventory dnfrepo.InventoryRepository,
			_ dnfrepo.EquipmentRepository,
		) error {
			return mutate(account, accountInventory, character, inventory)
		})
	}
	if o.settlements == nil {
		return ErrOwnerUnavailable
	}
	return o.settlements.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		return mutate(tx.Account, tx.AccountInventory, tx.Character, tx.Inventory)
	})
}
