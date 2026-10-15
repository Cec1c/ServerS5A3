// 本文件定义 DNF 背包仓储接口、记录和字段保存入口。
package repository

import (
	"context"
	"strings"
	"time"

	"longheng.io/server/internal/platform/db"
)

// InventoryRepository 保存角色背包和仓库状态。
type InventoryRepository interface {
	db.Store[InventoryRecord]
}

// InventoryRecord 是角色背包仓储记录。
type InventoryRecord struct {
	CharacterID string               `json:"character_id"`
	Slots       map[string]ItemStack `json:"slots,omitempty"`
	Warehouse   map[string]ItemStack `json:"warehouse,omitempty"`
	UpdatedAt   time.Time            `json:"updated_at,omitempty"`
	// ContainerState 是本次发放读取的容量快照，不写入背包表或物品元数据。
	ContainerState *CharacterContainerState `json:"-"`
}

// ItemStack 是背包或仓库中的一组物品。
type ItemStack struct {
	ItemID   int64             `json:"item_id"`
	Count    int64             `json:"count"`
	Bind     bool              `json:"bind,omitempty"`
	ExpireAt time.Time         `json:"expire_at,omitempty"`
	RawEntry []byte            `json:"raw_entry,omitempty"`
	Extra    map[string]string `json:"extra,omitempty"`
}

// SortLockExtraKey 是物品栈上「物品栏位置锁定」（DOC-067）的持久化键。
// 值为 "1" 表示锁定；键不存在表示未锁。放在 repository 侧是因为
// 背包 owner 与穿戴 owner 都要读写它，而 equip 不能反向 import inventory。
const SortLockExtraKey = "sort_lock"

// InventoryField 表示背包记录可局部保存的字段。
type InventoryField string

const (
	InventoryFieldSlots     InventoryField = "slots"
	InventoryFieldWarehouse InventoryField = "warehouse"
)

// SaveInventoryFields 保存背包指定字段；底层不支持局部保存时退化为整条保存。
func SaveInventoryFields(ctx context.Context, repo InventoryRepository, record InventoryRecord, fields ...InventoryField) error {
	record.ContainerState = nil
	return db.SaveFields(ctx, repo, record, InventoryFields.Normalize, fields...)
}

// CloneInventory 拷贝背包记录，避免物品 Extra map 和 slot map 与调用方共享。
func CloneInventory(record InventoryRecord) InventoryRecord {
	record.Slots = cloneItemMap(record.Slots)
	record.Warehouse = cloneItemMap(record.Warehouse)
	if record.ContainerState != nil {
		state := *record.ContainerState
		record.ContainerState = &state
	}
	return record
}

// BindInventoryContainerState 读取真实扩容记录；未有扩容记录时保持基础容量，
// 不创建设置行、不自动全开，也不隐藏已有越界物品。
func BindInventoryContainerState(ctx context.Context, settings SettingsRepository, record *InventoryRecord) error {
	if record == nil {
		return ErrCharacterContainerStateInvalid
	}
	if settings == nil {
		return nil
	}
	state, found, err := LoadCharacterContainerState(ctx, settings, record.CharacterID)
	if err != nil {
		return err
	}
	record.ContainerState = nil
	if found {
		record.ContainerState = &state
	}
	return nil
}

func InventoryKey(record InventoryRecord) string {
	return strings.TrimSpace(record.CharacterID)
}
