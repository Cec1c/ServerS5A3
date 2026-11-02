package disjoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable  = errors.New("disjoint owner is unavailable")
	ErrAccountRequired   = errors.New("disjoint account id is required")
	ErrCharacterRequired = errors.New("disjoint character id is required")
	ErrCharacterNotFound = errors.New("disjoint character is not found")
	ErrAccountMismatch   = errors.New("disjoint character account mismatch")
	ErrInventoryNotFound = errors.New("disjoint inventory is not found")
	ErrProjectorRequired = errors.New("disjoint mutation projector is required")
)

type Assets struct {
	Account          *dnfrepo.AccountRecord
	AccountInventory *dnfrepo.AccountInventoryRecord
	Inventory        *dnfrepo.InventoryRecord
	Character        *dnfrepo.CharacterRecord
}

type Changes struct {
	Account           bool
	EmblemUpgradeFrom int
	AccountInventory  bool
	Inventory         bool
	CharacterStats    bool
}

type Projector func(*Assets) (Changes, error)

type Command struct {
	AccountLedger bool
	AccountID     string
	CharacterID   string
	UpdatedAt     time.Time
	Project       Projector
}

// Owner owns character/account authorization plus the atomic account
// warehouse and character inventory write used by all three gameplays.
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

func (o *Owner) DisjointEquipment(ctx context.Context, command Command) error {
	return o.mutate(ctx, "equipment-disjoint", command)
}

func (o *Owner) DisjointAvatar(ctx context.Context, command Command) error {
	return o.mutate(ctx, "avatar-disjoint", command)
}

func (o *Owner) CompoundEmblems(ctx context.Context, command Command) error {
	return o.mutate(ctx, "emblem-compound", command)
}

func (o *Owner) mutate(ctx context.Context, operation string, command Command) error {
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
			return fmt.Errorf("%w: character=%s operation=%s", ErrCharacterNotFound, characterID, operation)
		}
		if strings.TrimSpace(character.AccountID) != accountID {
			return fmt.Errorf("%w: account=%s character=%s operation=%s", ErrAccountMismatch, accountID, characterID, operation)
		}
		character = dnfrepo.CloneCharacter(character)
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != characterID || inventory.Slots == nil {
			return fmt.Errorf("%w: character=%s operation=%s", ErrInventoryNotFound, characterID, operation)
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
		previousSlots := inventory.Slots
		inventory = dnfrepo.CloneInventory(inventory)

		changes, err := command.Project(&Assets{
			Account:          account,
			AccountInventory: &accountInventory,
			Inventory:        &inventory,
			Character:        &character,
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
		if changes.CharacterStats {
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
		if changes.Inventory && operation != "equipment-disjoint" {
			consumed := false
			for key, before := range previousSlots {
				after := inventory.Slots[key]
				if before.Count > 0 && (after.ItemID != before.ItemID || after.Count < before.Count) {
					consumed = true
					break
				}
			}
			if consumed {
				kind := uint32(19)
				if operation == "avatar-disjoint" {
					kind = 42
				}
				dnfrepo.AddBattlePassCounter(&character, kind, -1, 1, now)
				if operation == "emblem-compound" && (changes.EmblemUpgradeFrom == 2 || changes.EmblemUpgradeFrom == 3) {
					dnfrepo.AddBattlePassCounter(&character, 20, int64(changes.EmblemUpgradeFrom), 1, now)
				}
				character.UpdatedAt = now
				if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
					return err
				}
			}
		}
		if changes.Inventory && operation == "equipment-disjoint" {
			count := int64(0)
			for key, before := range previousSlots {
				after, ok := inventory.Slots[key]
				consumed := before.Count
				if ok && after.ItemID == before.ItemID {
					consumed -= after.Count
				}
				if consumed > 0 {
					count += min(consumed, int64(^uint64(0)>>1)-count)
				}
			}
			if count > 0 {
				character = dnfrepo.CloneCharacter(character)
				dnfrepo.AddBattlePassCounter(&character, 17, -1, count, now)
				if dnfrepo.SeriaWelcomeActiveAt(character, now) {
					current := max(character.Stats["seria_returnee_disjoint_count"], 0)
					character.Stats["seria_returnee_disjoint_count"] = current + min(count, int64(^uint64(0)>>1)-current)
				}
				character.UpdatedAt = now
				if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// CreditEpicPiece changes only the account snapshot owned by this transaction.
// Equipment consumption, other rewards and this credit commit or roll back together.
func CreditEpicPiece(assets *Assets, pieceID, count uint32) (uint32, error) {
	if assets == nil || assets.Account == nil {
		return 0, ErrOwnerUnavailable
	}
	if pieceID == 0 || count == 0 {
		return 0, dnfrepo.ErrAccountEpicPieceIDRequired
	}
	account := assets.Account
	key := dnfrepo.AccountEpicPieceMetadataKey(pieceID)
	amount := uint64(dnfrepo.ParseAccountEpicPieceBalance(account.Metadata[key])) + uint64(count)
	amount = min(amount, uint64(dnfrepo.AccountEpicPieceBalanceMax))
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata[key] = dnfrepo.FormatAccountEpicPieceBalance(uint32(amount))
	return uint32(amount), nil
}
