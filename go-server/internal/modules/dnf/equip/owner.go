// 本文件负责角色穿戴装备 owner 的状态修改入口。
// 它只处理装备 raw entry 的可靠写入，不生成旧客户端成功 ACK。
package equip

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/creaturestate"
	"longheng.io/server/internal/modules/dnf/itemquality"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwnerUnavailable        = errors.New("equipment owner unavailable")
	ErrCharacterRequired       = errors.New("selected character id required")
	ErrEquipmentNotFound       = errors.New("equipment record not found")
	ErrSlotNotFound            = errors.New("equipped slot not found")
	ErrRawEntryTooShort        = errors.New("equipped raw entry is too short")
	ErrDurabilityMissing       = errors.New("equipped durability evidence is missing")
	ErrDurabilityInvalid       = errors.New("equipped durability evidence is invalid")
	ErrRepairCostMissing       = errors.New("equipped repair cost evidence is missing")
	ErrRepairCostInvalid       = errors.New("equipped repair cost evidence is invalid")
	ErrRepairNotRepairable     = errors.New("equipped item is not repairable by PVF durability evidence")
	ErrRepairGoldInsufficient  = errors.New("equipped repair gold is insufficient")
	ErrWalletTxnRequired       = errors.New("wallet transaction is required")
	ErrRepairAllRequiresOwners = errors.New("repair all requires equipment and wallet owners")
	ErrInventoryUnavailable    = errors.New("inventory repository unavailable")
	ErrInventoryNotFound       = errors.New("inventory record not found")
	ErrMoveUnsupported         = errors.New("equipment move is unsupported")
	ErrMoveSlotNotFound        = errors.New("equipment move slot not found")
	ErrMoveRawEntryMissing     = errors.New("equipment raw entry evidence is missing")
	ErrMoveRawEntryInvalid     = errors.New("equipment raw entry evidence is invalid")
	ErrMoveStackCountInvalid   = errors.New("equipment move stack count is invalid")
	ErrMoveTransactionRequired = errors.New("equipment move transaction is required")
	ErrMoveSlotOutOfRange      = errors.New("equipment move slot is outside the current EXE range")
	ErrMoveSlotMismatch        = errors.New("equipment entry key and slot do not match")
	ErrMoveValidatorRequired   = errors.New("equipment placement validator is required")
	ErrEquipmentTxnRequired    = errors.New("equipment mutation requires a character item transaction")
	ErrLegacyPVFSlotCollision  = errors.New("legacy PVF equipment slot migration collides with current equipment slot")
	ErrPetRepositoryRequired   = errors.New("pet repository is required for a creature equipment move")
	ErrPetMoveTxnRequired      = errors.New("pet equipment move requires a character pet transaction")
	ErrPetRecordNotFound       = errors.New("pet record not found for creature equipment move")
	ErrPetEntryNotFound        = errors.New("pet entry not found for creature equipment move")
	ErrPetOwnershipMismatch    = errors.New("pet inventory, equipment, and creature record do not match")
)

// Owner 是穿戴装备和耐久的 durable owner 边界。
type Owner struct {
	equipment          dnfrepo.EquipmentRepository
	inventory          dnfrepo.InventoryRepository
	character          dnfrepo.CharacterRepository
	accounts           dnfrepo.AccountRepository
	items              dnfrepo.CharacterItemUnitOfWork
	assets             dnfrepo.CharacterAssetUnitOfWork
	pets               dnfrepo.PetRepository
	petItems           dnfrepo.CharacterPetUnitOfWork
	placementValidator PlacementValidator
	nameTagChecker     func(itemID uint32) bool
	// avatarClassifier 是运行时 PVF 权威（由 dnfbridge 注入），用于区分普通装扮与
	// 「稀有克隆装扮」。为零时装备搬运一个字节都不多写（见 withCloneAvatarAppearance）。
	avatarClassifier  alignedcmd.AvatarClassifier
	inItemTransaction bool
}

// RepairCommand 描述修理穿戴装备的最小命令。
type RepairCommand struct {
	SelectedCharacterID uint16
	AccountID           string
	SlotIndex           int16
	QuickRepair         bool
	AutoRepair          bool
}

// RepairResult 描述穿戴装备耐久修理的写库结果；当前只允许零金币单件修理由上层回成功 ACK。
type RepairResult struct {
	CharacterID   string
	SlotIndex     int16
	ItemID        int64
	OldDurability uint16
	NewDurability uint16
	Cost          int64
	UpdatedGold   int64
	Changed       bool
	// FreeRepair marks the 魔王契约 auto-repair path (premium type 586):
	// body[5]=1 plus an active contract makes the repair cost zero.
	FreeRepair bool
	// RepairedCount is set by RepairAll: how many entries were restored.
	RepairedCount int
}

// MoveCommand 描述装备槽和物品栏之间移动的最小命令。
type MoveCommand struct {
	SelectedCharacterID      uint16
	SourceListType           byte
	SourceSlotIndex          int16
	SourceInstanceValue      int32
	MoveCount                int32
	DestinationListType      byte
	DestinationSlotIndex     int16
	DestinationInstanceValue int32
	// LoginWearWindowOpen 由 dnfbridge 填：这条 op19 落在"进入场景后的登录引导
	// 窗口"内（窗口的开启/关闭依据见 dnfbridge/equip_login_wear_window.go）。
	// 它只对 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP=1 这条**默认关闭**的开关有意义；
	// 开关关闭时（默认档 = 今天的行为）本字段被完全忽略。
	LoginWearWindowOpen bool
	// WornSlotClaimOccurrence 由 dnfbridge 填：这条 op19 若属于"客户端在声明装备槽
	// 穿戴态"那一族（源端 IV=0 + 装备端点点名一件穿着的物品，见
	// worn_slot_reassert_gate.go），它就是**同一个装备槽在当前登录纪元里的第几条**
	// （1 起算）。0 = 桥侧没有计数（测试字面量、或 DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT
	// 写 all 的"全部吞掉"档）= 行为与今天完全一致。
	//
	// 语义（默认档 = 重试档，见 dnfbridge/equip_worn_slot_claim_occurrence.go）：
	//   1  ⇒ 登录批的那一条复述，继续按 noop 吞掉（09-28"每次重登被卸下"的修复面）；
	//   ≥2 ⇒ 玩家拖不动在重试，放行给厂商口径的反转端点搬运，执行成真·unequip。
	// 取值只被 equipmentWornSlotReassertClaim 与 equipmentLoginWearReassertNoop 读取。
	WornSlotClaimOccurrence int
	// MirrorReequipEcho 由 dnfbridge 填：本条 op19 是"客户端在**我们刚把那件脱下之后**
	// 立刻发来的镜像包"（把身份写在背包端点、装备端点为空的形状 c，且同一件物品、
	// 同一个装备槽、间隔在闸门窗口内）。厂商参考实现把这一族判 ClientEcho（拒绝、一字节不改），
	// 真因是客户端在复述它**脱下之前**的本地视图；不挡它就会出现
	// 脱下 → 0.6~0.8 s 后被穿回 的拉锯（现场 2026-09-29T10:35:31.967 unequip →
	// 10:35:32.733 equip）。判定与窗口见 dnfbridge/equip_mirror_requip_echo.go。
	MirrorReequipEcho bool
	// PlayerRequestFlag 由 dnfbridge 填：这条 op19 的尾字节 `body[26]`（`TrailingState0`）
	// == 1，即"客户端模块 8 在严格形状上标注的**玩家亲手发起**"。它只让
	// `equipmentWornSlotReassertClaim` 那一族门禁惰性（⇒ 反转端点兜底按厂商口径执行真·脱下），
	// **不影响**甲（镜像回穿）与任何其它判定。false = 与本轮之前逐字节相同。
	// 开关与语义见 dnfbridge/equip_player_request_flag.go。
	PlayerRequestFlag bool
}

// Placement is the typed input required to authorize an item entering an
// equipment slot. The validator owns PVF and character compatibility rules.
type Placement struct {
	CharacterID     string
	ItemID          int64
	SourceListType  byte
	SourceSlotIndex int16
	TargetSlotIndex int16
}

// PlacementValidator validates PVF item type, character restrictions, and the
// exact target worn slot. Owner refuses ordinary equipment writes without it.
type PlacementValidator interface {
	ValidateEquipmentPlacement(context.Context, Placement) error
}

// PlacementValidatorFunc adapts a function to PlacementValidator.
type PlacementValidatorFunc func(context.Context, Placement) error

func (f PlacementValidatorFunc) ValidateEquipmentPlacement(ctx context.Context, placement Placement) error {
	if f == nil {
		return ErrMoveValidatorRequired
	}
	return f(ctx, placement)
}

// MoveResult 描述穿戴/卸下写库结果；它不是旧客户端成功 ACK 许可。
type MoveResult struct {
	CharacterID          string
	SourceListType       byte
	SourceSlotIndex      int16
	DestinationListType  byte
	DestinationSlotIndex int16
	ItemID               int64
	SwappedItemID        int64
	Mode                 string
	Changed              bool
	InventorySlots       map[string]dnfrepo.ItemStack
	// NoopGate 只在 Mode=="noop" 时给出是哪一条闸门把这条 op19 判成"什么都不写"，
	// 用于现场日志区分（identity_echo = 厂商同源的复述闸门；
	// login_wear_reassert_noop = DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 开关下的
	// 登录引导窗口兜底）。
	NoopGate string
}

// NewOwner 创建穿戴装备 owner；缺少装备仓储时拒绝处理。
func NewOwner(repos dnfrepo.Group) (*Owner, error) {
	if repos.Equipment == nil {
		return nil, ErrOwnerUnavailable
	}
	return &Owner{
		equipment: repos.Equipment,
		inventory: repos.Inventory,
		character: repos.Character,
		accounts:  repos.Account,
		items:     repos.CharacterItems,
		assets:    repos.CharacterAssets,
		pets:      repos.Pet,
		petItems:  repos.CharacterPets,
	}, nil
}

// NewOwnerWithPlacementValidator enables ordinary equipment writes only when
// the caller supplies the typed PVF/character placement owner.
func NewOwnerWithPlacementValidator(repos dnfrepo.Group, validator PlacementValidator) (*Owner, error) {
	if validator == nil {
		return nil, ErrMoveValidatorRequired
	}
	owner, err := NewOwner(repos)
	if err != nil {
		return nil, err
	}
	owner.placementValidator = validator
	return owner, nil
}

// SetNameTagChecker registers a PVF-backed predicate that identifies name tag
// cards. When set, unequip of a name tag card is blocked (PR #239).
func (o *Owner) SetNameTagChecker(checker func(itemID uint32) bool) {
	if o != nil {
		o.nameTagChecker = checker
	}
}

// SetAvatarClassifier 注册"普通装扮 / 克隆装扮"的运行时 PVF 权威判定，只在一次
// op19 装备搬运事务内用于写/清穿戴行的克隆外观键。为零 = 不写任何东西（回退档）。
//
// 同族参考实现见 90原神开源0.1.8 `equip/owner.go:190-196`。
func (o *Owner) SetAvatarClassifier(classifier alignedcmd.AvatarClassifier) {
	if o != nil {
		o.avatarClassifier = classifier
	}
}

// Repair 按 86JP TryRepairSingleEquipped 修理穿戴槽：PVF 证据定价（repair
// price/grade/durability + pricetable 全局费率），金币与耐久在同一角色资产
// 事务内提交；body[5]=1 且魔王契约(586)生效时免费；body[7]=1 快速修理按
// [quick repair cost rate] 加价。slot=-1 转 RepairAll。
func (o *Owner) Repair(ctx context.Context, cmd RepairCommand, costResolver alignedcmd.RepairCostResolver) (RepairResult, error) {
	if o == nil || o.assets == nil {
		return RepairResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return RepairResult{}, ErrCharacterRequired
	}
	if cmd.SlotIndex == -1 {
		return o.RepairAll(ctx, cmd, costResolver)
	}
	if costResolver == nil {
		return RepairResult{}, ErrRepairCostMissing
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	var result RepairResult
	err := o.assets.WithinCharacterAssets(ctx, characterID, func(characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		var err error
		result, err = o.repairSingleEquipped(ctx, cmd, characterID, characters, equipment, costResolver)
		return err
	})
	return result, err
}

func (o *Owner) repairSingleEquipped(
	ctx context.Context,
	cmd RepairCommand,
	characterID string,
	characters dnfrepo.CharacterRepository,
	equipment dnfrepo.EquipmentRepository,
	costResolver alignedcmd.RepairCostResolver,
) (RepairResult, error) {
	record, ok, err := equipment.Load(ctx, characterID)
	if err != nil {
		return RepairResult{}, err
	}
	if !ok {
		return RepairResult{}, ErrEquipmentNotFound
	}
	record = dnfrepo.CloneEquipment(record)
	record.CharacterID = characterID
	if record.Entries == nil {
		record.Entries = make(map[string]dnfrepo.EquipmentEntry)
	}

	key := entryKey(cmd.SlotIndex)
	entry, ok := record.Entries[key]
	if !ok {
		return RepairResult{}, fmt.Errorf("%w: slot=%d", ErrSlotNotFound, cmd.SlotIndex)
	}
	if len(entry.RawEntry) < 12 {
		return RepairResult{}, fmt.Errorf("%w: slot=%d len=%d", ErrRawEntryTooShort, cmd.SlotIndex, len(entry.RawEntry))
	}
	// 耐久字按行格式取偏移：0x77 行在 +0x0B..0x0C，legacy 摘要行仍在 +10..11。
	// 旧的 `raw[10] | raw[11]<<8` 在 0x77 行上读到的是 ext/强化字节 + 耐久低字节
	// （+15 武器 = 0x0F | 0x30<<8 = 12303），恒 >= 上限 ⇒ 永远修不好。见
	// repair_durability_offset.go。
	current, ok := RepairDurabilityWord(entry.RawEntry, entry.ItemID)
	if !ok {
		return RepairResult{}, fmt.Errorf("%w: slot=%d len=%d", ErrRawEntryTooShort, cmd.SlotIndex, len(entry.RawEntry))
	}
	evidence, err := costResolver(entry.ItemID)
	if err != nil {
		return RepairResult{}, err
	}
	if evidence.MaxDurability <= 0 {
		return RepairResult{}, fmt.Errorf("%w: slot=%d item=%d", ErrRepairNotRepairable, cmd.SlotIndex, entry.ItemID)
	}
	maxDurability := evidence.MaxDurability
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return RepairResult{}, err
	}
	if !found {
		return RepairResult{}, fmt.Errorf("%w: character=%s", ErrWalletTxnRequired, characterID)
	}
	character = dnfrepo.CloneCharacter(character)
	if character.Stats == nil {
		return RepairResult{}, fmt.Errorf("%w: character=%s has no stats", ErrWalletTxnRequired, characterID)
	}
	gold := character.Stats["gold"]
	result := RepairResult{
		CharacterID:   characterID,
		SlotIndex:     cmd.SlotIndex,
		ItemID:        entry.ItemID,
		OldDurability: uint16(current),
		NewDurability: uint16(maxDurability),
	}
	if current >= maxDurability {
		result.NewDurability = uint16(current)
		result.UpdatedGold = gold
		return result, nil
	}
	cost := CalcRepairCost(evidence, current, upgradeLevelOfRawEntry(entry.RawEntry), cmd.QuickRepair)
	freeRepair := false
	statsChanged := false
	if cost != 0 && cmd.AutoRepair && o.premiumActive(ctx, cmd.AccountID, premium.DevilSlotType(premium.DevilSlotAutoRepair)) {
		// 魔王契约 auto repair: the client sent body[5]=1 and the account
		// holds an active slot-6 contract, so the repair is free (86JP
		// freeRepair = autoRepair && HasActiveAutoRepairForAccount).
		if premium.TryConsumeDaily(&character, premium.DevilSlotAutoRepair, time.Now().UTC()) {
			cost = 0
			freeRepair = true
			statsChanged = true
		}
	}
	if cost > gold {
		return RepairResult{}, fmt.Errorf("%w: cost=%d gold=%d", ErrRepairGoldInsufficient, cost, gold)
	}

	if cost > 0 {
		character.Stats["gold"] = gold - cost
		statsChanged = true
	}
	if statsChanged {
		character.UpdatedAt = time.Now()
		if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
			return RepairResult{}, err
		}
	}
	entry.RawEntry = append([]byte(nil), entry.RawEntry...)
	// 写回与读取同一个偏移（0x77 行 → +0x0B..0x0C）。旧写法把上限写进 +0x0A
	// （客户端渲染成强化等级）并把耐久字低字节清零，见 repair_durability_offset.go。
	if !SetRepairDurabilityWord(entry.RawEntry, entry.ItemID, maxDurability) {
		return RepairResult{}, fmt.Errorf("%w: slot=%d len=%d", ErrRawEntryTooShort, cmd.SlotIndex, len(entry.RawEntry))
	}
	if entry.Extra == nil {
		entry.Extra = make(map[string]string, 1)
	}
	entry.Extra["durability"] = strconv.FormatInt(maxDurability, 10)
	record.Entries[key] = entry
	record.UpdatedAt = time.Now()
	if err := dnfrepo.SaveEquipmentFields(ctx, equipment, record, dnfrepo.EquipmentFieldEntries); err != nil {
		return RepairResult{}, err
	}
	result.Cost = cost
	result.UpdatedGold = gold - cost
	result.Changed = true
	result.FreeRepair = freeRepair
	return result, nil
}

// RepairAll 按 86JP TryRepairAll 全部修理：扫描穿戴装备与主列表快捷栏
// (slot 3..8)，只处理 13 类可修理装备且耐久未满的条目，合计费用一次扣除，
// ACK 槽位 0xFFFF，客户端据此本地拉满。
func (o *Owner) RepairAll(ctx context.Context, cmd RepairCommand, costResolver alignedcmd.RepairCostResolver) (RepairResult, error) {
	if o == nil || o.assets == nil {
		return RepairResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return RepairResult{}, ErrCharacterRequired
	}
	if costResolver == nil {
		return RepairResult{}, ErrRepairCostMissing
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	var result RepairResult
	err := o.assets.WithinCharacterAssets(ctx, characterID, func(characters dnfrepo.CharacterRepository, inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		var err error
		result, err = o.repairAllInTransaction(ctx, cmd, characterID, characters, inventory, equipment, costResolver)
		return err
	})
	return result, err
}

type repairAllTarget struct {
	equipped bool
	key      string
	slot     int16
	itemID   int64
	maxDura  int64
	cost     int64
}

func (o *Owner) repairAllInTransaction(
	ctx context.Context,
	cmd RepairCommand,
	characterID string,
	characters dnfrepo.CharacterRepository,
	inventory dnfrepo.InventoryRepository,
	equipment dnfrepo.EquipmentRepository,
	costResolver alignedcmd.RepairCostResolver,
) (RepairResult, error) {
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return RepairResult{}, err
	}
	if !found {
		return RepairResult{}, fmt.Errorf("%w: character=%s", ErrWalletTxnRequired, characterID)
	}
	character = dnfrepo.CloneCharacter(character)
	if character.Stats == nil {
		return RepairResult{}, fmt.Errorf("%w: character=%s has no stats", ErrWalletTxnRequired, characterID)
	}
	equipRecord, equipFound, err := equipment.Load(ctx, characterID)
	if err != nil {
		return RepairResult{}, err
	}
	equipRecord = dnfrepo.CloneEquipment(equipRecord)
	equipRecord.CharacterID = characterID
	if equipRecord.Entries == nil {
		equipRecord.Entries = make(map[string]dnfrepo.EquipmentEntry)
	}
	invRecord, invFound, err := inventory.Load(ctx, characterID)
	if err != nil {
		return RepairResult{}, err
	}
	invRecord = dnfrepo.CloneInventory(invRecord)
	invRecord.CharacterID = characterID
	if invRecord.Slots == nil {
		invRecord.Slots = make(map[string]dnfrepo.ItemStack)
	}

	autoRepairActive := cmd.AutoRepair && o.premiumActive(ctx, cmd.AccountID, premium.DevilSlotType(premium.DevilSlotAutoRepair))
	freeRepair := false
	targets := make([]repairAllTarget, 0, 16)
	total := int64(0)
	if equipFound {
		for key, entry := range equipRecord.Entries {
			if len(entry.RawEntry) < 12 {
				continue
			}
			slot, err := strconv.Atoi(key)
			if err != nil {
				continue
			}
			evidence, err := costResolver(entry.ItemID)
			if err != nil {
				return RepairResult{}, err
			}
			if evidence.MaxDurability <= 0 || !RepairAllEligible(evidence.EquipmentType) {
				continue
			}
			current, ok := RepairDurabilityWord(entry.RawEntry, entry.ItemID)
			if !ok || current >= evidence.MaxDurability {
				continue
			}
			cost := CalcRepairCost(evidence, current, upgradeLevelOfRawEntry(entry.RawEntry), cmd.QuickRepair)
			targets = append(targets, repairAllTarget{equipped: true, key: key, slot: int16(slot), itemID: entry.ItemID, maxDura: evidence.MaxDurability, cost: cost})
			total += cost
		}
	}
	if invFound {
		for slot := int16(3); slot <= 8; slot++ {
			key := "0:" + strconv.Itoa(int(slot))
			stack, ok := invRecord.Slots[key]
			if !ok || stack.ItemID <= 0 {
				continue
			}
			evidence, err := costResolver(stack.ItemID)
			if err != nil {
				return RepairResult{}, err
			}
			if evidence.MaxDurability <= 0 || !RepairAllEligible(evidence.EquipmentType) {
				continue
			}
			current, err := repairBagDurabilityOf(stack)
			if err != nil || current >= evidence.MaxDurability {
				continue
			}
			upgradeLevel := 0
			if len(stack.RawEntry) > 0x0A {
				upgradeLevel = int(stack.RawEntry[0x0A] & 0x1F)
			}
			cost := CalcRepairCost(evidence, current, upgradeLevel, cmd.QuickRepair)
			targets = append(targets, repairAllTarget{key: key, slot: slot, itemID: stack.ItemID, maxDura: evidence.MaxDurability, cost: cost})
			total += cost
		}
	}

	gold := character.Stats["gold"]
	result := RepairResult{
		CharacterID: characterID,
		SlotIndex:   -1,
		FreeRepair:  freeRepair,
	}
	if len(targets) == 0 {
		result.UpdatedGold = gold
		return result, nil
	}
	if autoRepairActive && premium.TryConsumeDaily(&character, premium.DevilSlotAutoRepair, time.Now().UTC()) {
		freeRepair = true
		result.FreeRepair = true
		total = 0
		for index := range targets {
			targets[index].cost = 0
		}
	}
	if total > gold {
		return RepairResult{}, fmt.Errorf("%w: cost=%d gold=%d", ErrRepairGoldInsufficient, total, gold)
	}
	if total > 0 || freeRepair {
		character.Stats["gold"] = gold - total
		character.UpdatedAt = time.Now()
		if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
			return RepairResult{}, err
		}
	}
	for _, target := range targets {
		if target.equipped {
			entry := equipRecord.Entries[target.key]
			entry.RawEntry = append([]byte(nil), entry.RawEntry...)
			SetRepairDurabilityWord(entry.RawEntry, entry.ItemID, target.maxDura)
			if entry.Extra == nil {
				entry.Extra = make(map[string]string, 1)
			}
			entry.Extra["durability"] = strconv.FormatInt(target.maxDura, 10)
			equipRecord.Entries[target.key] = entry
			continue
		}
		stack := invRecord.Slots[target.key]
		stack = cloneItemStackForRepair(stack)
		if stack.Extra == nil {
			stack.Extra = make(map[string]string, 2)
		}
		stack.Extra["durability"] = strconv.FormatInt(target.maxDura, 10)
		if len(stack.RawEntry) >= 12 {
			stack.RawEntry = append([]byte(nil), stack.RawEntry...)
			SetRepairDurabilityWord(stack.RawEntry, stack.ItemID, target.maxDura)
		}
		invRecord.Slots[target.key] = stack
	}
	if len(targets) > 0 {
		equipRecord.UpdatedAt = time.Now()
		if equipFound {
			if err := dnfrepo.SaveEquipmentFields(ctx, equipment, equipRecord, dnfrepo.EquipmentFieldEntries); err != nil {
				return RepairResult{}, err
			}
		}
		invRecord.UpdatedAt = time.Now()
		if invFound {
			if err := dnfrepo.SaveInventoryFields(ctx, inventory, invRecord, dnfrepo.InventoryFieldSlots); err != nil {
				return RepairResult{}, err
			}
		}
	}
	result.Cost = total
	result.UpdatedGold = gold - total
	result.Changed = true
	result.RepairedCount = len(targets)
	return result, nil
}

func upgradeLevelOfRawEntry(raw []byte) int {
	if len(raw) > 0x0A {
		return int(raw[0x0A] & 0x1F)
	}
	return 0
}

// repairBagDurabilityOf 读背包堆叠的**当前耐久证据**：extra 的 `durability`
// （发放/掉落/礼包/邮件路径都写它；客户端投影同样是 extra 优先 ——
// dnfbridge/current_item_list_stack_projection.go:42 只在 extra 非零时才覆盖 +0x0B）。
// 修理的**写回**落在 raw 的耐久字上（SetRepairDurabilityWord，与客户端读的同一个偏移），
// 修完两侧同为上限 ⇒ 读侧来源与写侧偏移不会互相打架。旧写侧偏移（raw[10]/raw[11]）
// 在 0x77 行上会把上限写进 ext/强化字节、并把耐久字低字节清零。
func repairBagDurabilityOf(stack dnfrepo.ItemStack) (int64, error) {
	if stack.Extra == nil {
		return 0, ErrRepairCostMissing
	}
	raw := strings.TrimSpace(stack.Extra["durability"])
	if raw == "" {
		return 0, ErrRepairCostMissing
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: %q", ErrRepairCostInvalid, raw)
	}
	return value, nil
}

func cloneItemStackForRepair(stack dnfrepo.ItemStack) dnfrepo.ItemStack {
	if stack.Extra != nil {
		extra := make(map[string]string, len(stack.Extra)+1)
		for key, value := range stack.Extra {
			extra[key] = value
		}
		stack.Extra = extra
	}
	return stack
}

// premiumActive reports whether the account currently holds an active premium
// contract of the given type. A missing account repository or record reads as
// inactive, so only the explicit free-repair bonus is gated by it.
func (o *Owner) premiumActive(ctx context.Context, accountID string, premiumType int64) bool {
	if o == nil || o.accounts == nil || strings.TrimSpace(accountID) == "" {
		return false
	}
	account, ok, err := o.accounts.Load(ctx, accountID)
	if err != nil || !ok {
		return false
	}
	return premium.Active(account, premiumType, time.Now().UTC())
}

// Move 在装备槽和背包/装扮/宠物物品栏之间迁移 raw entry。
// Ordinary placement requires structural bounds plus a typed PVF/character validator.
func (o *Owner) Move(ctx context.Context, cmd MoveCommand) (MoveResult, error) {
	if o == nil || o.equipment == nil {
		return MoveResult{}, ErrOwnerUnavailable
	}
	if o.inventory == nil {
		return MoveResult{}, ErrInventoryUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return MoveResult{}, ErrCharacterRequired
	}
	kind, err := validateMoveStructure(cmd)
	if err != nil {
		return MoveResult{}, err
	}
	creatureMove := kind == moveKindPet
	petAggregateMove := creatureMove || kind == moveKindArtifact
	// Current EXE c3ea63ec's dual-weapon button (0276C260 -> 025EBBF0)
	// sends list3/12 -> list3/24 with count=0. Here zero denotes a worn-slot
	// exchange, not a stack quantity. Keep every other move's count=1 rule;
	// PVF placement and the character item transaction still authorize the swap.
	weaponButtonSwap := cmd.SourceListType == wireListEquipment && cmd.SourceSlotIndex == 12 &&
		cmd.DestinationListType == wireListEquipment && cmd.DestinationSlotIndex == 24 && cmd.MoveCount == 0
	if cmd.MoveCount != 1 && !weaponButtonSwap {
		return MoveResult{}, fmt.Errorf("%w: count=%d", ErrMoveStackCountInvalid, cmd.MoveCount)
	}
	if !creatureMove && o.placementValidator == nil {
		return MoveResult{}, ErrMoveValidatorRequired
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	if !o.inItemTransaction {
		var result MoveResult
		if petAggregateMove {
			if o.pets == nil {
				return MoveResult{}, ErrPetRepositoryRequired
			}
			if o.petItems == nil {
				return MoveResult{}, ErrPetMoveTxnRequired
			}
			err = o.petItems.WithinCharacterPets(ctx, characterID, func(inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository, pets dnfrepo.PetRepository) error {
				txOwner := &Owner{
					equipment:          equipment,
					inventory:          inventory,
					character:          o.character,
					pets:               pets,
					placementValidator: o.placementValidator,
					inItemTransaction:  true,
				}
				var err error
				result, err = txOwner.Move(ctx, cmd)
				return err
			})
		} else {
			if o.items == nil {
				return MoveResult{}, ErrMoveTransactionRequired
			}
			err = o.items.WithinCharacterItems(ctx, characterID, func(inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
				txOwner := &Owner{
					equipment:          equipment,
					inventory:          inventory,
					character:          o.character,
					placementValidator: o.placementValidator,
					avatarClassifier:   o.avatarClassifier,
					inItemTransaction:  true,
				}
				var err error
				result, err = txOwner.Move(ctx, cmd)
				return err
			})
		}
		return result, err
	}
	if !petAggregateMove {
		if _, err := o.normalizeLegacyPVFInitialEquipmentSlots(ctx, characterID); err != nil {
			return MoveResult{}, err
		}
	} else if creatureMove {
		if _, err := creaturestate.ReconcileInventory(ctx, characterID, o.inventory, o.equipment, o.pets); err != nil {
			return MoveResult{}, err
		}
	}

	// Current NoPack op19 carries two endpoint addresses, not a reliable move
	// direction. Equip and unequip requests keep the inventory/avatar/cargo
	// endpoint first and the worn endpoint second; only the endpoint instance
	// values and authoritative server state change. Resolve the direction from
	// the transaction-scoped DB snapshot so the decision and mutation are
	// serialized by CharacterItemUnitOfWork.
	resolution, err := o.resolveMoveEndpoints(ctx, characterID, cmd)
	if err != nil {
		return MoveResult{}, err
	}
	if resolution.echo {
		// 客户端复述穿戴态（见 equipmentEndpointIdentityEcho / equipmentLoginWearReassertNoop）：
		// 一个字节都不写。返回端点原样、count=0 的 noop 结果，bridge 会按既有 noop 口径回一条
		// moveValue=0 的有界 ACK，并且不发容器/外观刷新（Changed=false）。
		return MoveResult{
			CharacterID:          characterID,
			SourceListType:       cmd.SourceListType,
			SourceSlotIndex:      cmd.SourceSlotIndex,
			DestinationListType:  cmd.DestinationListType,
			DestinationSlotIndex: cmd.DestinationSlotIndex,
			ItemID:               resolution.echoItemID,
			Mode:                 "noop",
			NoopGate:             resolution.noopGate,
		}, nil
	}
	resolved, reversed := resolution.resolved, resolution.reversed
	var result MoveResult
	if resolved.SourceListType == wireListEquipment && resolved.DestinationListType == wireListEquipment {
		result, err = o.swapEquipSlots(ctx, resolved)
	} else if resolved.SourceListType == wireListEquipment {
		result, err = o.unequip(ctx, resolved)
	} else {
		result, err = o.equip(ctx, resolved)
	}
	if err != nil {
		return MoveResult{}, err
	}
	switch kind {
	case moveKindPet:
		if err := o.syncPetEquipmentMove(ctx, characterID, result); err != nil {
			return MoveResult{}, err
		}
	case moveKindArtifact:
		if err := o.syncPetArtifactEquipmentMove(ctx, characterID, result); err != nil {
			return MoveResult{}, err
		}
	}
	if reversed && cmd.SourceListType != cmd.DestinationListType {
		// Keep the request's A/B list direction, but return the authoritative
		// inventory slot selected by the owner. Cross-list op19 accepts a
		// redirected final slot; this is required when the client's stale bag
		// view nominates an already occupied unequip destination.
		result.SourceListType = cmd.SourceListType
		result.SourceSlotIndex = result.DestinationSlotIndex
		result.DestinationListType = cmd.DestinationListType
		result.DestinationSlotIndex = cmd.DestinationSlotIndex
	}
	if reversed && cmd.SourceListType == wireListEquipment && cmd.DestinationListType == wireListEquipment {
		result.SourceSlotIndex = cmd.SourceSlotIndex
		result.DestinationSlotIndex = cmd.DestinationSlotIndex
	}
	return result, nil
}

type legacyPVFInitialEquipmentSlotMove struct {
	sourceKey string
	targetKey string
	target    int16
	entry     dnfrepo.EquipmentEntry
}

// normalizeLegacyPVFInitialEquipmentSlots converts only untouched
// source=pvf_create_equipment_list records from the legacy worn-slot keys to
// the current EXE actor-slot keys used by op19. It runs inside the same
// CharacterItemUnitOfWork as the requested move, so a later placement/save
// failure rolls the migration back with the move. Runtime rows and already
// migrated PVF rows carry current_exe_runtime_move and are never reinterpreted.
func (o *Owner) normalizeLegacyPVFInitialEquipmentSlots(ctx context.Context, characterID string) (bool, error) {
	record, found, err := o.equipment.Load(ctx, characterID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrEquipmentNotFound
	}
	record = ensureEquipmentRecord(dnfrepo.CloneEquipment(record), characterID)

	plans := make(map[string]legacyPVFInitialEquipmentSlotMove)
	targets := make(map[string]string)
	for key, entry := range record.Entries {
		if !isUntouchedLegacyPVFInitialEquipment(entry) {
			continue
		}
		target, ok := currentActorSlotForLegacyPVFInitialSlot(entry.SlotIndex)
		if !ok {
			continue
		}
		sourceKey := entryKey(entry.SlotIndex)
		if key != sourceKey {
			return false, fmt.Errorf("%w: key=%q entry=%d", ErrMoveSlotMismatch, key, entry.SlotIndex)
		}
		targetKey := entryKey(target)
		if previous, exists := targets[targetKey]; exists {
			return false, fmt.Errorf(
				"%w: source=%s other_source=%s target=%s",
				ErrLegacyPVFSlotCollision,
				sourceKey,
				previous,
				targetKey,
			)
		}
		targets[targetKey] = sourceKey
		plans[sourceKey] = legacyPVFInitialEquipmentSlotMove{
			sourceKey: sourceKey,
			targetKey: targetKey,
			target:    target,
			entry:     entry,
		}
	}
	if len(plans) == 0 {
		return false, nil
	}

	// A target occupied by another planned legacy row is safe because every
	// source key is deleted before any target key is inserted. Any other
	// occupant is authoritative current/runtime state and must fail closed.
	for _, plan := range plans {
		if _, occupied := record.Entries[plan.targetKey]; !occupied {
			continue
		}
		if _, movingAway := plans[plan.targetKey]; !movingAway {
			return false, fmt.Errorf(
				"%w: source=%s target=%s",
				ErrLegacyPVFSlotCollision,
				plan.sourceKey,
				plan.targetKey,
			)
		}
	}

	for sourceKey := range plans {
		delete(record.Entries, sourceKey)
	}
	for _, plan := range plans {
		entry := plan.entry
		entry.SlotIndex = plan.target
		entry.Extra = cloneExtra(entry.Extra)
		entry.Extra["equipped_slot"] = strconv.Itoa(int(plan.target))
		entry.Extra["current_exe_equipment_type"] = strconv.Itoa(int(plan.target))
		entry.Extra["current_exe_runtime_move"] = "1"
		record.Entries[plan.targetKey] = entry
	}
	record.UpdatedAt = time.Now()
	if err := dnfrepo.SaveEquipmentFields(ctx, o.equipment, record, dnfrepo.EquipmentFieldEntries); err != nil {
		return false, err
	}
	return true, nil
}

func isUntouchedLegacyPVFInitialEquipment(entry dnfrepo.EquipmentEntry) bool {
	if !strings.EqualFold(strings.TrimSpace(entry.Extra["source"]), "pvf_create_equipment_list") {
		return false
	}
	raw := strings.TrimSpace(strings.ToLower(entry.Extra["current_exe_runtime_move"]))
	switch raw {
	case "", "0", "false", "no", "off":
		return true
	default:
		// Unknown non-empty markers also fail closed: they may represent a
		// newer runtime migration state that this owner must not reinterpret.
		return false
	}
}

func currentActorSlotForLegacyPVFInitialSlot(slot int16) (int16, bool) {
	switch {
	case slot == 11:
		return 12, true
	case slot >= 13 && slot <= 22:
		return slot + 1, true
	case slot == 23:
		return 25, true
	default:
		return 0, false
	}
}

// moveEndpointResolution 是 op19 两个端点的判定结果。
type moveEndpointResolution struct {
	resolved MoveCommand
	reversed bool
	// echo 为真表示这条 op19 只是在**复述穿戴态**（客户端把身份写在背包端点、
	// 而权威状态说那个端点为空、那件物品正穿在装备端点上）。这种包不能被
	// 当成"脱下"执行，任何权威状态都不许改写。
	echo       bool
	echoItemID int64
	// noopGate 见 MoveResult.NoopGate；echo 为真时说明命中的是哪一条闸门。
	noopGate string
}

// equipLoginWearReassertNoopEnabled 是 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 开关。
//
// **默认关闭**：关闭时本文件的行为与历史完全一致（厂商口径：SourceInstanceValue==0
// 的 op19 就是真移动，不许当复述）。只有显式设为 1/true/yes/on 时才启用下面这条
// 登录引导窗口兜底；设回 0 即回退。
func equipLoginWearReassertNoopEnabled() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP")))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// equipmentLoginWearReassertNoop 是**开关控制的兜底**判定：登录引导窗口内、
// 背包端点为空的"把装备槽那件物品挪进背包"形状的 op19，按 noop 处理。
//
// 与 equipmentEndpointIdentityEcho 的区别（两者判据互不重叠，后者的判据不许改）：
//   - 厂商同源闸门要求 SourceInstanceValue > 0（客户端把身份写在背包端点）；
//   - 本兜底只处理 SourceInstanceValue == 0 且**装备端点**写了实例的那一族，
//     也就是现场登录包里实际出现的形状：
//     hex 01130029000000…000012000000000001000000030c006dc1050600000000ffffffff0000
//     src=(list0 背包,18,IV=0) dst=(list3 装备,12,IV=101040493)
//
// 复核证据（2026-09-27）：
//   - 厂商真机（YKCN S4A21 客户端 CipherPacket.log:1439 + GameLog.log:2184）在
//     同一登录窗口发的 op19 是 src=(0,28,IV=26058) dst=(3,13,IV=0x17DC8D58)：
//     **源端实例非零**。厂商服务端因此能靠 SourceInstanceValue>0 认出复述。
//   - 我们的客户端在 13 次完成登录收尾的连接里对角色 6 发了 11 次该形状
//     （角色 1：0/61、角色 3：0/41、角色 4：1/67）⇒ 它不是普遍的客户端复述，
//     而是角色数据驱动的、客户端主动请求"把这件装备挪下来"。判据因此必须同时
//     要求：这条包只可能由"客户端指名装备槽上那件物品"构成。
//
// 窗口的开启/关闭依据、以及一次性语义由 dnfbridge 负责（本函数只信 cmd 上的
// LoginWearWindowOpen），因此本函数不会在窗口外改变任何行为。
func equipmentLoginWearReassertNoop(
	cmd MoveCommand,
	inventoryEmpty bool,
	equipped dnfrepo.EquipmentEntry,
	equipmentOccupied bool,
) bool {
	if !equipLoginWearReassertNoopEnabled() || !cmd.LoginWearWindowOpen {
		return false
	}
	// 登录纪元内的**重试**（同槽第 2 条起）不是复述：放行给厂商口径的反转端点搬运
	// （= 真脱下）。桥侧同一判据见 dnfbridge/equip_worn_slot_claim_occurrence.go。
	if cmd.WornSlotClaimOccurrence > 1 {
		return false
	}
	if !inventoryEmpty || !equipmentOccupied {
		return false
	}
	if cmd.SourceListType != wireListMain && cmd.SourceListType != wireListAvatar {
		return false
	}
	if cmd.DestinationListType != wireListEquipment {
		return false
	}
	if cmd.MoveCount != 1 {
		return false
	}
	if cmd.SourceInstanceValue != 0 {
		return false
	}
	if equipped.ItemID <= 0 || int64(cmd.DestinationInstanceValue) != equipped.ItemID {
		return false
	}
	return true
}

// equipmentLoginWearUndeclaredReversalGate 是"登录复述上下文内，反转端点会卸下一件
// 客户端没点名的装备"这条闸门的具名值（写进 MoveResult.NoopGate）。
//
// 它**不是** login_wear_reassert_noop：那条要求客户端点名的正是穿着的那件（身份一致）；
// 本条处理身份**不一致**的那一半。两条都不许互相顶替 —— 现场的 `noopGate=` 计数是
// 操作者判读档位是否生效的第一手依据。
const equipmentLoginWearUndeclaredReversalGate = "login_wear_undeclared_reversal"

// equipmentMoveReversalIdentityGuardEnabled 是
// DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD 开关（运行时读 env，不缓存）。
//
// **默认开启（修复档）**：只有显式写 off/0/false/no/legacy/disable/disabled/none
// 才回退到今天。
func equipmentMoveReversalIdentityGuardEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD"))) {
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// equipmentLoginWearUndeclaredWearReversal 判定"在**登录复述上下文**里，下面那条反转
// 端点兜底会把一件客户端**没有点名**的装备卸下来"。
//
// 反转分支的判据只有占用：srcIV==0 && dstIV!=0 && B 被占用 ⇒ 反转成
// src=(3,slot,IV=…) ⇒ o.unequip。它**不看** dstIV 到底指谁。于是在登录复述上下文里，
// 当客户端在装备端点上写的是**别**的 item id（它那一格视图过期了）时，服务端照样把当前
// 真正穿着的那件卸下来，而客户端从未点名它 —— 这是"只按占用做推断"的另一面，与
// equipmentEndpointIdentityEcho 同一族判据（都只信客户端自己声明的身份）。
//
// **范围只到"登录复述上下文"**（cmd.LoginWearWindowOpen，由桥侧
// equip_login_wear_reassert_anchor.go 的锚点/每槽收口决定），因此普通游戏时段的行为
// 一个字节都不变。范围为什么必须这么窄（这条是量出来的，不是猜的）：
//
//	把同一条判据放到**所有**时段（去掉 LoginWearWindowOpen 要求）会让
//	owner_test.go 的 TestOwnerMoveUnequipUsesNextEmptyMainSlotFromStaleClientDestination
//	三条子用例全部失败（Mode 从 unequip 变成 noop）—— 那条用例钉的是一个**故意保留**
//	的特性：客户端选中的背包落点过期（被占）时，把那件装备卸下来并改落到下一个真空槽。
//	按"宁可少拦不可多拦"的口径，本闸门只作用于登录复述上下文。
//
// 边界（必须知情，不许当成"复述闸门"用）：
//   - 客户端点名的那件正是穿着的那件（现场 2026-09-28 全部 65 次 unequip 都是这个形状）
//     ⇒ 本条闸门**惰性**，走 login_wear_reassert_noop 那一条；
//   - 本条**不能**区分"登录复述"与"玩家真实脱下"：两者包体逐字节相同（同一会话
//     game-001200 的 61423/61575 两条逐字节同体包，一条 noop、一条 unequip）。
//     复述那一族的覆盖仍然只由 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP + 桥侧锚点/每槽收口决定。
//   - 命中本条时下游**不补**权威刷新（inventory/handler.go:1107 只认 login_wear_reassert_noop），
//     客户端靠下一次开背包（现场每次 op19 之前都有一条 type=217）自己对齐。
func equipmentLoginWearUndeclaredWearReversal(cmd MoveCommand, equipped dnfrepo.EquipmentEntry, destinationOccupied bool) bool {
	if !equipmentMoveReversalIdentityGuardEnabled() {
		return false
	}
	if !equipLoginWearReassertNoopEnabled() || !cmd.LoginWearWindowOpen {
		return false
	}
	if cmd.SourceInstanceValue != 0 || cmd.DestinationInstanceValue == 0 || !destinationOccupied {
		return false
	}
	if equipped.ItemID <= 0 {
		return false
	}
	return int64(cmd.DestinationInstanceValue) != equipped.ItemID
}

// equipmentEndpointIdentityEcho 判定"客户端复述穿戴态"的 op19。
//
// 现场（HK 线上 2026-09-25 19:26，角色 6 test78 / 圣耀救赎光剑 101040493，
// `packet_log.20260925-192600.txt` 会话 game-000013 pkt_seq=110 与 game-000016
// pkt_seq=109，两次选角字节完全相同）：
//
//	hex 01130029000000a6277cbe19000010000000000001000000030c006dc1050600000000ffffffff0000
//	src=(list0 主背包, slot16, instance=0) MoveCount=1 dst=(list3 装备, slot12, instance=101040493)
//
// 权威状态：主背包槽 16 为空、装备槽 12 = 101040493。客户端写在**装备端点**上的实例
// 正是槽 12 实际穿着的那件物品的 item id —— 它在指名"槽 12 上是这件"，而不是在请求
// 把某件背包物品穿上去。旧代码只看占用：A 空 + B 占用 ⇒ 反转端点 ⇒ 判成 `unequip`
// （现场 `equipMove=unequip requestA=(0,16) requestB=(3,12) item=101040493`），
// 登录瞬间就把穿着的武器卸进背包。
//
// 参考实现（ServerS4A21 `Server/DfoServer/Game/Inventory/Services/InventoryMoveService.cs:899`
// `IsEquipmentClientEchoWhenSourceMissing` → `:109 Fail(ClientEcho)`，由
// `Network/Handlers/InventoryHandler.Move.cs:96-101` 回 `0x13` 错误 ACK）对同一族包
// 明确**拒绝且不落库**：源端点不存在、客户端却用实例复述装备端点上的那件物品时，
// 那不是移动指令。我们此前没有这条闸门。
//
// 判据只用客户端自己给出的**身份**证据（与上一轮"耐久不是身份"同一口径）：
//   - 两个端点分属不同容器，且装备端点是 list3/list17；
//   - 背包端点的实例 == 装备端点上那件物品的 item id（客户端指名它）；
//   - 装备端点的实例 == 0（客户端没有声明该槽被别的物品占着）；
//   - 权威状态确认：背包端点为空、装备端点确实穿着那件物品。
//
// 真正的"脱下"包不受影响：客户端把身份写在**装备**端点、背包端点的实例为 0
// （现场 03:29 `pkt_seq=1185` 与 04:32 `pkt_seq=336` 的操作者真实脱下都是这个形状）。
func equipmentEndpointIdentityEcho(
	cmd MoveCommand,
	inventoryEmpty bool,
	equipped dnfrepo.EquipmentEntry,
	equipmentOccupied bool,
) bool {
	if !inventoryEmpty || !equipmentOccupied {
		return false
	}
	if cmd.SourceListType == wireListEquipment || cmd.DestinationListType != wireListEquipment {
		return false
	}
	if cmd.SourceInstanceValue <= 0 || cmd.DestinationInstanceValue != 0 {
		return false
	}
	if equipped.ItemID <= 0 || int64(cmd.SourceInstanceValue) != equipped.ItemID {
		return false
	}
	return true
}

// equipmentDuplicateEndpointIdentityGate 是"两个端点写了**同一个**实例、而背包端点在权威
// 状态里为空"这一类 op19 的具名闸门（写进 MoveResult.NoopGate）。
//
// 它与同族另外两条**互不重叠**，现场按 noopGate= 计数时必须分开读：
//   - login_wear_reassert_noop 要求 SourceInstanceValue == 0；
//   - identity_echo 要求 DestinationInstanceValue == 0；
//   - 本条只处理两端**都**非零且**相等**的第三种形状，见 equipmentDuplicateEndpointIdentity。
const equipmentDuplicateEndpointIdentityGate = "duplicate_endpoint_identity"

// equipmentMoveDuplicateIdentityGuardEnabled 是
// DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD 开关（运行时读 env，不缓存、不新增
// options.go 字段 —— 与 equipmentMoveReversalIdentityGuardEnabled 同一读法，理由同：
// 现场改档不需要重新编译，回退必须显式写出来）。
//
// **默认开启（修复档）**：只有显式写 off/0/false/no/legacy/disable/disabled/none 才是
// 回退档（= 今天：反转端点兜底把这条包变成一次真落库的卸下）。
func equipmentMoveDuplicateIdentityGuardEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD"))) {
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// equipmentDuplicateEndpointIdentity 判定 2026-09-28 17:58:24.624 CST 那条现场 op19 的
// 形状：**两端写着同一个实例，而背包端点在权威状态里是空的**。
//
//	hex 01130029000000baaed1b90c01 | 00 0b00 6dc10506 01000000 03 0c00 6dc10506 00000000 ffffffff 0000
//	src=(list0 主背包, slot 11, IV=101040493) dst=(list3 装备, slot 12, IV=101040493)
//	权威状态：背包 0:11 **为空**、装备槽 12 穿着 101040493（= dstIV）。
//
// 为什么这条包不能驱动任何搬运（判据只用客户端自己声明的身份 + 权威占用，不看时间、
// 不看"是不是复述"）：
//   - 客户端在**两端**声明了**同一个**实例。一件物品不可能同时位于两个容器里，
//     所以这条包自述的状态在任何权威状态下都不可能成立 —— 它不描述一次真实搬运；
//   - 它唯一可核对的内容是"装备槽 12 上是 101040493"，而权威状态**已经**是那样
//     （equipped.ItemID == dstIV）⇒ 请求要的结果已经达成 ⇒ noop，一个字节都不必写；
//   - 背包端点的声明（0:11 上有 101040493）被权威占用**证伪**（inventoryEmpty）。
//     反转端点兜底（resolveMoveEndpoints 末尾那一支）只按"B 被占用"做推断，
//     于是把这条自相矛盾的包变成一次**真卸下**：客户端想穿上它，服务端却把它脱下来。
//
// 现场（`_dist/spontaneous-equip-1758-investigation.md` §2.2/§3.3 + 线上
// packet_log.txt:75197/75199/75201）：这条包真的卸下了操作者的武器 101040493
// （equipMove=unequip changed=true，槽 12 清空、0:11 落袋、mode0 里 `0c ffffffff`），
// 而 ④ 的三个收口（登录窗口 / 锚点+每槽 / 身份矛盾闸门）全部要求 srcIV==0，
// 对本形状**一个都不会被调用**。
//
// 范围为什么只到"背包端点权威为空"这一半（**故意**，有现场依据）：
//   - 背包端点**被占**的同名形状（packet_log:71705，`src=(0,19,IV=101010856)
//     dst=(3,12,IV=101010856)`）在契约上根本走不到反转分支 —— resolveMoveEndpoints 的
//     `if aOccupied { return resolved=cmd }` 先命中，现场判定是 equip_swap；
//   - 而且那一半可能是**同 item id 的两件**（两个副本）之间的合法换装，我们没有现场
//     证据可以推翻它。所以本闸门不碰它（回归钉子见 equipment_duplicate_identity_guard_test.go）。
func equipmentDuplicateEndpointIdentity(
	cmd MoveCommand,
	inventoryEmpty bool,
	equipped dnfrepo.EquipmentEntry,
	equipmentOccupied bool,
) bool {
	if !equipmentMoveDuplicateIdentityGuardEnabled() {
		return false
	}
	if !inventoryEmpty || !equipmentOccupied {
		return false
	}
	if cmd.SourceListType != wireListMain && cmd.SourceListType != wireListAvatar {
		return false
	}
	if cmd.DestinationListType != wireListEquipment {
		return false
	}
	if cmd.MoveCount != 1 {
		return false
	}
	if cmd.SourceInstanceValue == 0 || cmd.DestinationInstanceValue == 0 {
		return false
	}
	if cmd.SourceInstanceValue != cmd.DestinationInstanceValue {
		return false
	}
	if equipped.ItemID <= 0 || int64(cmd.DestinationInstanceValue) != equipped.ItemID {
		return false
	}
	return true
}

func (o *Owner) resolveMoveEndpoints(ctx context.Context, characterID string, cmd MoveCommand) (moveEndpointResolution, error) {
	var inventory dnfrepo.InventoryRecord
	if cmd.SourceListType != wireListEquipment || cmd.DestinationListType != wireListEquipment {
		loaded, ok, err := o.inventory.Load(ctx, characterID)
		if err != nil {
			return moveEndpointResolution{}, err
		}
		if !ok {
			return moveEndpointResolution{}, ErrInventoryNotFound
		}
		inventory = ensureInventoryRecord(dnfrepo.CloneInventory(loaded), characterID)
	}
	equipment, ok, err := o.equipment.Load(ctx, characterID)
	if err != nil {
		return moveEndpointResolution{}, err
	}
	if !ok {
		return moveEndpointResolution{}, ErrEquipmentNotFound
	}
	equipment = ensureEquipmentRecord(dnfrepo.CloneEquipment(equipment), characterID)
	aOccupied, err := moveEndpointOccupied(&inventory, &equipment, cmd.SourceListType, cmd.SourceSlotIndex)
	if err != nil {
		return moveEndpointResolution{}, err
	}
	bOccupied, err := moveEndpointOccupied(&inventory, &equipment, cmd.DestinationListType, cmd.DestinationSlotIndex)
	if err != nil {
		return moveEndpointResolution{}, err
	}
	// 方向判定优先于"两端都空 ⇒ 报错"：客户端那条"登录复述穿戴态"的 op19 可能两个
	// 端点都是空的（它点名的背包格本来就是空的），那仍然是一条**声明**，必须回一条
	// moveValue=0 的 noop，而不是把客户端的声明变成一次硬失败。见下面的
	// equipmentWornSlotReassert 调用点与 worn_slot_reassert_gate.go。
	//
	// 身份闸门优先于占用推断：客户端在背包端点上指名的那件物品正穿在装备端点上时，
	// 这条包是复述穿戴态，不是脱下指令（见 equipmentEndpointIdentityEcho）。
	if cmd.DestinationListType == wireListEquipment {
		equipped, found := equipment.Entries[entryKey(cmd.DestinationSlotIndex)]
		// 镜像回穿闸门（甲，默认 on；见 dnfbridge/equip_mirror_requip_echo.go）：
		// 优先级最高 —— 它是**我们自己的落库记录**推出来的（刚把那件脱下），
		// 与客户端声明的身份无关，必须在同族四条之前拦掉。
		if cmd.MirrorReequipEcho {
			return moveEndpointResolution{echo: true, echoItemID: int64(cmd.SourceInstanceValue), noopGate: equipmentMirrorReequipEchoGate}, nil
		}
		if found {
			if equipmentEndpointIdentityEcho(cmd, !aOccupied, equipped, bOccupied) {
				return moveEndpointResolution{echo: true, echoItemID: equipped.ItemID, noopGate: "identity_echo"}, nil
			}
			// 兜底（默认关闭）：登录引导窗口内、客户端把身份写在**装备**端点、
			// 背包端点实例为 0 的那一族，见 equipmentLoginWearReassertNoop。
			if equipmentLoginWearReassertNoop(cmd, !aOccupied, equipped, bOccupied) {
				return moveEndpointResolution{echo: true, echoItemID: equipped.ItemID, noopGate: "login_wear_reassert_noop"}, nil
			}
			// 身份矛盾闸门（默认 on；DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD=off
			// 一键回退到今天）：登录复述上下文里，下面的反转端点兜底会把这一格
			// **客户端没有点名**的装备卸下来。见 equipmentLoginWearUndeclaredWearReversal。
			if equipmentLoginWearUndeclaredWearReversal(cmd, equipped, bOccupied) {
				return moveEndpointResolution{echo: true, echoItemID: equipped.ItemID, noopGate: equipmentLoginWearUndeclaredReversalGate}, nil
			}
			// 双端点同名闸门（默认 on；DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD=off
			// 一键回退到今天）：客户端把**同一个实例**写在两端、背包端点权威为空 ⇒
			// 这条包不描述任何真实搬运，而下面末尾那支反转兜底会把它变成一次真卸下。
			// 见 equipmentDuplicateEndpointIdentity（现场 packet_log.txt:75197）。
			if equipmentDuplicateEndpointIdentity(cmd, !aOccupied, equipped, bOccupied) {
				return moveEndpointResolution{echo: true, echoItemID: equipped.ItemID, noopGate: equipmentDuplicateEndpointIdentityGate}, nil
			}
		}
		// 方向判定本身（默认 on；DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT=off
		// 一键回退到今天）：客户端把身份写在**装备**端点、源端实例为 0 时，这条 op19
		// 声明的是"那一格现在穿着什么"，不是搬运请求 —— **两个方向都必须判 noop**：
		//   - 那一格穿着点名那件 ⇒ 声明已经成立（worn_slot_reassert）；
		//   - 那一格**空着** ⇒ 声明与权威状态矛盾（玩家刚脱下来）⇒ 也不许用这条陈述
		//     把背包里那件穿回去（worn_slot_stale_claim）。
		// 放在同族四条之后：窗口打开时仍由 login_wear_reassert_noop 具名（现场判读口径
		// 不变），本条只在窗口/开关都不再吸收它时接管。见 worn_slot_reassert_gate.go
		// （现场包 :93098/:91884/:76912/:93127）。
		if noop, gate := equipmentWornSlotReassert(cmd, equipped, found); noop {
			return moveEndpointResolution{echo: true, echoItemID: int64(cmd.DestinationInstanceValue), noopGate: gate}, nil
		}
	}
	if !aOccupied && !bOccupied {
		return moveEndpointResolution{}, fmt.Errorf(
			"%w: endpoints=(%d,%d),(%d,%d)",
			ErrMoveSlotNotFound,
			cmd.SourceListType,
			cmd.SourceSlotIndex,
			cmd.DestinationListType,
			cmd.DestinationSlotIndex,
		)
	}
	// The current 28-byte op19 body carries the live instance value for both
	// endpoints. A zero A instance and nonzero B instance is the observed
	// unequip form. Prefer that direction even when the server still has an
	// item in A because a missed/early list0 snapshot can leave the client's
	// chosen empty destination stale. The owner will relocate to the next real
	// empty slot in the same proven inventory page instead of corrupting the
	// worn slot with A.
	if cmd.SourceInstanceValue == 0 && cmd.DestinationInstanceValue != 0 && bOccupied {
		return moveEndpointResolution{resolved: reverseMoveEndpoints(cmd), reversed: true}, nil
	}
	if cmd.SourceInstanceValue != 0 && cmd.DestinationInstanceValue == 0 && aOccupied {
		return moveEndpointResolution{resolved: cmd}, nil
	}
	if aOccupied {
		return moveEndpointResolution{resolved: cmd}, nil
	}
	return moveEndpointResolution{resolved: reverseMoveEndpoints(cmd), reversed: true}, nil
}

func moveEndpointOccupied(inventory *dnfrepo.InventoryRecord, equipment *dnfrepo.EquipmentRecord, listType byte, slotIndex int16) (bool, error) {
	if listType == wireListEquipment {
		entry, occupied := equipment.Entries[entryKey(slotIndex)]
		if occupied && entry.SlotIndex != slotIndex {
			return false, fmt.Errorf("%w: key=%d entry=%d", ErrMoveSlotMismatch, slotIndex, entry.SlotIndex)
		}
		return occupied, nil
	}
	items, _, ok := inventoryMap(inventory, listType)
	if !ok {
		return false, fmt.Errorf("%w: endpoint list=%d", ErrMoveUnsupported, listType)
	}
	_, occupied := items[inventoryKey(listType, slotIndex)]
	return occupied, nil
}

func reverseMoveEndpoints(cmd MoveCommand) MoveCommand {
	return MoveCommand{
		SelectedCharacterID:      cmd.SelectedCharacterID,
		SourceListType:           cmd.DestinationListType,
		SourceSlotIndex:          cmd.DestinationSlotIndex,
		SourceInstanceValue:      cmd.DestinationInstanceValue,
		MoveCount:                cmd.MoveCount,
		DestinationListType:      cmd.SourceListType,
		DestinationSlotIndex:     cmd.SourceSlotIndex,
		DestinationInstanceValue: cmd.SourceInstanceValue,
	}
}

func (o *Owner) equip(ctx context.Context, cmd MoveCommand) (MoveResult, error) {
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	inventory, equipment, err := o.loadMoveRecords(ctx, characterID)
	if err != nil {
		return MoveResult{}, err
	}
	srcItems, srcField, ok := inventoryMap(&inventory, cmd.SourceListType)
	if !ok {
		return MoveResult{}, fmt.Errorf("%w: source list=%d", ErrMoveUnsupported, cmd.SourceListType)
	}
	if cmd.SourceListType == wireListGuildMedal && !guildMedalInventorySlotContains(cmd.SourceSlotIndex) {
		return MoveResult{}, fmt.Errorf("%w: guild-medal source slot=%d", ErrMoveUnsupported, cmd.SourceSlotIndex)
	}
	srcKey := inventoryKey(cmd.SourceListType, cmd.SourceSlotIndex)
	source, ok := srcItems[srcKey]
	if !ok {
		return MoveResult{}, fmt.Errorf("%w: source list=%d slot=%d", ErrMoveSlotNotFound, cmd.SourceListType, cmd.SourceSlotIndex)
	}
	if !isEquipmentMoveUnit(source, cmd.SourceListType) {
		return MoveResult{}, fmt.Errorf("%w: count=%d", ErrMoveStackCountInvalid, source.Count)
	}
	if cmd.SourceListType == wireListPet && cmd.DestinationSlotIndex == petCreatureWornSlot {
		// 宠物本体的附魔（宝珠）真源是 PetEntry；背包行没有这些键时从 PetEntry 回填，
		// 否则 petCreatureEntryFromStack 会把穿戴态 raw[0x10:0x14]/[0x14] 与 Extra 写成 0，
		// 玩家看到的就是"穿上之后宝珠消失"。
		source = o.inheritPetEnchantFields(ctx, characterID, source)
	}
	entry, err := entryFromStack(source, cmd.SourceListType, cmd.DestinationSlotIndex, cmd.SourceInstanceValue)
	if err != nil {
		return MoveResult{}, err
	}
	entry = removeSealOnFirstEquipmentUse(entry, source)
	if err := o.validatePlacement(ctx, characterID, source.ItemID, cmd.SourceListType, cmd.SourceSlotIndex, cmd.DestinationSlotIndex); err != nil {
		return MoveResult{}, err
	}

	result := MoveResult{
		CharacterID:          characterID,
		SourceListType:       cmd.SourceListType,
		SourceSlotIndex:      cmd.SourceSlotIndex,
		DestinationListType:  cmd.DestinationListType,
		DestinationSlotIndex: cmd.DestinationSlotIndex,
		ItemID:               source.ItemID,
		Mode:                 "equip",
	}
	dstKey := entryKey(cmd.DestinationSlotIndex)
	previous, occupied := equipment.Entries[dstKey]
	// 「稀有克隆装扮」穿上时要记下"被它顶掉的那件普通装扮"作为自己要显示的外观；
	// 顶掉的是克隆件则继承它记着的那个 id（厂商 InventoryMoveService.cs:950-1005
	// SyncAvatarClearAvatarId，同族 0.1.8 equip/owner.go:1059-1082）。分类器为零
	// （回退档）时本函数不改 entry 一个字节。
	entry = o.withCloneAvatarAppearance(entry, previous, occupied)
	if occupied {
		if previous.SlotIndex != cmd.DestinationSlotIndex {
			return MoveResult{}, fmt.Errorf("%w: key=%d entry=%d", ErrMoveSlotMismatch, cmd.DestinationSlotIndex, previous.SlotIndex)
		}
		// 回背包的行不能带着克隆外观键：那件道具可能本身就是克隆件，键只在穿戴行有意义。
		srcItems[srcKey] = stackFromEntry(clearCloneAvatarAppearance(previous))
		result.SwappedItemID = previous.ItemID
		result.Mode = "equip_swap"
	} else {
		delete(srcItems, srcKey)
	}
	equipment.Entries[dstKey] = entry
	// 本次 op19 真正改动的只有两格：背包的 srcKey（换成被顶下来的那件，或被删除）
	// 与穿戴表的 dstKey。这两个 key 是 O-B 增量写的全部依据（见
	// owner_move_save_scope.go）；给不出来就整容器写并记一条回退事件。
	return o.saveMove(
		ctx, inventory, equipment, []dnfrepo.InventoryField{srcField},
		moveWriteKeys{inventory: []string{srcKey}, equipment: []string{dstKey}},
		result,
	)
}

func removeSealOnFirstEquipmentUse(entry dnfrepo.EquipmentEntry, source dnfrepo.ItemStack) dnfrepo.EquipmentEntry {
	// The current 0x77 item row's byte +0x0D is the visible sealed-state
	// byte. Runtime-PVF [attach type] [sealing] grants persist it as the
	// explicit seal_flag extra. The first successful equip consumes that
	// state permanently; otherwise unequipping copies the old flag back and
	// the client asks to remove the seal every time.
	if firstExtraInt(source.Extra, 0, "seal_flag", "seal") == 0 {
		return entry
	}
	if len(entry.RawEntry) > 0x0D {
		entry.RawEntry[0x0D] = 0
	}
	if entry.Extra == nil {
		entry.Extra = make(map[string]string, 4)
	}
	for _, key := range []string{"seal_flag", "seal", "equipment_update_seal_flag", "update_seal_flag"} {
		delete(entry.Extra, key)
	}
	entry.Extra["seal_removed_by_first_equip"] = "1"
	entry.Extra["trade_locked_by_first_equip"] = "1"
	return entry
}

func (o *Owner) unequip(ctx context.Context, cmd MoveCommand) (MoveResult, error) {
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	inventory, equipment, err := o.loadMoveRecords(ctx, characterID)
	if err != nil {
		return MoveResult{}, err
	}
	dstItems, dstField, ok := inventoryMap(&inventory, cmd.DestinationListType)
	if !ok {
		return MoveResult{}, fmt.Errorf("%w: destination list=%d", ErrMoveUnsupported, cmd.DestinationListType)
	}
	if cmd.DestinationListType == wireListGuildMedal && !guildMedalInventorySlotContains(cmd.DestinationSlotIndex) {
		return MoveResult{}, fmt.Errorf("%w: guild-medal destination slot=%d", ErrMoveUnsupported, cmd.DestinationSlotIndex)
	}
	srcKey := entryKey(cmd.SourceSlotIndex)
	source, ok := equipment.Entries[srcKey]
	if !ok {
		return MoveResult{}, fmt.Errorf("%w: equip slot=%d", ErrMoveSlotNotFound, cmd.SourceSlotIndex)
	}
	if source.SlotIndex != cmd.SourceSlotIndex {
		return MoveResult{}, fmt.Errorf("%w: key=%d entry=%d", ErrMoveSlotMismatch, cmd.SourceSlotIndex, source.SlotIndex)
	}
	// PR #239: name tag cards can only be replaced by purchase, not manually unequipped.
	if o.nameTagChecker != nil && source.ItemID > 0 && o.nameTagChecker(uint32(source.ItemID)) {
		return MoveResult{}, fmt.Errorf("%w: name tag card cannot be unequipped", ErrMoveUnsupported)
	}
	if len(source.RawEntry) == 0 {
		return MoveResult{}, ErrMoveRawEntryMissing
	}

	result := MoveResult{
		CharacterID:          characterID,
		SourceListType:       cmd.SourceListType,
		SourceSlotIndex:      cmd.SourceSlotIndex,
		DestinationListType:  cmd.DestinationListType,
		DestinationSlotIndex: cmd.DestinationSlotIndex,
		ItemID:               source.ItemID,
		Mode:                 "unequip",
	}
	dstKey := inventoryKey(cmd.DestinationListType, cmd.DestinationSlotIndex)
	if _, occupied := dstItems[dstKey]; occupied && cmd.DestinationInstanceValue == 0 {
		actualSlot, found := nextEmptyUnequipInventorySlot(dstItems, cmd.DestinationListType, cmd.DestinationSlotIndex)
		if !found {
			return MoveResult{}, fmt.Errorf("%w: no empty unequip destination after list=%d slot=%d", ErrMoveSlotNotFound, cmd.DestinationListType, cmd.DestinationSlotIndex)
		}
		cmd.DestinationSlotIndex = actualSlot
		result.DestinationSlotIndex = actualSlot
		dstKey = inventoryKey(cmd.DestinationListType, actualSlot)
	}
	if previous, occupied := dstItems[dstKey]; occupied {
		entry, err := entryFromStack(previous, cmd.DestinationListType, cmd.SourceSlotIndex, cmd.DestinationInstanceValue)
		if err != nil {
			return MoveResult{}, err
		}
		if err := o.validatePlacement(ctx, characterID, previous.ItemID, cmd.DestinationListType, cmd.DestinationSlotIndex, cmd.SourceSlotIndex); err != nil {
			return MoveResult{}, err
		}
		// 脱下克隆件时背包里那件顶上穿戴槽：它就是新的被穿戴装扮，克隆外观键
		// 按同一条规则重算（普通装扮顶上来 = 键清空）。见 withCloneAvatarAppearance。
		entry = o.withCloneAvatarAppearance(entry, source, true)
		equipment.Entries[srcKey] = entry
		result.SwappedItemID = previous.ItemID
		result.Mode = "unequip_swap"
	} else {
		delete(equipment.Entries, srcKey)
	}
	dstItems[dstKey] = stackFromEntry(clearCloneAvatarAppearance(source))
	// dstKey 已经是**改后**的落点（:1294 的 nextEmptyUnequipInventorySlot 会改落点），
	// 这是 O-B 最容易写错的一格；srcKey 是腾空/被顶替的那个穿戴槽。
	return o.saveMove(
		ctx, inventory, equipment, []dnfrepo.InventoryField{dstField},
		moveWriteKeys{inventory: []string{dstKey}, equipment: []string{srcKey}},
		result,
	)
}

// withCloneAvatarAppearance 计算穿戴行的克隆外观键。entry 是即将穿上的那件，
// previous 是被它顶掉的那件，occupied 表示那个穿戴槽原本有东西。
//
//   - 穿上的是克隆件、且被顶掉的是**非克隆**装扮 ⇒ 记下被顶掉那件的 ItemID
//     （厂商 InventoryMoveService.cs:977-991）。
//   - 穿上的是克隆件、被顶掉的是克隆件 ⇒ 继承后者的键（连换两件克隆件时外观
//     跟着走，同族 0.1.8 equip/owner.go:1073-1076）。
//   - 其它任何情况（普通装扮、换到空槽、被顶掉的是非装扮）⇒ 清空该键。
//
// 所有路径都先无条件 delete，所以残留在背包行/旧记录里的键不会漏进穿戴行。
// classifier 为零时本函数只做"删一个不存在的键"，entry 的可见字节与改动前一致。
func (o *Owner) withCloneAvatarAppearance(
	entry, previous dnfrepo.EquipmentEntry,
	occupied bool,
) dnfrepo.EquipmentEntry {
	entry.Extra = cloneExtra(entry.Extra)
	delete(entry.Extra, dnfrepo.CloneAvatarAppearanceExtraKey)
	if o == nil || o.avatarClassifier == nil || entry.ItemID <= 0 || entry.ItemID > int64(^uint32(0)) {
		return entry
	}
	_, isClone := o.avatarClassifier(uint32(entry.ItemID))
	if !isClone || !occupied || previous.ItemID <= 0 || previous.ItemID > int64(^uint32(0)) {
		return entry
	}
	isAvatar, previousIsClone := o.avatarClassifier(uint32(previous.ItemID))
	if !isAvatar {
		return entry
	}
	appearanceID := previous.ItemID
	if previousIsClone {
		appearanceID = firstExtraInt(previous.Extra, 0, dnfrepo.CloneAvatarAppearanceExtraKey)
	}
	if appearanceID <= 0 || appearanceID > int64(^uint32(0)) {
		return entry
	}
	entry.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] = strconv.FormatInt(appearanceID, 10)
	return entry
}

// clearCloneAvatarAppearance 返回一份"不带克隆外观键"的 entry 副本。背包行、
// 脱下回包、以及 withCloneAvatarAppearance 的入口都用它，键因此只可能存在于
// 当前真正穿着的克隆件的穿戴行上。同族 0.1.8 equip/owner.go:1084-1088。
func clearCloneAvatarAppearance(entry dnfrepo.EquipmentEntry) dnfrepo.EquipmentEntry {
	entry.Extra = cloneExtra(entry.Extra)
	delete(entry.Extra, dnfrepo.CloneAvatarAppearanceExtraKey)
	return entry
}

const (
	mainEquipmentInventorySlotStart int16 = 9
	mainEquipmentInventorySlotEnd   int16 = 64
	avatarInventorySlotStart        int16 = 0
	avatarInventorySlotEnd          int16 = 209
	petBodyInventorySlotStart       int16 = 0
	petBodyInventorySlotEnd         int16 = 139
	petArtifactInventorySlotStart   int16 = 140
	petArtifactInventorySlotEnd     int16 = 188
	guildMedalInventorySlotStart    int16 = 0
	guildMedalInventorySlotEnd      int16 = 48
)

func nextEmptyUnequipInventorySlot(items map[string]dnfrepo.ItemStack, listType byte, requested int16) (int16, bool) {
	start, end := mainEquipmentInventorySlotStart, mainEquipmentInventorySlotEnd
	switch listType {
	case wireListMain:
		// Equipment page only. Slots 0..8 are the main-list quick bar and the
		// later PVF pages own consumables, materials, quests, and professions.
	case wireListAvatar:
		// Current NoPack list-1 sort/page reader owns the 0..209 avatar page.
		start, end = avatarInventorySlotStart, avatarInventorySlotEnd
	case wireListPet:
		// Current NoPack splits list 7 into creature bodies 0..139 and
		// creature artifacts 140..188. Never redirect an unequip across those
		// pages; slots 189..238 are pet consumables and are not worn endpoints.
		switch {
		case requested >= petBodyInventorySlotStart && requested <= petBodyInventorySlotEnd:
			start, end = petBodyInventorySlotStart, petBodyInventorySlotEnd
		case requested >= petArtifactInventorySlotStart && requested <= petArtifactInventorySlotEnd:
			start, end = petArtifactInventorySlotStart, petArtifactInventorySlotEnd
		default:
			return 0, false
		}
	case wireListGuildMedal:
		// Current NoPack sub_E9AC00/sub_E9AC30 split the shared 98-slot
		// list-38 container into two 49-slot pages. Medal equip/unequip owns
		// only the first page; slots 49..97 belong to guardian gems.
		start, end = guildMedalInventorySlotStart, guildMedalInventorySlotEnd
	default:
		return 0, false
	}
	if requested < start || requested > end {
		return 0, false
	}
	for slot := requested; slot <= end; slot++ {
		if _, occupied := items[inventoryKey(listType, slot)]; !occupied {
			return slot, true
		}
	}
	for slot := start; slot < requested; slot++ {
		if _, occupied := items[inventoryKey(listType, slot)]; !occupied {
			return slot, true
		}
	}
	return 0, false
}

func guildMedalInventorySlotContains(slot int16) bool {
	return slot >= guildMedalInventorySlotStart && slot <= guildMedalInventorySlotEnd
}

func (o *Owner) swapEquipSlots(ctx context.Context, cmd MoveCommand) (MoveResult, error) {
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	record, ok, err := o.equipment.Load(ctx, characterID)
	if err != nil {
		return MoveResult{}, err
	}
	if !ok {
		return MoveResult{}, ErrEquipmentNotFound
	}
	record = ensureEquipmentRecord(dnfrepo.CloneEquipment(record), characterID)
	srcKey := entryKey(cmd.SourceSlotIndex)
	dstKey := entryKey(cmd.DestinationSlotIndex)
	source, sourceOK := record.Entries[srcKey]
	destination, destinationOK := record.Entries[dstKey]
	if !sourceOK {
		return MoveResult{}, fmt.Errorf("%w: equip slot=%d", ErrMoveSlotNotFound, cmd.SourceSlotIndex)
	}
	if source.SlotIndex != cmd.SourceSlotIndex {
		return MoveResult{}, fmt.Errorf("%w: key=%d entry=%d", ErrMoveSlotMismatch, cmd.SourceSlotIndex, source.SlotIndex)
	}
	if len(source.RawEntry) == 0 {
		return MoveResult{}, ErrMoveRawEntryMissing
	}
	if destinationOK && destination.SlotIndex != cmd.DestinationSlotIndex {
		return MoveResult{}, fmt.Errorf("%w: key=%d entry=%d", ErrMoveSlotMismatch, cmd.DestinationSlotIndex, destination.SlotIndex)
	}
	if destinationOK && len(destination.RawEntry) == 0 {
		return MoveResult{}, ErrMoveRawEntryMissing
	}
	if cmd.SourceSlotIndex == cmd.DestinationSlotIndex {
		return MoveResult{CharacterID: characterID, SourceListType: cmd.SourceListType, SourceSlotIndex: cmd.SourceSlotIndex, DestinationListType: cmd.DestinationListType, DestinationSlotIndex: cmd.DestinationSlotIndex, ItemID: source.ItemID, Mode: "noop"}, nil
	}
	if err := o.validatePlacement(ctx, characterID, source.ItemID, wireListEquipment, cmd.SourceSlotIndex, cmd.DestinationSlotIndex); err != nil {
		return MoveResult{}, err
	}
	if destinationOK {
		if err := o.validatePlacement(ctx, characterID, destination.ItemID, wireListEquipment, cmd.DestinationSlotIndex, cmd.SourceSlotIndex); err != nil {
			return MoveResult{}, err
		}
	}
	source = equipmentEntryAfterSlotSwap(source, cmd.DestinationSlotIndex)
	record.Entries[dstKey] = source
	if destinationOK {
		destination = equipmentEntryAfterSlotSwap(destination, cmd.SourceSlotIndex)
		record.Entries[srcKey] = destination
	} else {
		delete(record.Entries, srcKey)
	}
	record.UpdatedAt = time.Now()
	// 穿戴槽互换只动两格：srcKey 与 dstKey（都不动背包）。
	if err := o.saveMoveEquipmentEntries(ctx, record, []string{srcKey, dstKey}); err != nil {
		return MoveResult{}, err
	}
	result := MoveResult{
		CharacterID:          characterID,
		SourceListType:       cmd.SourceListType,
		SourceSlotIndex:      cmd.SourceSlotIndex,
		DestinationListType:  cmd.DestinationListType,
		DestinationSlotIndex: cmd.DestinationSlotIndex,
		ItemID:               source.ItemID,
		Mode:                 "equip_slot_swap",
		Changed:              true,
	}
	if destinationOK {
		result.SwappedItemID = destination.ItemID
	}
	return result, nil
}

// Only the validated current-client main/support pair carries this runtime
// slot identity. Other worn-slot formats retain their existing representation.
func equipmentEntryAfterSlotSwap(entry dnfrepo.EquipmentEntry, slot int16) dnfrepo.EquipmentEntry {
	old := entry.SlotIndex
	entry.SlotIndex = slot
	if !((old == 12 && slot == 24) || (old == 24 && slot == 12)) {
		return entry
	}
	entry.Extra = cloneExtra(entry.Extra)
	for _, key := range []string{"equipped_slot", "equipment_slot", "current_exe_equipment_type"} {
		entry.Extra[key] = strconv.Itoa(int(slot))
	}
	entry.Extra["current_exe_runtime_move"] = "1"
	if len(entry.RawEntry) == currentEquipmentRawEntryWireSize {
		entry.RawEntry = append([]byte(nil), entry.RawEntry...)
		binary.LittleEndian.PutUint16(entry.RawEntry[:2], uint16(slot))
		for _, key := range []string{"raw_entry_hex", "equipment_raw_entry_hex"} {
			if _, ok := entry.Extra[key]; ok {
				entry.Extra[key] = hex.EncodeToString(entry.RawEntry)
			}
		}
	}
	return entry
}

func (o *Owner) loadMoveRecords(ctx context.Context, characterID string) (dnfrepo.InventoryRecord, dnfrepo.EquipmentRecord, error) {
	inventory, ok, err := o.inventory.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.InventoryRecord{}, dnfrepo.EquipmentRecord{}, err
	}
	if !ok {
		return dnfrepo.InventoryRecord{}, dnfrepo.EquipmentRecord{}, ErrInventoryNotFound
	}
	equipment, ok, err := o.equipment.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.InventoryRecord{}, dnfrepo.EquipmentRecord{}, err
	}
	if !ok {
		return dnfrepo.InventoryRecord{}, dnfrepo.EquipmentRecord{}, ErrEquipmentNotFound
	}
	return ensureInventoryRecord(dnfrepo.CloneInventory(inventory), characterID), ensureEquipmentRecord(dnfrepo.CloneEquipment(equipment), characterID), nil
}

// saveMove 是穿/脱的唯一落库出口。keys 是本次移动真正改动的 key 集合
// （见 owner_move_save_scope.go）：
//
//	DNFBRIDGE_INVENTORY_SAVE_SCOPE=collection（默认）⇒ 背包整容器重写（= 今天）
//	DNFBRIDGE_EQUIPMENT_SAVE_SCOPE=collection（默认）⇒ 穿戴表整容器重写（= 今天）
//	置成 slot / entry ⇒ 只写 keys 里那 1-2 格（厂商 SaveDirtyItems 口径）
//
// keys 给不出来（空）时**逐字节回退到整容器写并记一条回退事件**，绝不静默不写。
func (o *Owner) saveMove(
	ctx context.Context,
	inventory dnfrepo.InventoryRecord,
	equipment dnfrepo.EquipmentRecord,
	fields []dnfrepo.InventoryField,
	keys moveWriteKeys,
	result MoveResult,
) (MoveResult, error) {
	now := time.Now()
	inventory.UpdatedAt = now
	equipment.UpdatedAt = now
	if err := o.saveMoveInventoryFields(ctx, inventory, fields, keys.inventory); err != nil {
		return MoveResult{}, err
	}
	if err := o.saveMoveEquipmentEntries(ctx, equipment, keys.equipment); err != nil {
		return MoveResult{}, err
	}
	result.Changed = true
	result.InventorySlots = cloneItemMap(inventory.Slots)
	return result, nil
}

func entryKey(slotIndex int16) string {
	return strconv.FormatInt(int64(slotIndex), 10)
}

const currentEquipmentSlotCount int16 = 33

func validateMoveStructure(cmd MoveCommand) (moveKind, error) {
	if cmd.SourceListType != wireListEquipment && cmd.DestinationListType != wireListEquipment {
		return moveKindOrdinary, ErrMoveUnsupported
	}
	if cmd.SourceSlotIndex < 0 || cmd.DestinationSlotIndex < 0 {
		return moveKindOrdinary, fmt.Errorf("%w: src=%d dst=%d", ErrMoveSlotOutOfRange, cmd.SourceSlotIndex, cmd.DestinationSlotIndex)
	}
	if cmd.SourceListType == wireListEquipment && cmd.SourceSlotIndex >= currentEquipmentSlotCount {
		return moveKindOrdinary, fmt.Errorf("%w: source=%d max=%d", ErrMoveSlotOutOfRange, cmd.SourceSlotIndex, currentEquipmentSlotCount-1)
	}
	if cmd.DestinationListType == wireListEquipment && cmd.DestinationSlotIndex >= currentEquipmentSlotCount {
		return moveKindOrdinary, fmt.Errorf("%w: destination=%d max=%d", ErrMoveSlotOutOfRange, cmd.DestinationSlotIndex, currentEquipmentSlotCount-1)
	}

	petMove := (cmd.SourceListType == wireListPet &&
		cmd.DestinationListType == wireListEquipment &&
		cmd.SourceSlotIndex <= 139 &&
		cmd.DestinationSlotIndex == petCreatureWornSlot) ||
		(cmd.SourceListType == wireListEquipment &&
			cmd.SourceSlotIndex == petCreatureWornSlot &&
			cmd.DestinationListType == wireListPet &&
			cmd.DestinationSlotIndex <= 139)
	artifactMove := (cmd.SourceListType == wireListPet &&
		cmd.DestinationListType == wireListEquipment &&
		cmd.SourceSlotIndex >= petArtifactSourceSlotMin && cmd.SourceSlotIndex <= petArtifactSourceSlotMax &&
		isPetArtifactTarget(cmd.DestinationSlotIndex)) ||
		(cmd.SourceListType == wireListEquipment &&
			isPetArtifactTarget(cmd.SourceSlotIndex) &&
			cmd.DestinationListType == wireListPet &&
			cmd.DestinationSlotIndex >= petArtifactSourceSlotMin && cmd.DestinationSlotIndex <= petArtifactSourceSlotMax)
	if cmd.SourceListType == wireListPet || cmd.DestinationListType == wireListPet {
		if petMove {
			return moveKindPet, nil
		}
		if artifactMove {
			return moveKindArtifact, nil
		}
		return moveKindOrdinary, fmt.Errorf("%w: pet move src=(%d,%d) dst=(%d,%d)", ErrMoveUnsupported, cmd.SourceListType, cmd.SourceSlotIndex, cmd.DestinationListType, cmd.DestinationSlotIndex)
	}
	if !isOrdinaryMoveList(cmd.SourceListType) || !isOrdinaryMoveList(cmd.DestinationListType) {
		return moveKindOrdinary, fmt.Errorf("%w: src=%d dst=%d", ErrMoveUnsupported, cmd.SourceListType, cmd.DestinationListType)
	}
	return moveKindOrdinary, nil
}

type moveKind int

const (
	moveKindOrdinary moveKind = iota
	moveKindPet
	moveKindArtifact
)

// Current NoPack closes the artifact family as runtime targets 27/28/29
// (sub_35FC900 -> equip_artifactred/blue/green) and the shared list-7 source
// range 140..188 (sub_30A4950's unequip search); it is not one slot per color.
const (
	petArtifactSourceSlotMin int16 = 140
	petArtifactSourceSlotMax int16 = 188
)

func isPetArtifactTarget(slot int16) bool {
	return slot >= 27 && slot <= 29
}

func isOrdinaryMoveList(listType byte) bool {
	switch listType {
	case wireListMain, wireListAvatar, wireListPersonalCargo, wireListEquipment, wireListGuildMedal:
		return true
	default:
		return false
	}
}

func (o *Owner) validatePlacement(ctx context.Context, characterID string, itemID int64, sourceListType byte, sourceSlotIndex int16, targetSlotIndex int16) error {
	// Current NoPack op19 closes the creature body path as list-7 slots
	// 0..139 <-> actor worn target 26.
	if sourceListType == wireListPet && sourceSlotIndex >= 0 && sourceSlotIndex <= 139 && targetSlotIndex == petCreatureWornSlot {
		return nil
	}
	if o == nil || o.placementValidator == nil {
		return ErrMoveValidatorRequired
	}
	return o.placementValidator.ValidateEquipmentPlacement(ctx, Placement{
		CharacterID:     characterID,
		ItemID:          itemID,
		SourceListType:  sourceListType,
		SourceSlotIndex: sourceSlotIndex,
		TargetSlotIndex: targetSlotIndex,
	})
}

const (
	wireListMain          byte  = 0
	wireListAvatar        byte  = 1
	wireListPersonalCargo byte  = 2
	wireListEquipment     byte  = 3
	wireListPet           byte  = 7
	wireListGuildMedal    byte  = 38
	petCreatureWornSlot   int16 = 26
)

func ensureInventoryRecord(record dnfrepo.InventoryRecord, characterID string) dnfrepo.InventoryRecord {
	record.CharacterID = characterID
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	if record.Warehouse == nil {
		record.Warehouse = make(map[string]dnfrepo.ItemStack)
	}
	return record
}

func ensureEquipmentRecord(record dnfrepo.EquipmentRecord, characterID string) dnfrepo.EquipmentRecord {
	record.CharacterID = characterID
	if record.Entries == nil {
		record.Entries = make(map[string]dnfrepo.EquipmentEntry)
	}
	return record
}

func inventoryMap(record *dnfrepo.InventoryRecord, listType byte) (map[string]dnfrepo.ItemStack, dnfrepo.InventoryField, bool) {
	switch listType {
	case wireListMain, wireListAvatar, wireListPet, wireListGuildMedal:
		return record.Slots, dnfrepo.InventoryFieldSlots, true
	case wireListPersonalCargo:
		return record.Warehouse, dnfrepo.InventoryFieldWarehouse, true
	default:
		return nil, "", false
	}
}

func inventoryKey(listType byte, slotIndex int16) string {
	return fmt.Sprintf("%d:%d", listType, slotIndex)
}

func entryFromStack(stack dnfrepo.ItemStack, sourceListType byte, equipSlot int16, sourceInstanceValue int32) (dnfrepo.EquipmentEntry, error) {
	if sourceListType == wireListPet && equipSlot == petCreatureWornSlot {
		return petCreatureEntryFromStack(stack, equipSlot)
	}
	raw := append([]byte(nil), stack.RawEntry...)
	if len(raw) == 0 {
		if currentTitleBookStackNeedsRawEntry(stack, sourceListType, equipSlot) {
			raw = buildCurrentTitleEquipmentRawEntry(stack.ItemID, equipSlot)
		} else if rebuilt, ok := buildCurrentEquipmentRawEntryFromMoveEvidence(stack, sourceListType, equipSlot, sourceInstanceValue); ok {
			raw = rebuilt
		} else {
			var err error
			raw, err = rawEntryFromExtra(stack.Extra)
			if err != nil {
				return dnfrepo.EquipmentEntry{}, err
			}
		}
	}
	raw = overlayEquipmentTailDataFromExtra(raw, stack.Extra)
	extra := cloneExtra(stack.Extra)
	// DOC-067：穿戴栏不展示位置锁，穿/脱一律不携带（A21 InventoryMoveService.cs:806-810，
	// source/destination 任一端是 Equipment 即清）。清在进穿戴行这一步，脱下时背包副本
	// 由穿戴行重建，因此两个方向都不会把锁带回来。
	delete(extra, dnfrepo.SortLockExtraKey)
	if len(raw) == currentEquipmentRawEntryWireSize {
		raw[0x60] = 0
	}
	// A permanent avatar is a real, occupied list-1 item whose current-EXE
	// amount field is zero.  Count==0 therefore cannot mean "empty slot" for
	// this container. Preserve the repository value explicitly while the item
	// is worn, otherwise an unequip would silently turn it into Count==1.
	//
	// This marker is internal persistence state only; the outward 0x77 amount
	// remains reconstructed from amount_or_count / the real ItemStack when a
	// list snapshot is emitted.
	extra["current_exe_inventory_count"] = strconv.FormatInt(stack.Count, 10)
	extra["equipped_slot"] = strconv.Itoa(int(equipSlot))
	// Persist the current-EXE actor slot chosen by the validated op19 move.
	// Initial PVF records use legacy worn-slot numbering and deliberately do
	// not carry this marker; once such an item is moved by the runtime, the
	// explicit slot must win on later op13/mode1 rebuilds.
	extra["current_exe_equipment_type"] = strconv.Itoa(int(equipSlot))
	extra["current_exe_runtime_move"] = "1"
	return dnfrepo.EquipmentEntry{
		SlotIndex: equipSlot,
		ItemID:    stack.ItemID,
		Bind:      stack.Bind,
		ExpireAt:  stack.ExpireAt,
		RawEntry:  raw,
		Extra:     extra,
	}, nil
}

const currentEquipmentRawEntryWireSize = 0x77

// buildCurrentEquipmentRawEntryFromMoveEvidence repairs only the historical
// relational rows that already have enough current-client/PVF evidence to be
// rendered as equipment but lost their opaque 0x77 row during import.  The
// live op19 source instance is required; no identity is invented when the
// client did not prove one.
//
// 耐久证据与身份证据分开判定：运行时 PVF 对 `[ring]` / `[amulet]` / `[wrist]` /
// `[support]` / `[magic stone]` / `[earring]`（以及装扮、宠物、称号、勋章、宠物
// 神器）**根本没有 `[durability]` 段**，这些装备没有耐久语义，耐久字段是
// "不适用"而不是"证据缺失" —— 详见 currentEquipmentRawEntryDurability。
// quality_seed 与 op19 源实例这两条**身份**证据的要求没有任何放宽。
func buildCurrentEquipmentRawEntryFromMoveEvidence(
	stack dnfrepo.ItemStack,
	sourceListType byte,
	equipSlot int16,
	sourceInstanceValue int32,
) ([]byte, bool) {
	if (sourceListType != wireListMain && sourceListType != wireListAvatar) ||
		stack.ItemID <= 0 || stack.ItemID > int64(^uint32(0)) || stack.Count != 1 ||
		sourceInstanceValue == 0 || stack.Extra == nil ||
		!strings.EqualFold(strings.TrimSpace(stack.Extra["item_kind"]), "equipment") ||
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(stack.Extra["pvf_path"])), "equipment/") {
		return nil, false
	}
	qualitySeed := uint32(firstExtraInt(stack.Extra, 0, "quality_seed"))
	if !itemquality.Valid(qualitySeed) {
		return nil, false
	}
	durability, durabilityProven := currentEquipmentRawEntryDurability(stack.Extra)
	if !durabilityProven {
		return nil, false
	}
	raw := make([]byte, currentEquipmentRawEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(equipSlot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(stack.ItemID))
	binary.LittleEndian.PutUint32(raw[6:10], qualitySeed)
	raw[0x0A] = byte(firstExtraInt(stack.Extra, 0, "packed_flag_byte", "packed_flag"))
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], durability)
	raw[0x0D] = byte(firstExtraInt(stack.Extra, 0, "seal_flag", "seal"))
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(sourceInstanceValue))
	return raw, true
}

// currentEquipmentRawEntryDurability resolves the durability word written at
// raw+0x0B when a lost 0x77 equipment row is rebuilt from op19 move evidence.
//
// 运行时 PVF 实测（runtime/data/dnf/Script.pvf，equipment/equipment.lst 全量）：
// ring 3142/3142、amulet 3022/3022、wrist 3012/3012、support 2677/2677、
// magic stone 1651/1651、earring 25/25 份文档**都没有 `[durability]` 段**；
// 装扮/宠物/称号/勋章/宠物神器同样没有。发放端与掉落端因此只在 PVF 声明了正耐久
// 时才写 durability/max_durability（gmtool.grantAttachmentExtra、
// dungeon_drop_pickup.buildCurrentDungeonPickupStack、
// mailbox_durability_projection.mergeCurrentMailAttachmentDurabilityExtra 全是
// `> 0` 才写），所以 GM 邮件领出来的首饰/左右槽/耳环 Extra 里一条耐久键都没有。
//
// 参考实现（86JP ItemMetadataResolver.HasDurabilityByType）同样按类型白名单只给
// 武器/护甲耐久，其余装备 Durability = 0；真实存档 dnf_equipment_entries 里已穿戴
// 的 amulet(20002)/wrist(22002)/ring(24002) 0x77 行 0x0B 也都是 0。
//
// 所以"一条耐久键都没有"= 该装备没有耐久（0x0B 写 0），**不是**身份证据缺失；
// 按"缺耐久就拒绝穿上"处理会让这些装备永远穿不上（服务端静默不回包、客户端本地
// 乐观穿上、重登又掉下来）。已经写了耐久键就必须能读出当前耐久 0..0xffff：
// 坏值/超范围/只写了一半（只有 max_durability）仍然失败关闭 —— 闸门只为
// "PVF 完全未声明耐久"这一种形状打开。
func currentEquipmentRawEntryDurability(extra map[string]string) (uint16, bool) {
	declared := false
	for _, key := range []string{"durability", "current_durability", "max_durability"} {
		if strings.TrimSpace(extra[key]) != "" {
			declared = true
			break
		}
	}
	value := firstExtraInt(extra, -1, "durability", "current_durability")
	if value < 0 {
		// 一条耐久键都没有 ⇒ 无耐久装备（0）；写了耐久键却读不出当前耐久 ⇒
		// 坏证据/半截证据，失败关闭（max_durability 不能冒充当前耐久）。
		return 0, !declared
	}
	if value > int64(^uint16(0)) {
		return 0, false
	}
	return uint16(value), true
}

func currentTitleBookStackNeedsRawEntry(stack dnfrepo.ItemStack, sourceListType byte, equipSlot int16) bool {
	if sourceListType != wireListMain ||
		equipSlot != 13 ||
		stack.ItemID <= 0 ||
		stack.ItemID > int64(^uint32(0)) ||
		!strings.EqualFold(strings.TrimSpace(stack.Extra["source"]), "title_book_get") {
		return false
	}
	category, categoryErr := strconv.ParseInt(strings.TrimSpace(stack.Extra["title_book_category"]), 10, 32)
	index, indexErr := strconv.ParseInt(strings.TrimSpace(stack.Extra["title_book_index"]), 10, 32)
	return categoryErr == nil && category >= 0 && category < 5 &&
		indexErr == nil && index >= 0 && index < 200
}

func buildCurrentTitleEquipmentRawEntry(itemID int64, equipSlot int16) []byte {
	// Current selected-equipment records use the same 43-byte constructor
	// summary as initial equipment. A title has no durability; byte 0 is the
	// validated actor slot, +1 is the PVF item ID, and +5 is the constructor's
	// required create scalar 1. The remaining fields are zero.
	raw := make([]byte, 43)
	raw[0] = byte(equipSlot)
	binary.LittleEndian.PutUint32(raw[1:5], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[5:9], 1)
	return raw
}

func petCreatureEntryFromStack(stack dnfrepo.ItemStack, equipSlot int16) (dnfrepo.EquipmentEntry, error) {
	serial := firstExtraInt(stack.Extra, 0, "creature_serial_or_handle", "creature_serial", "pet_serial", "serial", "handle", "instance_value", "item_uid")
	if serial <= 0 {
		return dnfrepo.EquipmentEntry{}, ErrMoveRawEntryMissing
	}
	enchantCardID, enchantUpgradeCount, err := petEnchantFieldsFromStack(stack)
	if err != nil {
		return dnfrepo.EquipmentEntry{}, err
	}
	raw := buildPetCreatureEquipEntry(equipSlot, stack.ItemID, serial)
	binary.LittleEndian.PutUint32(raw[0x10:0x14], enchantCardID)
	raw[0x14] = enchantUpgradeCount
	extra := cloneExtra(stack.Extra)
	extra["equipped_slot"] = strconv.Itoa(int(equipSlot))
	extra["pet_enchant_card_item_id"] = strconv.FormatUint(uint64(enchantCardID), 10)
	extra["enchant_card_id"] = strconv.FormatUint(uint64(enchantCardID), 10)
	extra["enchant_upgrade_count"] = strconv.FormatUint(uint64(enchantUpgradeCount), 10)
	extra["value_a"] = strconv.FormatUint(uint64(enchantCardID), 10)
	extra["byte_12"] = strconv.FormatUint(uint64(enchantUpgradeCount), 10)
	extra["raw_entry_hex"] = hex.EncodeToString(raw)
	return dnfrepo.EquipmentEntry{
		SlotIndex: equipSlot,
		ItemID:    stack.ItemID,
		Bind:      stack.Bind,
		ExpireAt:  stack.ExpireAt,
		RawEntry:  raw,
		Extra:     extra,
	}, nil
}

func petEnchantFieldsFromStack(stack dnfrepo.ItemStack) (uint32, byte, error) {
	cardID := firstExtraInt(stack.Extra, -1, "pet_enchant_card_item_id", "enchant_card_id", "value_a")
	if cardID < 0 && len(stack.RawEntry) >= 0x13 {
		cardID = int64(binary.LittleEndian.Uint32(stack.RawEntry[0x0E:0x12]))
	}
	if cardID < 0 {
		cardID = 0
	}
	if cardID > int64(^uint32(0)) {
		return 0, 0, ErrMoveRawEntryInvalid
	}
	upgradeCount := firstExtraInt(stack.Extra, -1, "enchant_upgrade_count", "pet_enchant_upgrade_count", "byte_12")
	if upgradeCount < 0 && len(stack.RawEntry) >= 0x13 {
		upgradeCount = int64(stack.RawEntry[0x12])
	}
	if upgradeCount < 0 {
		upgradeCount = 0
	}
	if upgradeCount > int64(^byte(0)) {
		return 0, 0, ErrMoveRawEntryInvalid
	}
	return uint32(cardID), byte(upgradeCount), nil
}

// inheritPetEnchantFields 是**宠物分支专属**的附魔回落：源行（宠物栏 list 7）没有
// 附魔键时，按 creature_key/序列号从 PetEntry（dnf_pet_entry_extra）取回宝珠字段。
//
// 现场（角色 3，2026-09-24）：宝珠已经成功写进 PetEntry
// （`enchant_card_id` / `pet_enchant_card_item_id` = 10008668，由
// inventory.setPetEntryEnchantFields 写入），但穿戴行只从背包行的 Extra 读
// （petCreatureEntryFromStack → petEnchantFieldsFromStack），背包行没有这些键 →
// 穿戴态 raw[0x10:0x14]/[0x14] 与 Extra 全写成 0 → 客户端看起来"宝珠消失"。
//
// 普通装备（武器/防具等）不吃这条回落：它们的附魔本来就在堆栈 Extra ←→ 装备行
// 之间正常搬运（操作者实测正常），所以这里只处理 list 7 → 槽 26 的宠物本体。
func (o *Owner) inheritPetEnchantFields(ctx context.Context, characterID string, stack dnfrepo.ItemStack) dnfrepo.ItemStack {
	if o == nil || o.pets == nil || stack.ItemID <= 0 {
		return stack
	}
	if firstExtraInt(stack.Extra, 0, "pet_enchant_card_item_id", "enchant_card_id", "value_a") > 0 {
		return stack
	}
	record, found, err := o.pets.Load(ctx, characterID)
	if err != nil || !found {
		return stack
	}
	key, err := findPetEntryKey(record, petStackSerial(stack), stack.ItemID)
	if err != nil || key == "" {
		return stack
	}
	petEntry, ok := record.Entries[key]
	if !ok {
		return stack
	}
	cardID := firstExtraInt(petEntry.Extra, 0, "pet_enchant_card_item_id", "enchant_card_id", "value_a")
	if cardID <= 0 {
		return stack
	}
	upgrade := firstExtraInt(petEntry.Extra, 0, "enchant_upgrade_count", "pet_enchant_upgrade_count", "byte_12")
	if upgrade < 0 {
		upgrade = 0
	}
	extra := cloneExtra(stack.Extra)
	if extra == nil {
		extra = make(map[string]string, 6)
	}
	card := strconv.FormatInt(cardID, 10)
	upgradeText := strconv.FormatInt(upgrade, 10)
	extra["pet_enchant_card_item_id"] = card
	extra["enchant_card_id"] = card
	extra["value_a"] = card
	extra["enchant_upgrade_count"] = upgradeText
	extra["pet_enchant_upgrade_count"] = upgradeText
	extra["byte_12"] = upgradeText
	stack.Extra = extra
	return stack
}

func stackFromEntry(entry dnfrepo.EquipmentEntry) dnfrepo.ItemStack {
	extra := cloneExtra(entry.Extra)
	raw := overlayEquipmentTailDataFromExtra(append([]byte(nil), entry.RawEntry...), entry.Extra)
	// Also clear historical worn records that predate the equip-side cleanup.
	delete(extra, dnfrepo.SortLockExtraKey)
	if len(raw) == currentEquipmentRawEntryWireSize {
		raw[0x60] = 0
	}
	if len(raw) > 0 {
		extra["raw_entry_hex"] = hex.EncodeToString(raw)
	}
	extra["equipment_slot"] = strconv.Itoa(int(entry.SlotIndex))
	if entry.SlotIndex == petCreatureWornSlot {
		if serial := petSerialFromEquippedRaw(raw); serial > 0 {
			extra["creature_serial_or_handle"] = strconv.FormatInt(serial, 10)
		}
		// 宠物脱下：附魔必须落进**背包行**的 0x77 行字节（0x0E:0x12 = 宝珠 id、
		// 0x12 = 强化数）并与 Extra 双写。普通装备走 setEnchantFields 就是这个契约，
		// 宠物此前只 cloneExtra → 穿戴行有宝珠、脱下后背包里看不到（现场报障）。
		raw, extra = carryPetEnchantToInventoryRow(raw, extra)
	}
	return dnfrepo.ItemStack{
		ItemID:   entry.ItemID,
		Count:    equipmentEntryInventoryCount(entry),
		Bind:     entry.Bind,
		ExpireAt: entry.ExpireAt,
		RawEntry: raw,
		Extra:    extra,
	}
}

const (
	equipmentSocketCollectionOffset = 0x27
	equipmentSocketCollectionBytes  = 17
	equipmentLegacyTailDataOffset   = 0x2F
	equipmentTailDataBytes          = 37
	// petInventoryRowSize 是背包（list 7）宠物行的固定 0x77 行长度：客户端的
	// 背包行读取器按这个长度取"附魔宝珠 id（0x0E:0x12）与强化数（0x12）"。
	petInventoryRowSize = 0x77
)

// carryPetEnchantToInventoryRow 把穿戴中宠物行的附魔投影成**背包行**形态。
// 穿戴行是 43 字节布局（宝珠 id 在 0x10:0x14、强化数在 0x14），背包行是 0x77
// 布局（id 在 0x0E:0x12、强化数在 0x12）——两者偏移不同，所以必须显式搬运，
// 否则脱下后背包里看不到宝珠词条。Extra 侧同时双写，与普通装备 setEnchantFields 同契约。
func carryPetEnchantToInventoryRow(raw []byte, extra map[string]string) ([]byte, map[string]string) {
	if extra == nil {
		extra = make(map[string]string, 8)
	}
	card := petEnchantCardFromExtra(extra)
	upgrade := petEnchantUpgradeFromExtra(extra)
	if len(raw) >= 0x15 {
		if card == 0 {
			card = int64(binary.LittleEndian.Uint32(raw[0x10:0x14]))
		}
		if upgrade == 0 {
			upgrade = int64(raw[0x14])
		}
	}
	if card <= 0 && upgrade == 0 {
		return raw, extra
	}
	row := make([]byte, petInventoryRowSize)
	copy(row, raw)
	binary.LittleEndian.PutUint32(row[0x0E:0x12], uint32(card))
	row[0x12] = byte(upgrade)
	extra["value_a"] = strconv.FormatInt(card, 10)
	extra["enchant_card_id"] = strconv.FormatInt(card, 10)
	extra["pet_enchant_card_item_id"] = strconv.FormatInt(card, 10)
	extra["enchant_upgrade_count"] = strconv.FormatInt(upgrade, 10)
	extra["pet_enchant_upgrade_count"] = strconv.FormatInt(upgrade, 10)
	extra["byte_12"] = strconv.FormatInt(upgrade, 10)
	extra["raw_entry_hex"] = hex.EncodeToString(row)
	return row, extra
}

// petEnchantCardFromExtra 按"宠物附魔卡 id"的既有键顺序读 Extra。
func petEnchantCardFromExtra(extra map[string]string) int64 {
	for _, key := range []string{"pet_enchant_card_item_id", "enchant_card_id", "value_a"} {
		if value, err := strconv.ParseInt(strings.TrimSpace(extra[key]), 10, 64); err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func petEnchantUpgradeFromExtra(extra map[string]string) int64 {
	for _, key := range []string{"enchant_upgrade_count", "pet_enchant_upgrade_count", "byte_12"} {
		if value, err := strconv.ParseInt(strings.TrimSpace(extra[key]), 10, 64); err == nil && value > 0 {
			return value
		}
	}
	return 0
}

var equipmentTailDataExtraKeys = []string{"tail_data_2f", "tailData2F", "tail2f", "raw_data_2f"}

func overlayEquipmentTailDataFromExtra(raw []byte, extra map[string]string) []byte {
	// Current NoPack parses raw+0x27..+0x37 as a live fixed-row vector.
	// Earlier code overlaid a legacy C# tail representation on that vector
	// during ordinary equip moves.  The current EXE has not proved that either
	// structure represents a normal-equipment socket, so preserve repository
	// raw bytes exactly until the real mutation is closed.
	_ = extra
	return raw
}

func equipmentSocketPrimaryValue(value []byte, visible bool) uint32 {
	if !visible {
		return 0
	}
	if len(value) >= 4 {
		if raw := binary.LittleEndian.Uint32(value[:4]); raw != 0 {
			return raw
		}
	}
	return ^uint32(0)
}

func equipmentTailDataFromExtra(extra map[string]string) []byte {
	if len(extra) == 0 {
		return nil
	}
	var fallback []byte
	for _, key := range equipmentTailDataExtraKeys {
		raw := strings.TrimSpace(extra[key])
		if raw == "" {
			continue
		}
		raw = strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "", "0x", "", "0X", "").Replace(raw)
		decoded, err := hex.DecodeString(raw)
		if err != nil || len(decoded) == 0 {
			continue
		}
		out := make([]byte, equipmentTailDataBytes)
		copy(out, decoded)
		if equipmentTailHasSocketData(out) {
			return out
		}
		if fallback == nil {
			fallback = out
		}
	}
	return fallback
}

func equipmentTailHasSocketData(tail []byte) bool {
	if len(tail) == 0 {
		return false
	}
	if tail[0] != 0 {
		return true
	}
	if len(tail) >= 9 {
		return binary.LittleEndian.Uint32(tail[1:5]) != 0 || binary.LittleEndian.Uint32(tail[5:9]) != 0
	}
	for _, value := range tail {
		if value != 0 {
			return true
		}
	}
	return false
}

// isEquipmentMoveUnit accepts exactly one ordinary equipment item. The
// current client represents a permanent avatar in list 1 with amount/count
// zero, but it is still a single wearable object (its non-empty item id and
// later PVF placement check are authoritative). Do not broaden this exception
// to normal inventory lists, where zero remains invalid for an equip move.
func isEquipmentMoveUnit(stack dnfrepo.ItemStack, listType byte) bool {
	if stack.Count == 1 {
		return true
	}
	return listType == wireListAvatar && stack.Count == 0
}

// equipmentEntryInventoryCount restores the exact container count recorded at
// equip time. Historical worn rows predate that marker and are normal
// equipment, so they retain the proven single-item fallback of one.
func equipmentEntryInventoryCount(entry dnfrepo.EquipmentEntry) int64 {
	if raw := strings.TrimSpace(entry.Extra["current_exe_inventory_count"]); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil && value >= 0 {
			return value
		}
	}
	return 1
}

func rawEntryFromExtra(extra map[string]string) ([]byte, error) {
	if extra == nil {
		return nil, ErrMoveRawEntryMissing
	}
	raw := strings.TrimSpace(extra["raw_entry_hex"])
	if raw == "" {
		raw = strings.TrimSpace(extra["equipment_raw_entry_hex"])
	}
	if raw == "" {
		return nil, ErrMoveRawEntryMissing
	}
	raw = strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "", "0x", "", "0X", "").Replace(raw)
	if len(raw)%2 != 0 {
		return nil, fmt.Errorf("%w: odd hex length", ErrMoveRawEntryInvalid)
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("%w: %v", ErrMoveRawEntryInvalid, err)
	}
	return decoded, nil
}

func buildPetCreatureEquipEntry(slot int16, itemID int64, serial int64) []byte {
	var buf bytes.Buffer
	writeByte := func(value byte) {
		_ = buf.WriteByte(value)
	}
	writeUint16 := func(value uint16) {
		var tmp [2]byte
		binary.LittleEndian.PutUint16(tmp[:], value)
		buf.Write(tmp[:])
	}
	writeUint32 := func(value uint32) {
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], value)
		buf.Write(tmp[:])
	}

	writeByte(byte(slot))
	writeUint32(uint32(clampInt32(itemID)))
	writeUint32(uint32(clampInt32(serial)))
	writeByte(0)
	writeUint16(0)
	writeUint32(0)
	writeUint32(0)
	writeByte(0)
	writeByte(0)
	writeUint16(0)
	writeUint32(uint32(clampInt32(serial)))
	writeByte(0)
	writeUint32(0)
	writeByte(0)
	writeUint16(0)
	writeByte(0)
	buf.Write(make([]byte, 10))
	return buf.Bytes()
}

func petSerialFromEquippedRaw(raw []byte) int64 {
	// The current pet constructor repeats the creature serial/handle at +24.
	// Do not accept the ordinary equipment instance scalar at +5. The current
	// creature row carries its authoritative serial at +24, while ordinary
	// equipment instance values are not creature identities.
	// identity. buildPetCreatureEquipEntry always writes the authoritative +24
	// field, so a shorter historical/ordinary row must fail closed here.
	if len(raw) >= 28 {
		value := int64(binary.LittleEndian.Uint32(raw[24:28]))
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstExtraInt(extra map[string]string, fallback int64, keys ...string) int64 {
	for _, key := range keys {
		raw := strings.TrimSpace(extra[key])
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 0, 64)
		if err == nil {
			return value
		}
	}
	return fallback
}

func clampInt32(value int64) int32 {
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	if value < -int64(^uint32(0)>>1)-1 {
		return -int32(^uint32(0)>>1) - 1
	}
	return int32(value)
}

func cloneExtra(extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return make(map[string]string)
	}
	out := make(map[string]string, len(extra)+2)
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func cloneItemMap(in map[string]dnfrepo.ItemStack) map[string]dnfrepo.ItemStack {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]dnfrepo.ItemStack, len(in))
	for key, value := range in {
		value.Extra = cloneExtra(value.Extra)
		out[key] = value
	}
	return out
}
