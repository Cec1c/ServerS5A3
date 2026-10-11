// Package avataroptionchange 拥有「装扮属性调整箱」（时装属性调整箱）的一次性物品
// 事务：扣掉主背包里的一个箱子，并把装扮栏里那件装扮的 `AbilityNo` 改成玩家选的
// 属性下标。
//
// # 口径来源（照抄厂商 A21，不自行设计）
//
// ServerS4A21-master/Server/DfoServer/Game/Inventory/Services/InventoryAvatarOptionChangeService.cs
// 的 `TryChange` 校验顺序与 `InventoryAvatarOptionChangeError` 分类逐条对应：
//
//	InvalidRequest          请求自身非法（负槽位 / itemId<=0）
//	SourceNotFound          主背包源槽不存在、itemId<=0、不是可堆叠品、或 count<1
//	SourceItemMismatch      主背包源槽 itemId 与请求不一致
//	NotOptionChangeBox      源的 PVF stackable 没有 `[avatar select ability change]`
//	TargetNotFound          装扮栏目标槽不存在或 itemId<=0
//	TargetItemMismatch      目标槽 itemId 与请求不一致
//	NotAvatar               目标不是装扮
//	AvatarMetadataMissing   目标装扮的 PVF 装备文件读不到
//	AvatarGradeMismatch     箱子的 AvatarGrade 列表里没有该装扮的 `[grade]`
//
// 两条必须保留的厂商语义：
//
//  1. **能力值由客户端选定，服务端不生成、不校验具体能力**。厂商只把请求里的
//     `AbilityNo` 原样写进目标行（`updatedTarget.AbilityNo = request.AbilityNo`），
//     从不查表、从不重掷。现场抓包也印证了这一点：被调整的 40734「深渊骑士胸甲」
//     在真档里**没有** `[avatar select ability]` 段，只有 `[ability case index] 50`，
//     而客户端上送的 abilityNo=47 < 50 ⇒ 可选项清单根本不在服务端能读到的地方。
//
//  2. **箱子只按 AvatarGrade 比对目标装扮的 `[grade]`**（厂商
//     `CanChangeAvatarGrade` 只比 `entry.AvatarGrade == avatarGrade`）。
//     `[avatar select ability change]` 行里的 source/target scope 厂商**不校验**，
//     这里同样不校验——多拦一条就会把现场已验证合法的操作变成服务端拒绝。
//
// # 事务边界
//
// 与 modules/dnf/avatarcompound 同一套 owner 写法：整次改动落在
// `dnfrepo.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets` 里，
// 校验、扣箱、写属性、重建行投影全在同一个账号-角色资产事务内完成，任何一步失败
// 整体回滚（不会出现"箱子扣了但属性没改"）。
package avataroptionchange

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable  = errors.New("avatar option change owner is unavailable")
	ErrAccountRequired   = errors.New("avatar option change account id is required")
	ErrCharacterRequired = errors.New("avatar option change character id is required")
	ErrCharacterNotFound = errors.New("avatar option change character is not found")
	ErrAccountMismatch   = errors.New("avatar option change character account mismatch")
	ErrInventoryNotFound = errors.New("avatar option change inventory is not found")
	ErrProjectorRequired = errors.New("avatar option change row projector is required")
)

// 与厂商 InventoryAvatarOptionChangeError 一一对应的失败分类。顺序即校验顺序。
var (
	ErrInvalidRequest        = errors.New("avatar option change request is invalid")
	ErrSourceNotFound        = errors.New("avatar option change source is missing or not usable")
	ErrSourceItemMismatch    = errors.New("avatar option change source item does not match the request")
	ErrNotOptionChangeBox    = errors.New("avatar option change source is not an avatar option change box")
	ErrTargetNotFound        = errors.New("avatar option change target is missing")
	ErrTargetItemMismatch    = errors.New("avatar option change target item does not match the request")
	ErrNotAvatar             = errors.New("avatar option change target is not an avatar")
	ErrAvatarMetadataMissing = errors.New("avatar option change target avatar metadata is missing")
	ErrAvatarGradeMismatch   = errors.New("avatar option change box does not accept the target avatar grade")
)

// ItemKind 是运行时 PVF 侧的物品大类；与 pvfDungeonDropCatalog 的两种引用一致。
type ItemKind string

const (
	KindStackable ItemKind = "stackable"
	KindEquipment ItemKind = "equipment"
)

// ItemDefinition 是本 owner 需要的运行时 PVF 视图。
//
// 只带厂商同一口径下用到的字段：箱子的 `[avatar select ability change]` 是否存在
// 及其 AvatarGrade 列，装扮的 `[grade]` 与是不是装扮。
type ItemDefinition struct {
	Kind ItemKind
	// AvatarGrades 是 stackable 的 `[avatar select ability change]` 每行第一列
	// （厂商 `AvatarSelectAbilityChangeEntry.AvatarGrade`）。非空即
	// `HasAvatarSelectAbilityChange`。
	AvatarGrades []int64
	// Grade 是 equipment 的 `[grade]`；装扮品级：1 普通 / 2 高级 / 3 稀有。
	Grade int64
	// EquipmentType 是 equipment 的 `[equipment type]` 原文（例如 `[coat avatar]`）。
	EquipmentType string
	// IsAvatar 由 bridge 用既有的 currentEquipmentPlacementRuleForPVFType 判定，
	// 模块层不复制那张表。
	IsAvatar bool
}

// ItemCatalog 是运行时 PVF 的只读入口。
type ItemCatalog interface {
	ResolveItem(uint32) (ItemDefinition, error)
}

// AvatarRowProjector 在事务内把目标装扮行（0x77 行）重建成携带新属性字的形状。
// 调用时 extra 的 AbilityNoExtraKey 已经写好，投影只负责让持久化的 raw 行与 extra
// 一致。由 bridge 注入，复用既有的 currentItemListEntryFromStack +
// applyCurrentItemListAvatarAbilityRow，模块层不复制行布局常量。
type AvatarRowProjector func(stack *dnfrepo.ItemStack)

// Command 描述一次「使用装扮属性调整箱」事务。
type Command struct {
	AccountID   string
	CharacterID string
	UpdatedAt   time.Time

	// SourceSlot/SourceItemID 是主背包（list 0）里的箱子。
	SourceSlot   int16
	SourceItemID int32
	// TargetSlot/TargetItemID 是装扮栏（list 1）里被调整的那件装扮。
	TargetSlot   int16
	TargetItemID int32
	// AbilityNo 是客户端选定的属性下标，原样落库，服务端不生成也不校验具体能力。
	AbilityNo uint8

	ProjectAvatarRow AvatarRowProjector
}

// Result 是一次成功调整的结果，供回包与刷新使用。
type Result struct {
	CharacterID string
	// Source* 描述被扣掉 1 个的箱子。
	SourceSlot      int16
	SourceItemID    int64
	SourceRemaining int64
	SourceRemoved   bool
	// Target* 描述被改写的装扮。
	TargetSlot      int16
	TargetItemID    int64
	TargetAbilityNo uint8
}

// Owner 拥有装扮属性调整箱所需的角色背包原子写入。
type Owner struct {
	inventory dnfrepo.InventoryRepository
	assets    dnfrepo.AccountCharacterAssetUnitOfWork
	catalog   ItemCatalog
}

// NewOwner 创建 owner。catalog 可以为 nil（此时任何箱子都无法被证明合法，
// 校验会以 NotOptionChangeBox / AvatarMetadataMissing 拒绝，绝不臆造）。
func NewOwner(repositories dnfrepo.Group, catalog ItemCatalog) (*Owner, error) {
	if repositories.AccountAssets == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{inventory: repositories.Inventory, assets: repositories.AccountAssets, catalog: catalog}, nil
}

// ChangeAvatarOption 在一个账号-角色资产事务里执行一次属性调整。
func (o *Owner) ChangeAvatarOption(ctx context.Context, command Command) (Result, error) {
	if o == nil || o.assets == nil {
		return Result{}, ErrOwnerUnavailable
	}
	accountID := strings.TrimSpace(command.AccountID)
	characterID := strings.TrimSpace(command.CharacterID)
	if accountID == "" {
		return Result{}, ErrAccountRequired
	}
	if characterID == "" {
		return Result{}, ErrCharacterRequired
	}
	if command.ProjectAvatarRow == nil {
		return Result{}, ErrProjectorRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := command.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var result Result
	err := o.assets.WithinAccountCharacterAssets(ctx, accountID, characterID, func(
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
			return fmt.Errorf("%w: character=%s", ErrCharacterNotFound, characterID)
		}
		if strings.TrimSpace(character.AccountID) != accountID {
			return fmt.Errorf("%w: account=%s character=%s", ErrAccountMismatch, accountID, characterID)
		}
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != characterID || inventory.Slots == nil {
			return fmt.Errorf("%w: character=%s", ErrInventoryNotFound, characterID)
		}
		inventory = dnfrepo.CloneInventory(inventory)

		committed, err := o.project(&inventory, command)
		if err != nil {
			return err
		}
		inventory.UpdatedAt = now
		if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		result = committed
		result.CharacterID = characterID
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// project 是事务内的"校验 + 应用"，顺序照抄厂商 TryChange。
func (o *Owner) project(
	inventory *dnfrepo.InventoryRecord,
	command Command,
) (Result, error) {
	if inventory == nil || inventory.Slots == nil {
		return Result{}, ErrInventoryNotFound
	}
	if command.SourceSlot < 0 || command.TargetSlot < 0 ||
		command.SourceItemID <= 0 || command.TargetItemID <= 0 {
		return Result{}, fmt.Errorf("%w: source_slot=%d target_slot=%d source_item=%d target_item=%d",
			ErrInvalidRequest, command.SourceSlot, command.TargetSlot, command.SourceItemID, command.TargetItemID)
	}

	sourceKey := inventorySlotKey(dnfrepo.MainInventoryListType, command.SourceSlot)
	source, found := inventory.Slots[sourceKey]
	if !found || source.ItemID <= 0 {
		return Result{}, fmt.Errorf("%w: slot=%d", ErrSourceNotFound, command.SourceSlot)
	}
	if source.ItemID != int64(command.SourceItemID) {
		return Result{}, fmt.Errorf("%w: slot=%d want=%d got=%d",
			ErrSourceItemMismatch, command.SourceSlot, command.SourceItemID, source.ItemID)
	}
	sourceDefinition, definitionErr := o.itemDefinition(source.ItemID)
	if definitionErr != nil || sourceDefinition.Kind != KindStackable || source.Count < 1 {
		// 厂商 `!InventoryStackRuleService.IsStackable(source) || source.Count < 1`
		// 同样归到 SourceNotFound。
		return Result{}, fmt.Errorf("%w: slot=%d item=%d count=%d err=%v",
			ErrSourceNotFound, command.SourceSlot, source.ItemID, source.Count, definitionErr)
	}
	if len(sourceDefinition.AvatarGrades) == 0 {
		// 厂商 `!TryLoadStackableFile(...) || !HasAvatarSelectAbilityChange`
		// ⇒ NotOptionChangeBox：源不是"装扮属性调整箱"。
		return Result{}, fmt.Errorf("%w: slot=%d item=%d", ErrNotOptionChangeBox, command.SourceSlot, source.ItemID)
	}

	targetKey := inventorySlotKey(1, command.TargetSlot)
	target, found := inventory.Slots[targetKey]
	if !found || target.ItemID <= 0 {
		return Result{}, fmt.Errorf("%w: slot=%d", ErrTargetNotFound, command.TargetSlot)
	}
	if target.ItemID != int64(command.TargetItemID) {
		return Result{}, fmt.Errorf("%w: slot=%d want=%d got=%d",
			ErrTargetItemMismatch, command.TargetSlot, command.TargetItemID, target.ItemID)
	}
	targetDefinition, definitionErr := o.itemDefinition(target.ItemID)
	if definitionErr != nil {
		// 厂商 `!ItemMetadataResolver.TryLoadEquipmentFile(...)` ⇒ AvatarMetadataMissing。
		return Result{}, fmt.Errorf("%w: slot=%d item=%d err=%v",
			ErrAvatarMetadataMissing, command.TargetSlot, target.ItemID, definitionErr)
	}
	if targetDefinition.Kind != KindEquipment || !targetDefinition.IsAvatar {
		// 厂商 `target.ItemKind != ItemCore.KindAvatar` ⇒ NotAvatar。
		return Result{}, fmt.Errorf("%w: slot=%d item=%d type=%q",
			ErrNotAvatar, command.TargetSlot, target.ItemID, targetDefinition.EquipmentType)
	}
	if !avatarGradeAccepted(sourceDefinition.AvatarGrades, targetDefinition.Grade) {
		return Result{}, fmt.Errorf("%w: slot=%d item=%d grade=%d box_grades=%v",
			ErrAvatarGradeMismatch, command.TargetSlot, target.ItemID, targetDefinition.Grade, sourceDefinition.AvatarGrades)
	}

	result := Result{
		SourceSlot:      command.SourceSlot,
		SourceItemID:    source.ItemID,
		TargetSlot:      command.TargetSlot,
		TargetItemID:    target.ItemID,
		TargetAbilityNo: command.AbilityNo,
	}
	source.Count--
	result.SourceRemaining = source.Count
	if source.Count <= 0 {
		delete(inventory.Slots, sourceKey)
		result.SourceRemoved = true
	} else {
		inventory.Slots[sourceKey] = source
	}

	if target.Extra == nil {
		target.Extra = make(map[string]string, 4)
	}
	target.Extra[AbilityNoExtraKey] = FormatAbilityNo(command.AbilityNo)
	command.ProjectAvatarRow(&target)
	inventory.Slots[targetKey] = target
	return result, nil
}

func (o *Owner) itemDefinition(itemID int64) (ItemDefinition, error) {
	if o == nil || o.catalog == nil || itemID <= 0 || itemID > int64(^uint32(0)) {
		return ItemDefinition{}, ErrOwnerUnavailable
	}
	return o.catalog.ResolveItem(uint32(itemID))
}

// avatarGradeAccepted 就是厂商 CanChangeAvatarGrade：箱子的 AvatarGrade 列表里
// 必须存在与装扮 `[grade]` 相等的条目（⇒ 稀有箱只作用于稀有装扮）。
// source/target scope 按厂商口径**不校验**。
func avatarGradeAccepted(grades []int64, avatarGrade int64) bool {
	for _, grade := range grades {
		if grade == avatarGrade {
			return true
		}
	}
	return false
}

func inventorySlotKey(listType byte, slot int16) string {
	return fmt.Sprintf("%d:%d", listType, slot)
}

// AbilityNoExtraKey 是装扮行里"玩家选定属性下标"的持久化键，与发放端
// （礼包 op518 / 商城 / 合成 op99）写入的键一致；bridge 侧
// currentItemListAvatarAbility 首选读取它。
const AbilityNoExtraKey = "ext_data0"

// FormatAbilityNo 把属性下标写成 extra 值。抽出来是为了让 bridge 的注入投影
// 与 owner 用同一个格式化，避免两处各写一份。
func FormatAbilityNo(abilityNo uint8) string {
	return strconv.FormatUint(uint64(abilityNo), 10)
}
