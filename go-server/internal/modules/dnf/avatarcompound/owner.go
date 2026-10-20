// Package avatarcompound 拥有"装扮合成"（时装合成，当前客户端 C2S op99
// ENUM_CMDPACKET_COMPOUND_AVATAR）的一次性物品事务。
//
// 一次合成会同时改动三处资产：两件被消耗的同部位装扮、一个被扣除的合成器
// 消耗品，以及结果装扮的写入。三者必须落在同一个账号-角色资产事务里，
// 任何一步校验失败都不能留下"消耗了但没产出"或"产出了但没消耗"的半成品。
package avatarcompound

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable  = errors.New("avatar compound owner is unavailable")
	ErrAccountRequired   = errors.New("avatar compound account id is required")
	ErrCharacterRequired = errors.New("avatar compound character id is required")
	ErrCharacterNotFound = errors.New("avatar compound character is not found")
	ErrAccountMismatch   = errors.New("avatar compound character account mismatch")
	ErrInventoryNotFound = errors.New("avatar compound inventory is not found")
	ErrProjectorRequired = errors.New("avatar compound mutation projector is required")
)

// Assets 是合成事务内可见的可变资产（当前只涉及角色背包）。
type Assets struct {
	Inventory *dnfrepo.InventoryRecord
}

// Changes 说明本次事务需要落库的字段。
type Changes struct {
	Inventory bool
}

// Projector 在事务内完成"校验 + 应用"；返回错误时整个事务回滚。
type Projector func(*Assets) (Changes, error)

// Command 描述一次装扮合成事务。
type Command struct {
	AccountID   string
	CharacterID string
	UpdatedAt   time.Time
	Project     Projector
}

// Owner 拥有装扮合成所需的角色背包原子写入。
type Owner struct {
	assets dnfrepo.AccountCharacterAssetUnitOfWork
}

// NewOwner 创建装扮合成 owner；缺少账号-角色资产事务时拒绝创建。
func NewOwner(repositories dnfrepo.Group) (*Owner, error) {
	if repositories.AccountAssets == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{assets: repositories.AccountAssets}, nil
}

// CompoundAvatars 在一个账号-角色资产事务里执行装扮合成。
func (o *Owner) CompoundAvatars(ctx context.Context, command Command) error {
	return o.mutate(ctx, "avatar-compound", command)
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

	return o.assets.WithinAccountCharacterAssets(ctx, accountID, characterID, func(
		_ dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		_ dnfrepo.EquipmentRepository,
	) error {
		if characters == nil || inventories == nil {
			return ErrOwnerUnavailable
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
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != characterID || inventory.Slots == nil {
			return fmt.Errorf("%w: character=%s operation=%s", ErrInventoryNotFound, characterID, operation)
		}
		inventory = dnfrepo.CloneInventory(inventory)

		changes, err := command.Project(&Assets{Inventory: &inventory})
		if err != nil {
			return err
		}
		if !changes.Inventory {
			return nil
		}
		inventory.UpdatedAt = now
		return dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots)
	})
}
