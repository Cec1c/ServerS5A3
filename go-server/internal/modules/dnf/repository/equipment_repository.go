// 本文件定义 DNF 穿戴装备仓储接口、记录和字段保存入口。
package repository

import (
	"context"
	"strings"
	"time"

	"longheng.io/server/internal/platform/db"
)

// EquipmentRepository 保存角色穿戴装备 raw entry。
type EquipmentRepository interface {
	db.Store[EquipmentRecord]
}

// EquipmentRecord 是角色穿戴装备的权威记录。
type EquipmentRecord struct {
	CharacterID string                    `json:"character_id"`
	Entries     map[string]EquipmentEntry `json:"entries,omitempty"`
	UpdatedAt   time.Time                 `json:"updated_at,omitempty"`
}

// EquipmentEntry 是单个穿戴槽位的 raw entry。
// RawEntry 按旧客户端 USERINFO/穿戴链路保存原始槽位数据，修理耐久只补 raw[10..11]。
type EquipmentEntry struct {
	SlotIndex int16             `json:"slot_index"`
	ItemID    int64             `json:"item_id"`
	Bind      bool              `json:"bind,omitempty"`
	ExpireAt  time.Time         `json:"expire_at,omitempty"`
	RawEntry  []byte            `json:"raw_entry,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// CloneAvatarAppearanceExtraKey 是"稀有克隆装扮穿在身上时，它要显示成哪一件
// 普通装扮"的持久键，值是被克隆那件装扮的 ItemID 十进制串。它只挂在**穿戴行**
// 的 Extra 上：穿戴行的 ItemID 始终是克隆件本身，只有外观投影把它换成这个值。
//
// 证据：
//   - 厂商 `Server/DfoServer/Game/Inventory/Models/AvatarDetail.cs:19`
//     `AvatarDetail.ClearAvatarId` 是同一个语义的持久整数；
//   - 厂商 `Game/Inventory/Services/InventoryMoveService.cs:950-1005`
//     `SyncAvatarClearAvatarId`：装扮进入穿戴槽且是克隆件、被它顶掉的那件是
//     **非克隆**装扮时写 `previousItemAtSlot.ItemId`，其余情况写 0（脱下即清）；
//   - 同族参考实现 `90原神开源0.1.8 .../repository/equipment_repository.go:35-38`
//     用同一个键名存同一个语义。
const CloneAvatarAppearanceExtraKey = "clone_avatar_appearance_id"

// EquipmentField 表示穿戴装备记录可局部保存的字段。
type EquipmentField string

const (
	EquipmentFieldEntries EquipmentField = "entries"
)

// SaveEquipmentFields 保存穿戴装备指定字段；底层不支持局部保存时退化为整条保存。
func SaveEquipmentFields(ctx context.Context, repo EquipmentRepository, record EquipmentRecord, fields ...EquipmentField) error {
	return db.SaveFields(ctx, repo, record, EquipmentFields.Normalize, fields...)
}

// CloneEquipment 深拷贝穿戴装备记录，避免 raw entry 和 Extra map 被调用方共享污染。
func CloneEquipment(record EquipmentRecord) EquipmentRecord {
	if len(record.Entries) == 0 {
		record.Entries = nil
		return record
	}
	out := make(map[string]EquipmentEntry, len(record.Entries))
	for key, entry := range record.Entries {
		entry.RawEntry = append([]byte(nil), entry.RawEntry...)
		entry.Extra = cloneStringMap(entry.Extra)
		out[key] = entry
	}
	record.Entries = out
	return record
}

func EquipmentKey(record EquipmentRecord) string {
	return strings.TrimSpace(record.CharacterID)
}
