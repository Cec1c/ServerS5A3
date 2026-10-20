package booster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable  = errors.New("booster owner is unavailable")
	ErrAccountRequired   = errors.New("booster account id is required")
	ErrCharacterRequired = errors.New("booster character id is required")
	ErrCharacterNotFound = errors.New("booster character is not found")
	ErrAccountMismatch   = errors.New("booster character account mismatch")
	ErrInventoryNotFound = errors.New("booster inventory is not found")
	ErrProjectorRequired = errors.New("booster mutation projector is required")
	ErrGoldCostInvalid   = errors.New("booster gold cost is invalid")
	ErrGoldInsufficient  = errors.New("booster gold is insufficient")
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
	AccountLedger bool
	AccountID     string
	CharacterID   string
	UpdatedAt     time.Time
	GoldCost      int64
	Project       Projector
}

// Owner owns booster authorization and the atomic account-warehouse plus
// character-inventory commit. Packet and raw-row projection stay in bridge.
type Owner struct {
	settings dnfrepo.SettingsRepository
	assets   dnfrepo.AccountCharacterAssetUnitOfWork
}

func NewOwner(repositories dnfrepo.Group) (*Owner, error) {
	if repositories.AccountAssets == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{settings: repositories.Settings, assets: repositories.AccountAssets}, nil
}

func (o *Owner) Open(ctx context.Context, command Command) error {
	if o == nil || o.assets == nil {
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
	if command.GoldCost < 0 {
		return ErrGoldCostInvalid
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

	within := func(apply func(dnfrepo.AccountRepository, dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
		if command.AccountLedger {
			ledger, ok := o.assets.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork)
			if !ok {
				return ErrOwnerUnavailable
			}
			return ledger.WithinAccountCharacterLedgerAssets(ctx, accountID, characterID, apply)
		}
		return o.assets.WithinAccountCharacterAssets(ctx, accountID, characterID, func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
			return apply(nil, a, c, i, e)
		})
	}
	return within(func(
		ledger dnfrepo.AccountRepository,
		accounts dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		_ dnfrepo.EquipmentRepository,
	) error {
		if accounts == nil || characters == nil || inventories == nil {
			return ErrOwnerUnavailable
		}
		var account *dnfrepo.AccountRecord
		if command.AccountLedger {
			if ledger == nil {
				return ErrOwnerUnavailable
			}
			record, found, err := ledger.Load(ctx, accountID)
			if err != nil {
				return err
			}
			if !found || record.AccountID != accountID {
				return ErrOwnerUnavailable
			}
			record = dnfrepo.CloneAccount(record)
			account = &record
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
		character = dnfrepo.CloneCharacter(character)
		if command.GoldCost > 0 {
			if character.Stats["gold"] < command.GoldCost {
				return ErrGoldInsufficient
			}
			character.Stats["gold"] -= command.GoldCost
		}
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != characterID || inventory.Slots == nil {
			return fmt.Errorf("%w: character=%s", ErrInventoryNotFound, characterID)
		}
		accountInventory, _, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		accountInventory = dnfrepo.CloneAccountInventory(accountInventory)
		accountInventory.AccountID = accountID
		if accountInventory.Slots == nil {
			accountInventory.Slots = make(map[string]dnfrepo.ItemStack)
		}
		if err := dnfrepo.BindInventoryContainerState(ctx, o.settings, &inventory); err != nil {
			return err
		}
		inventory = dnfrepo.CloneInventory(inventory)

		changes, err := command.Project(&Assets{
			Account:          account,
			AccountInventory: &accountInventory,
			Character:        &character,
			Inventory:        &inventory,
		})
		if err != nil {
			return err
		}
		if changes.Account {
			if ledger == nil || account == nil {
				return ErrOwnerUnavailable
			}
			account.UpdatedAt = now
			if err := ledger.Save(ctx, *account); err != nil {
				return err
			}
		}
		if changes.Inventory {
			inventory.UpdatedAt = now
			if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if changes.Character || command.GoldCost > 0 {
			character.UpdatedAt = now
			if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
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
	})
}

// CreditEpicPiece 仅改事务内的账号副本；开盒、其它奖励或保存失败会一起回滚。
func CreditEpicPiece(assets *Assets, pieceID, count uint32, now time.Time) (uint32, error) {
	if assets == nil || assets.Account == nil {
		return 0, ErrOwnerUnavailable
	}
	if pieceID == 0 || count == 0 {
		return 0, dnfrepo.ErrAccountEpicPieceIDRequired
	}
	account := assets.Account
	key := dnfrepo.AccountEpicPieceMetadataKey(pieceID)
	amount := uint64(dnfrepo.ParseAccountEpicPieceBalance(account.Metadata[key])) + uint64(count)
	if amount > uint64(dnfrepo.AccountEpicPieceBalanceMax) {
		amount = uint64(dnfrepo.AccountEpicPieceBalanceMax)
	}
	if account.Metadata == nil {
		account.Metadata = map[string]string{}
	}
	account.Metadata[key] = dnfrepo.FormatAccountEpicPieceBalance(uint32(amount))
	account.UpdatedAt = now.UTC()
	return uint32(amount), nil
}
