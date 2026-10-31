package cerashop

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const accountCeraMetadataKey = "account_cera"

var (
	ErrOwnerUnavailable  = errors.New("cera shop owner is unavailable")
	ErrAccountNotFound   = errors.New("cera shop account is not found")
	ErrCharacterNotFound = errors.New("cera shop character is not found")
	ErrInventoryNotFound = errors.New("cera shop inventory is not found")
	ErrAccountMismatch   = errors.New("cera shop character account mismatch")
	ErrCeraInsufficient  = errors.New("cera shop cera balance is insufficient")
	ErrProjectorRequired = errors.New("cera shop checkout projector is required")
)

type CheckoutChanges struct {
	Character bool
	Inventory bool
	Equipment bool
	Settings  bool
	Skill     bool
}

type CheckoutAssets struct {
	Account        *dnfrepo.AccountRecord
	Character      *dnfrepo.CharacterRecord
	Inventory      *dnfrepo.InventoryRecord
	Equipment      *dnfrepo.EquipmentRecord
	Settings       *dnfrepo.SettingsRecord
	EquipmentFound bool
	SettingsFound  bool
	Skill          *dnfrepo.SkillRecord
	SkillFound     bool
}

type CheckoutProjector func(*CheckoutAssets) (CheckoutChanges, error)

type CheckoutCommand struct {
	PayPolicy PayPolicy
	// 受PVF限制的金额先预留，余款再按PayPolicy扣除，支持混合购物车。
	CeraOnlyCost, TokenOnlyCost int64
	AccountID                   string
	CharacterID                 string
	SettingsScope               string
	Cost                        int64
	UpdatedAt                   time.Time
	IncludeSkills               bool
	Project                     CheckoutProjector
	// Preflight executes under the account transaction before charging. It is
	// used by the private festival checkout to reject transport retries.
	Preflight func(*dnfrepo.AccountRecord) error
}

type CheckoutResult struct {
	WalletBefore, WalletAfter Wallet
	Payment                   WalletPayment
	CeraBefore                int64
	CeraAfter                 int64
}

type Owner struct {
	assets dnfrepo.CeraShopAssetUnitOfWork
}

func NewOwner(repositories dnfrepo.Group) (*Owner, error) {
	if repositories.CeraShopAssets == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{assets: repositories.CeraShopAssets}, nil
}

func (o *Owner) Checkout(ctx context.Context, command CheckoutCommand) (CheckoutResult, error) {
	accountID := strings.TrimSpace(command.AccountID)
	characterID := strings.TrimSpace(command.CharacterID)
	settingsScope := strings.TrimSpace(command.SettingsScope)
	if o == nil || o.assets == nil || accountID == "" || characterID == "" || settingsScope == "" || command.Cost < 0 {
		return CheckoutResult{}, ErrOwnerUnavailable
	}
	if command.Project == nil {
		return CheckoutResult{}, ErrProjectorRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CheckoutResult{}, err
	}
	now := command.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var result CheckoutResult
	within := func(ctx context.Context, accountID, characterID, scope string, apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.SettingsRepository, dnfrepo.SkillRepository) error) error {
		return o.assets.WithinCeraShopAssets(ctx, accountID, characterID, scope, func(a dnfrepo.AccountRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository, s dnfrepo.SettingsRepository) error {
			return apply(a, c, i, e, s, nil)
		})
	}
	if command.IncludeSkills {
		extended, ok := o.assets.(dnfrepo.CeraShopSkillAssetUnitOfWork)
		if !ok {
			return CheckoutResult{}, ErrOwnerUnavailable
		}
		within = extended.WithinCeraShopSkillAssets
	}
	err := within(
		ctx,
		accountID,
		characterID,
		settingsScope,
		func(
			accounts dnfrepo.AccountRepository,
			characters dnfrepo.CharacterRepository,
			inventories dnfrepo.InventoryRepository,
			equipment dnfrepo.EquipmentRepository,
			settings dnfrepo.SettingsRepository,
			skills dnfrepo.SkillRepository,
		) error {
			if accounts == nil || characters == nil || inventories == nil || equipment == nil || settings == nil {
				return ErrOwnerUnavailable
			}
			account, found, err := accounts.Load(ctx, accountID)
			if err != nil {
				return err
			}
			if !found {
				return ErrAccountNotFound
			}
			character, found, err := characters.Load(ctx, characterID)
			if err != nil {
				return err
			}
			if !found {
				return ErrCharacterNotFound
			}
			if strings.TrimSpace(character.AccountID) != accountID {
				return ErrAccountMismatch
			}
			inventory, found, err := inventories.Load(ctx, characterID)
			if err != nil {
				return err
			}
			if !found || strings.TrimSpace(inventory.CharacterID) != characterID {
				return ErrInventoryNotFound
			}
			equipmentRecord, equipmentFound, err := equipment.Load(ctx, characterID)
			if err != nil {
				return err
			}
			if !equipmentFound {
				equipmentRecord = dnfrepo.EquipmentRecord{CharacterID: characterID}
			}
			settingsRecord, settingsFound, err := settings.Load(ctx, settingsScope)
			if err != nil {
				return err
			}
			if !settingsFound {
				settingsRecord = dnfrepo.SettingsRecord{Scope: settingsScope}
			}

			skillRecord := dnfrepo.SkillRecord{CharacterID: characterID}
			skillFound := false
			if command.IncludeSkills {
				if skills == nil {
					return ErrOwnerUnavailable
				}
				skillRecord, skillFound, err = skills.Load(ctx, characterID)
				if err != nil {
					return err
				}
				if !skillFound {
					skillRecord.CharacterID = characterID
				}
				skillRecord = dnfrepo.CloneSkill(skillRecord)
			}
			account = dnfrepo.CloneAccount(account)
			character = dnfrepo.CloneCharacter(character)
			inventory = dnfrepo.CloneInventory(inventory)
			equipmentRecord = dnfrepo.CloneEquipment(equipmentRecord)
			settingsRecord = dnfrepo.CloneSettings(settingsRecord)

			if command.Preflight != nil {
				if err := command.Preflight(&account); err != nil {
					return err
				}
			}
			walletBefore := WalletBalance(account)
			if command.CeraOnlyCost < 0 || command.TokenOnlyCost < 0 || command.CeraOnlyCost > command.Cost || command.TokenOnlyCost > command.Cost-command.CeraOnlyCost {
				return ErrOwnerUnavailable
			}
			var payment WalletPayment
			for _, charge := range []struct {
				cost   int64
				policy PayPolicy
			}{{command.CeraOnlyCost, PayPolicyCeraOnly}, {command.TokenOnlyCost, PayPolicyTokenCeraOnly}, {command.Cost - command.CeraOnlyCost - command.TokenOnlyCost, command.PayPolicy}} {
				part, e := PlanWalletPayment(WalletBalance(account), charge.cost, charge.policy)
				if e != nil {
					return fmt.Errorf("%w: %w", ErrCeraInsufficient, e)
				}
				ApplyWalletPayment(&account, part)
				payment.FromCera += part.FromCera
				payment.FromTokenCera += part.FromTokenCera
				payment.FromHappyTokenCera += part.FromHappyTokenCera
			}
			walletAfter := WalletBalance(account)
			ceraBefore, ceraAfter := walletBefore.Cera, walletAfter.Cera

			assets := &CheckoutAssets{
				Account:        &account,
				Character:      &character,
				Inventory:      &inventory,
				Equipment:      &equipmentRecord,
				Settings:       &settingsRecord,
				EquipmentFound: equipmentFound,
				SettingsFound:  settingsFound,
				Skill:          &skillRecord, SkillFound: skillFound,
			}
			changes, err := command.Project(assets)
			if err != nil {
				return err
			}

			account.UpdatedAt = now
			if err := accounts.Save(ctx, account); err != nil {
				return err
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
			if changes.Equipment {
				equipmentRecord.UpdatedAt = now
				if err := dnfrepo.SaveEquipmentFields(ctx, equipment, equipmentRecord, dnfrepo.EquipmentFieldEntries); err != nil {
					return err
				}
			}
			if changes.Settings {
				settingsRecord.UpdatedAt = now
				if err := dnfrepo.SaveSettingsFields(ctx, settings, settingsRecord, dnfrepo.SettingsFieldValues); err != nil {
					return err
				}
			}
			if changes.Skill {
				if !command.IncludeSkills || skills == nil {
					return ErrOwnerUnavailable
				}
				skillRecord.UpdatedAt = now
				if err := dnfrepo.SaveSkillFields(ctx, skills, skillRecord, dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldPoints, dnfrepo.SkillFieldLayouts, dnfrepo.SkillFieldTrees); err != nil {
					return err
				}
			}
			result = CheckoutResult{CeraBefore: ceraBefore, CeraAfter: ceraAfter, WalletBefore: walletBefore, WalletAfter: walletAfter, Payment: payment}
			return nil
		},
	)
	if err != nil {
		return CheckoutResult{}, err
	}
	return result, nil
}

func Balance(account dnfrepo.AccountRecord) int64 {
	raw := strings.TrimSpace(account.Metadata[accountCeraMetadataKey])
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func SetBalance(account *dnfrepo.AccountRecord, balance int64) {
	if account == nil {
		return
	}
	if account.Metadata == nil {
		account.Metadata = make(map[string]string, 4)
	}
	if balance < 0 {
		balance = 0
	}
	account.Metadata[accountCeraMetadataKey] = strconv.FormatInt(balance, 10)
}
