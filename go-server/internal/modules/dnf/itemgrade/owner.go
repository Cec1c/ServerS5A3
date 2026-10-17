package itemgrade

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable    = errors.New("item grade owner unavailable")
	ErrCharacterRequired   = errors.New("selected character id required")
	ErrTargetMissing       = errors.New("item grade target missing")
	ErrMaterialMissing     = errors.New("item grade material missing")
	ErrTargetTypeNotUsable = errors.New("item grade material does not accept the target equipment type")
	ErrMaterialTypeInvalid = errors.New("item grade material definition is invalid")
)

// ItemDefinition is the runtime-PVF view of one item this owner needs:
// the material's [stackable type] and, when the adjuster declares one, its
// [usable equip type] whitelist.
type ItemDefinition struct {
	StackableType  string
	ResealMaterial bool
	// UsableEquipTypes is the adjuster's PVF [usable equip type] list. An empty
	// list means the item is not slot-restricted (the reference implementation
	// documents the same rule: "空表示不限部位"), which is why the plain
	// 装备品级调整箱 (item 15, no such section) works on every slot.
	UsableEquipTypes []string
}

type ItemCatalog interface {
	ResolveItem(uint32) (ItemDefinition, error)
}

type SeedGenerator func() (uint32, error)

type Command struct {
	SelectedCharacterID uint16
	TargetSlot          int16
	TargetItemID        int32
	MaterialSlot        int16
	// TargetEquipmentType is the target's runtime-PVF [equipment type]
	// (for example `[earring]`). Empty means the bridge could not resolve the
	// target and the material's whitelist must not newly reject it.
	TargetEquipmentType string
	UpdatedAt           time.Time
}

type Result struct {
	CharacterID       string
	TargetSlot        int16
	TargetItemID      int32
	TargetStack       dnfrepo.ItemStack
	MaterialSlot      int16
	MaterialItemID    int64
	MaterialRemaining int64
	MaterialRemoved   bool
	NewSeed           uint32
	GoldKaleido       bool
}

// Owner owns kaleido validation, quality selection, material consumption, and
// the durable inventory write.
type Owner struct {
	inventory dnfrepo.InventoryRepository
	catalog   ItemCatalog
	newSeed   SeedGenerator
}

func NewOwner(repos dnfrepo.Group, catalog ItemCatalog, newSeed SeedGenerator) (*Owner, error) {
	if repos.Inventory == nil {
		return nil, ErrOwnerUnavailable
	}
	if newSeed == nil {
		newSeed = itemquality.NewRandomSeed
	}
	return &Owner{inventory: repos.Inventory, catalog: catalog, newSeed: newSeed}, nil
}

func (o *Owner) Adjust(ctx context.Context, cmd Command) (Result, error) {
	if o == nil || o.inventory == nil || o.newSeed == nil {
		return Result{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return Result{}, ErrCharacterRequired
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	inventory, found, err := o.inventory.Load(ctx, characterID)
	if err != nil {
		return Result{}, err
	}
	if !found || strings.TrimSpace(inventory.CharacterID) != characterID {
		return Result{}, ErrOwnerUnavailable
	}
	inventory = dnfrepo.CloneInventory(inventory)

	targetKey := inventoryKey(cmd.TargetSlot)
	target, found := inventory.Slots[targetKey]
	if !found || target.ItemID != int64(cmd.TargetItemID) || target.ItemID <= 0 {
		return Result{}, fmt.Errorf("%w: slot=%d item=%d", ErrTargetMissing, cmd.TargetSlot, cmd.TargetItemID)
	}
	materialKey := inventoryKey(cmd.MaterialSlot)
	material, found := inventory.Slots[materialKey]
	if !found || material.ItemID <= 0 || material.Count <= 0 {
		return Result{}, fmt.Errorf("%w: slot=%d", ErrMaterialMissing, cmd.MaterialSlot)
	}

	// 材料白名单（运行时 PVF [usable equip type]）：现场报障「耳环无法使用品级
	// 调整器」的判据就在这里——黄金品级调整箱按武器/防具/首饰/特殊装备分箱，
	// 首饰箱只列 [amulet] [ring] [wrist]、特殊装备箱只列 [support] [magic stone]，
	// 两份都不含 [earring]；而 490701689「黄金品级调整箱 (Lv1~90)」与
	// 490701814「黄金品级调整箱 (特殊装备)」显式列了 [earring]。
	// 表为空 = 不限部位（普通装备品级调整箱 item 15 没有这一段）。
	// 86JP/A21 参考实现是同一口径：ResetItemQualityPolicyResolver.Allows =
	// IsResettableEquipmentType(type) && (!HasExplicitEquipmentTypes ||
	// _allowedEquipmentTypes.Contains(type))。
	materialDefinition, definitionFound := o.itemDefinition(material.ItemID)
	// 目录暂不可用时也不能把标准黄金蜜蜡当作普通品级材料。
	if material.ItemID == 14 || definitionFound && materialDefinition.ResealMaterial {
		return Result{}, ErrMaterialTypeInvalid
	}
	if definitionFound && !AllowsTargetEquipmentType(materialDefinition, cmd.TargetEquipmentType) {
		return Result{}, fmt.Errorf("%w: slot=%d item=%d target_type=%q usable=%v",
			ErrTargetTypeNotUsable,
			cmd.TargetSlot,
			cmd.TargetItemID,
			NormalizeEquipmentType(cmd.TargetEquipmentType),
			materialDefinition.UsableEquipTypes,
		)
	}

	goldKaleido := isGoldKaleidoDefinition(materialDefinition, definitionFound)
	seed := itemquality.TopSeed
	if !goldKaleido {
		seed, err = o.newSeed()
		if err != nil {
			return Result{}, err
		}
	}
	if err := itemquality.Apply(&target, seed); err != nil {
		return Result{}, err
	}
	// A grade-adjust target is necessarily equipment. Normalize legacy rows so
	// every later inventory projection keeps using the quality seed instead of
	// falling back to the ordinary stack count.
	target.Extra["item_kind"] = "equipment"
	now := cmd.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	target.Extra["last_grade_adjust"] = now.Format(time.RFC3339)
	if goldKaleido {
		target.Extra["grade_adjust_type"] = "gold_kaleido"
	} else {
		target.Extra["grade_adjust_type"] = "standard_kaleido"
	}
	inventory.Slots[targetKey] = target

	material.Count--
	materialRemoved := material.Count == 0
	if materialRemoved {
		delete(inventory.Slots, materialKey)
	} else {
		inventory.Slots[materialKey] = material
	}
	inventory.UpdatedAt = now
	if err := dnfrepo.SaveInventoryFields(ctx, o.inventory, inventory, dnfrepo.InventoryFieldSlots); err != nil {
		return Result{}, err
	}
	return Result{
		CharacterID:       characterID,
		TargetSlot:        cmd.TargetSlot,
		TargetItemID:      cmd.TargetItemID,
		TargetStack:       target,
		MaterialSlot:      cmd.MaterialSlot,
		MaterialItemID:    material.ItemID,
		MaterialRemaining: material.Count,
		MaterialRemoved:   materialRemoved,
		NewSeed:           seed,
		GoldKaleido:       goldKaleido,
	}, nil
}

func (o *Owner) itemDefinition(itemID int64) (ItemDefinition, bool) {
	if o == nil || o.catalog == nil || itemID <= 0 || itemID > int64(^uint32(0)) {
		return ItemDefinition{}, false
	}
	definition, err := o.catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return ItemDefinition{}, false
	}
	return definition, true
}

func (o *Owner) isGoldKaleido(itemID int64) bool {
	definition, found := o.itemDefinition(itemID)
	return isGoldKaleidoDefinition(definition, found)
}

// isGoldKaleidoDefinition preserves the current-client/PVF rule: the gold
// marker alone selects the top-quality sentinel, including localized type names
// without "kaleido".
func isGoldKaleidoDefinition(definition ItemDefinition, found bool) bool {
	if !found {
		return false
	}
	normalized := strings.ToLower(strings.ReplaceAll(definition.StackableType, "`", ""))
	return strings.Contains(normalized, "gold")
}

// AllowsTargetEquipmentType 判定一件调整材料能不能作用在目标部位上。
//
// 唯一判据 = 运行时 PVF `[usable equip type]`：表非空时目标的 `[equipment type]`
// 必须在表内；表为空 = 不限部位。目标部位解析不出来（装备不在
// equipment/equipment.lst 等）时**不新拦**，返回 true——现状是服务端完全不看部位，
// 新引入的白名单只用于拦"材料自己声明了不允许的部位"，绝不把当前客户端允许的
// 操作变成服务端拒绝。
func AllowsTargetEquipmentType(definition ItemDefinition, targetEquipmentType string) bool {
	if len(definition.UsableEquipTypes) == 0 {
		return true
	}
	target := NormalizeEquipmentType(targetEquipmentType)
	if target == "" {
		return true
	}
	for _, allowed := range definition.UsableEquipTypes {
		if NormalizeEquipmentType(allowed) == target {
			return true
		}
	}
	return false
}

// NormalizeEquipmentType lowercases a PVF equipment-type token and strips the
// backticks/spaces the PVF text form carries (`[earring]` -> [earring]).
func NormalizeEquipmentType(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return strings.TrimSpace(strings.Trim(raw, "`"))
}

func inventoryKey(slot int16) string {
	return fmt.Sprintf("%d:%d", dnfrepo.MainInventoryListType, slot)
}
