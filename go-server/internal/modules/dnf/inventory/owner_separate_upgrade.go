// 本文件是 op438「锻造 / 别升级（separate upgrade）」的领域写侧：材料消耗与
// 锻造等级写入必须落在**同一个角色物品事务**里，成功 / 掷点失败 / 校验失败三条
// 路径都以"回包前已持久提交"为界；校验类拒绝不动任何资产。
//
// 两份互相独立的真源，逐条核对过：
//
//	A 运行时 PVF（解析在 dnfbridge/forging_pvf.go）：etc/upgrade_separate.etc 的
//	  [separate upgrade max] / [table]（4 个数一档：档位、?、成功率、材料权重）/
//	  [item weights by grade]（装备品级 → 材料模板 + 基础数量）/
//	  [item weights by rarity]（7 个稀有度权重），材料槽那一件的
//	  [portable genuine damage upgrade upgrede] / [equipment separate reinforcement
//	  ticket] / [action type] [lucky enchant deed] 2 身份与 [reinforcement usable
//	  item list] 白名单。
//	B 厂商参考 ServerS4A21 Game/Inventory/Services/InventorySeparateUpgradeService.cs
//	  :175-270（普通锻造）与 :60-160（券档），错误码取同族
//	  Game/ItemUpgrade/SeparateUpgradeModels.cs 的 SeparateUpgradeResult.Error*
//	  （ErrorMaterialCommit=17 / ErrorInvalidTarget=4 / ErrorDurability=7 /
//	  ErrorUnsupported=13 / ErrorNotWeapon=19 / ErrorInvalidMaterial=22 /
//	  ErrorMaxLevel=95）。参考实现里**没有** portable 锻造，所以便携式锻造炉那
//	  一档按真档物品说明实现（只消耗炉子 1 个、不消耗强烈的气息），不盲搬。
//
// 与 op50 强化/增幅的有意差异：
//
//   - 锻造等级落点是 **0x77 行的 0x57 字节 + Extra 的 byte_57/value_57**
//     （证据链见 services/gmtool/api_items_configure.go 的 ITEMCFG-D3；op50 用的是
//     packed ext 字节 0x0A，两处互不相干，绝不能混写）；
//   - 掷点满值是 **10000**（[table] 第 3 个数就是百分数×100），不是 op50 的 100000；
//   - 失败**不摧毁、不掉级**（普通锻造失败只是材料没了；券档只有显式写了
//     FailureDecrementLevel 才掉级，真档的幸运锻造券是 0），所以这里不接
//     保护券/补偿道具那一套；
//   - 耗材若写了 `[check usable itemlevel]` 窗口（真档便携式锻造炉 1..90 / 1..85，
//     锻造券与幸运锻造券都没有这一段），目标装备的 `[minimum level]` 必须落在窗口内，
//     否则按厂商同族的 ErrorRestriction 口径拒绝（客户端可见码 19，实测命中专用文案键
//     0x6a1a）。见 forgingConsumableAllowsTargetLevel。
package inventory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	// ErrForgingResolverRequired 表示这条 op438 没有运行时 PVF 解析器：
	// 不允许猜等级表/材料表，命令失败关闭（handler 回失败 ACK，不回成功体）。
	ErrForgingResolverRequired = errors.New("separate upgrade requires a runtime-PVF resolver")
	// ErrForgingEquipmentOwnerUnavailable 表示目标在穿戴表（list 3）但事务里
	// 拿不到装备仓储：同样失败关闭，绝不改主背包了事。
	ErrForgingEquipmentOwnerUnavailable = errors.New("separate upgrade requires an equipment repository for the worn target")
)

// 客户端可见错误码：取值来自厂商同族实现 SeparateUpgradeResult.Error*，并且已在本机对
// 当前 EXE（DNF.exe 8bf1d39d…）的 438 失败回调 0x2483b60 逐码核对过跳转：
//
//	0x2483b60 先 push 0/0/0/4 调 0x12030a0 复位锻造 UI（**无条件、先于读错误码**），
//	再 movzx esi, byte[ebp+0x10] 取码；码 > 0xd6 走 0x2483c23 的 default 分支
//	（文案键 0x67e，把码格式化进去）；码 <= 0xd6 走 0x2483d38 的字节索引表 +
//	0x2483d0c 的 11 槽跳转表。下表是逐码实测（本文件用到的 7 个码全部命中**专用**槽）：
//
//	码 4  -> 槽 1 -> 0x2483a1c 文案键 0x67a      码 7  -> 槽 2 -> 0x2483bf0 文案键 0x4e20
//	码 10 -> 槽 3 -> 0x2483bba 文案键 0x67c      码 13 -> 槽 4 -> 0x2483bdf 文案键 0x676
//	码 17 -> 槽 5 -> 0x2483ba6 文案键 0x8904     码 19 -> 槽 6 -> 0x2483bce 文案键 0x6a1a
//	码 22 -> 槽 7 -> 0x2483b4c 文案键 0x67d      码 95 -> 槽 4 -> 0x2483bdf 文案键 0x676
//	码 175 -> 槽 8 -> 0x2483c01 文案键 0x5338    码 214 -> 槽 9 -> 0x2483c12 文案键 0xa4f8
//	码 0 / 118 -> 槽 0 -> 0x2483c5f **不显示任何文案**（直接跳到函数出口）
//	其余 203 个码 + 所有 > 0xd6 的码 -> 槽 10 -> 0x2483c23 default（文案键 0x67e + 码）
//
// 另有一道前置闸门（0x2483b7c 调 0x35ca060，test al,al / jne 0x2483c5f）：闸门为真时
// **连文案都不显示**，所以"某码一定弹出某句文案"不成立；能保证的只有"复位 UI 一定执行"。
// 文案键对应的具体字串没有在本树里解析（客户端字符串是打包存储），这里只记录命中的键。
// 因此选用的是厂商同族枚举中**在本 EXE 有专用文案槽**的那几个码，不臆造新值。
const (
	forgingErrorInvalidTarget  byte = 4
	forgingErrorDurability     byte = 7
	forgingErrorUnsupported    byte = 13
	forgingErrorMaterialCommit byte = 17
	forgingErrorNotWeapon      byte = 19
	// forgingErrorRestriction 是"这件耗材不允许用在这件目标上"的客户端可见码，与
	// forgingErrorNotWeapon 同值（19）：厂商同族里 op50 的等级/部位限制用
	// ItemUpgradeResult.ErrorRestriction=19，separate-upgrade 的"非武器"用 19，
	// 当前 EXE 的 438 失败回调把 19（0x13）映射到同一个专用文案键 0x6a1a。
	forgingErrorRestriction     byte = 19
	forgingErrorInvalidMaterial byte = 22
	forgingErrorMaxLevel        byte = 95
)

// forgingSuccessWeightScale 是锻造掷点的满值：PVF [table] 第 3 个数就是
// 「百分数 ×100」，厂商 InventorySeparateUpgradeService 用的是 ServerRandom.Next(10000)。
const forgingSuccessWeightScale = 10000

// forgingRowOffset 是锻造等级在当前客户端 0x77 物品行里的唯一字节。
const forgingRowOffset = 0x57

// forgingExtraKeys 是锻造等级在 Extra 里的两个别名（与既有投影/客户端行生成的
// 读序完全一致：sceneInventoryExtraByte(extra, "byte_57", "value_57")）。
var forgingExtraKeys = []string{"byte_57", "value_57"}

// SeparateUpgradeResult 描述一次 op438 锻造的写库结果。
// Success=false 且 Rejected=true 表示校验类拒绝（未改动任何资产）；
// Success=true 且 Succeeded=false 表示掷点失败（材料已消耗、等级不变）。
type SeparateUpgradeResult struct {
	CharacterID string
	Success     bool
	Rejected    bool
	ErrorCode   byte
	// RejectionReason 只用于日志取证（同名拒绝对应哪一条 PVF/参考判据）。
	RejectionReason string

	TargetListType    byte
	TargetSlotIndex   int16
	TargetItemID      int64
	MaterialSlotIndex int16
	MaterialItemID    int64
	MaterialCost      int64
	// MaterialRemainingStackCount 是扣减之后材料槽那一行剩下的数量（0 = 已空）。
	MaterialRemainingStackCount int64

	OldLevel byte
	NewLevel byte
	// Succeeded 是掷点结果（false = 随机失败，等级按档位保持不变/掉级）。
	Succeeded bool
	// SuccessWeight 是本次真正参与掷点的权重（满值 10000），只用于日志。
	SuccessWeight int

	PortableFurnace bool
	TicketApplied   bool
	// LevelChanged 为真表示目标行的锻造等级真的变了（才会写库）。
	LevelChanged bool
	Changed      bool

	// TargetUpdatedStack / MaterialUpdatedStack 是发 op14 增量时用的行快照
	// （只对主背包 list 0 有意义；穿戴目标在装备聚合里，见 dnfbridge 的
	// PostActionRefreshSelectedEquipmentSlots 说明）。
	TargetUpdatedStack   dnfrepo.ItemStack
	MaterialUpdatedStack dnfrepo.ItemStack
	MaterialUpdated      bool
}

func newSeparateUpgradeResult(cmd Command) SeparateUpgradeResult {
	return SeparateUpgradeResult{
		Rejected:          true,
		ErrorCode:         forgingErrorInvalidTarget,
		TargetListType:    cmd.TargetListType,
		TargetSlotIndex:   cmd.TargetSlotIndex,
		MaterialSlotIndex: cmd.MaterialSlotIndex,
	}
}

// SeparateUpgrade 执行一次锻造。返回的 Result 里 Rejected/Succeeded/等级就是
// class1 438 回包里那几个字段的来源；只有事务提交成功才会走到这条出口。
func (o *Owner) SeparateUpgrade(ctx context.Context, cmd Command, resolver alignedcmd.ForgingResolver) (SeparateUpgradeResult, error) {
	base := newSeparateUpgradeResult(cmd)
	if o == nil || o.repo == nil {
		return SeparateUpgradeResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return SeparateUpgradeResult{}, ErrCharacterRequired
	}
	if resolver == nil {
		return SeparateUpgradeResult{}, ErrForgingResolverRequired
	}
	// 参考实现只接受 Main / Equipment 两个目标容器，且主背包里目标不能与材料同槽。
	if cmd.TargetListType != listTypeMain && cmd.TargetListType != listTypeEquipment {
		base.RejectionReason = "unsupported_target_list"
		return base, nil
	}
	if cmd.TargetSlotIndex < 0 || cmd.MaterialSlotIndex < 0 {
		base.RejectionReason = "negative_slot"
		return base, nil
	}

	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	base.CharacterID = characterID
	if !o.inItemTransaction {
		var result SeparateUpgradeResult
		err := o.withinInventoryTransaction(ctx, characterID, func(txOwner *Owner) error {
			var err error
			result, err = txOwner.SeparateUpgrade(ctx, cmd, resolver)
			return err
		})
		if err != nil {
			return SeparateUpgradeResult{}, err
		}
		if result.CharacterID == "" {
			result = base
			result.CharacterID = characterID
		}
		return result, nil
	}

	record, ok, err := o.repo.Load(ctx, characterID)
	if err != nil {
		return SeparateUpgradeResult{}, err
	}
	if !ok {
		return SeparateUpgradeResult{}, ErrInventoryNotFound
	}
	record = dnfrepo.CloneInventory(record)
	record.CharacterID = characterID
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	items := record.Slots
	result := base
	result.CharacterID = characterID

	// 材料槽恒在角色主背包（list 0）。账号共享槽 0:354..0:365 是**账号级**资产，
	// 不属于本事务，具名拒绝而不是当普通材料处理。
	if dnfrepo.IsAccountSharedInventorySlot(listTypeMain, cmd.MaterialSlotIndex) {
		result.ErrorCode = forgingErrorInvalidMaterial
		result.RejectionReason = "account_shared_material_slot"
		return result, nil
	}
	materialKey := slotKey(listTypeMain, cmd.MaterialSlotIndex)
	material, found := items[materialKey]
	if !found || material.Count < 1 {
		result.ErrorCode = forgingErrorInvalidMaterial
		result.RejectionReason = "material_slot_empty"
		return result, nil
	}
	result.MaterialItemID = material.ItemID

	// 目标：主背包 list 0 或穿戴聚合 list 3。两者都从**本角色自己的**权威记录读，
	// 目标归属因此由记录归属天然保证。
	var target dnfrepo.ItemStack
	var equipmentRecord dnfrepo.EquipmentRecord
	equipmentKey := ""
	targetFromEquipment := cmd.TargetListType == listTypeEquipment
	if targetFromEquipment {
		if o.equipment == nil {
			return SeparateUpgradeResult{}, ErrForgingEquipmentOwnerUnavailable
		}
		equipment, found, loadErr := o.equipment.Load(ctx, characterID)
		if loadErr != nil {
			return SeparateUpgradeResult{}, loadErr
		}
		if !found {
			result.RejectionReason = "equipment_record_missing"
			return result, nil
		}
		equipmentRecord = dnfrepo.CloneEquipment(equipment)
		equipmentRecord.CharacterID = characterID
		equipmentKey = strconv.FormatInt(int64(cmd.TargetSlotIndex), 10)
		entry, found := equipmentRecord.Entries[equipmentKey]
		if !found || entry.ItemID <= 0 {
			result.RejectionReason = "worn_target_absent"
			return result, nil
		}
		target = forgingStackFromEquipmentEntry(entry)
	} else {
		targetKey := slotKey(listTypeMain, cmd.TargetSlotIndex)
		if targetKey == materialKey {
			result.ErrorCode = forgingErrorInvalidMaterial
			result.RejectionReason = "target_equals_material"
			return result, nil
		}
		stack, found := items[targetKey]
		if !found || stack.Count <= 0 || !isUpgradeableEquipmentStack(stack) {
			result.RejectionReason = "main_target_not_equipment"
			return result, nil
		}
		target = stack
	}
	if target.ItemID <= 0 {
		result.RejectionReason = "target_item_missing"
		return result, nil
	}
	if int64(uint32(cmd.TargetItemTemplateID)) != target.ItemID {
		result.RejectionReason = "target_template_mismatch"
		return result, nil
	}
	if isEquipmentLocked(target) {
		result.RejectionReason = "target_asset_locked"
		return result, nil
	}
	result.TargetItemID = target.ItemID
	result.OldLevel = forgingLevelOf(target)
	result.NewLevel = result.OldLevel

	resolution, err := resolver(material.ItemID, target.ItemID)
	if err != nil {
		return SeparateUpgradeResult{}, err
	}
	// 解析器必须报告的就是这一次材料槽里的那一件；不一致 = 解析与资产脱节。
	if resolution.MaterialItemID != 0 && int64(resolution.MaterialItemID) != material.ItemID {
		result.ErrorCode = forgingErrorInvalidMaterial
		result.RejectionReason = "material_resolution_mismatch"
		return result, nil
	}
	if !resolution.TargetIsWeapon {
		result.ErrorCode = forgingErrorNotWeapon
		result.RejectionReason = "target_not_weapon"
		return result, nil
	}
	// 耗材自己声明的可用装备等级窗口（真实例：便携式锻造炉 10164035 = 1..90、
	// 10090052 = 1..85）。比的是目标装备的 [minimum level]（厂商口径），
	// 缺省（两端都没给）不判；判失败时按"限制"拒绝，材料/等级都不动。
	if !forgingConsumableAllowsTargetLevel(resolution) {
		result.ErrorCode = forgingErrorRestriction
		result.RejectionReason = "material_item_level_window"
		return result, nil
	}
	// 厂商口径：实例耐久必须等于 PVF 元数据耐久（满耐久）才能锻造。
	if !forgingDurabilitySatisfied(target, resolution.TargetDurability) {
		result.ErrorCode = forgingErrorDurability
		result.RejectionReason = "target_durability_not_full"
		return result, nil
	}

	plan, planErr, planReason := planForgingAttempt(resolution, target, material, result.OldLevel)
	if planReason != "" {
		result.ErrorCode = planErr
		result.RejectionReason = planReason
		return result, nil
	}
	result.SuccessWeight = plan.successWeight
	result.MaterialCost = plan.materialCost
	result.PortableFurnace = plan.portable
	result.TicketApplied = plan.ticket

	rolled := rand.Intn(forgingSuccessWeightScale)
	succeeded := rolled < plan.successWeight
	newLevel := plan.failureLevel
	if succeeded {
		newLevel = plan.successLevel
	}
	result.Succeeded = succeeded
	result.NewLevel = newLevel

	// 先算完再落库：材料扣减与目标等级写入在同一事务里一次提交。
	material = cloneStack(material)
	material.Count -= plan.materialCost
	result.MaterialRemainingStackCount = material.Count
	result.MaterialUpdated = true
	if material.Count <= 0 {
		result.MaterialRemainingStackCount = 0
		result.MaterialUpdatedStack = dnfrepo.ItemStack{}
		delete(items, materialKey)
	} else {
		updateStackRawAmount(&material)
		items[materialKey] = material
		result.MaterialUpdatedStack = material
	}

	result.TargetUpdatedStack = cloneStack(target)
	if newLevel != result.OldLevel {
		if targetFromEquipment {
			entry := equipmentRecord.Entries[equipmentKey]
			forgingSetLevelOnEquipmentEntry(&entry, newLevel)
			equipmentRecord.Entries[equipmentKey] = entry
			result.TargetUpdatedStack = forgingStackFromEquipmentEntry(entry)
		} else {
			target = cloneStack(target)
			forgingSetLevel(&target, newLevel)
			items[slotKey(listTypeMain, cmd.TargetSlotIndex)] = target
			result.TargetUpdatedStack = target
		}
		result.LevelChanged = true
	} else if !targetFromEquipment {
		result.TargetUpdatedStack = target
	}

	// 存储顺序：先装备表（若有），再背包表。两步都在同一个角色物品事务里，
	// 任何一步失败都不得留下"材料扣了、等级没写"的半截状态。
	if result.LevelChanged && targetFromEquipment {
		equipmentRecord.UpdatedAt = time.Now()
		if err := dnfrepo.SaveEquipmentFields(ctx, o.equipment, equipmentRecord, dnfrepo.EquipmentFieldEntries); err != nil {
			return SeparateUpgradeResult{}, err
		}
	}
	record.UpdatedAt = time.Now()
	if err := dnfrepo.SaveInventoryFields(ctx, o.repo, record, dnfrepo.InventoryFieldSlots); err != nil {
		return SeparateUpgradeResult{}, err
	}
	result.Success = true
	result.Rejected = false
	result.ErrorCode = 0
	result.Changed = true
	return result, nil
}

// forgingPlan 是一次已经通过校验的锻造尝试的掷点前参数。
type forgingPlan struct {
	successWeight int
	materialCost  int64
	portable      bool
	ticket        bool
	successLevel  byte
	failureLevel  byte
}

// planForgingAttempt 按材料身份选档并算出成功率/材料消耗/成功等级/失败等级。
// 返回的第三个值非空表示拒绝（校验类），它同时就是日志里的具名原因。
func planForgingAttempt(resolution alignedcmd.ForgingResolution, target dnfrepo.ItemStack, material dnfrepo.ItemStack, oldLevel byte) (forgingPlan, byte, string) {
	switch {
	case resolution.PortableFurnace:
		// 便携式锻造炉：真档物品说明写的是「使用后可以对装备进行 1 次锻造；
		// 锻造时不消耗强烈的气息」⇒ 只消耗炉子本身 1 个，成功率/等级上限照常走
		// [table]（参考实现没有 portable 锻造，这一档按物品说明实现）。
		if int(oldLevel) >= resolution.MaxLevel {
			return forgingPlan{}, forgingErrorMaxLevel, "forging_level_at_max"
		}
		level, ok := resolution.ForgingLevelFor(int(oldLevel) + 1)
		if !ok {
			return forgingPlan{}, forgingErrorUnsupported, "table_row_missing"
		}
		return forgingPlan{
			successWeight: level.SuccessWeight,
			materialCost:  1,
			portable:      true,
			successLevel:  oldLevel + 1,
			failureLevel:  oldLevel,
		}, 0, ""

	case resolution.ForgingTicketUsable():
		// 券档：材料槽那一件就是券，消耗 1 张。成功跳到券写的等级（additional =
		// 当前等级 + 增量）；失败只在券显式写了 FailureDecrementLevel 时才掉级。
		ticket := resolution.Ticket
		if len(resolution.TicketWhitelist) > 0 && !forgingWhitelistContains(resolution.TicketWhitelist, target.ItemID) {
			// 真档 125 张锻造券全部带 [reinforcement usable item list]（例如
			// +5 传承武器锻造券 490000622 的名单是 401000024 等 29 件）。
			return forgingPlan{}, forgingErrorInvalidTarget, "ticket_target_not_whitelisted"
		}
		targetLevel := ticket.TargetLevel
		if ticket.IsAdditional {
			maximumSourceLevel := resolution.MaxLevel - ticket.TargetLevel
			if ticket.ApplyValue >= 0 && ticket.ApplyValue < maximumSourceLevel {
				maximumSourceLevel = ticket.ApplyValue
			}
			if int(oldLevel) > maximumSourceLevel {
				return forgingPlan{}, forgingErrorMaxLevel, "ticket_source_level_too_high"
			}
			targetLevel = int(oldLevel) + ticket.TargetLevel
		}
		if targetLevel < 0 || targetLevel > 255 {
			return forgingPlan{}, forgingErrorUnsupported, "ticket_target_level_out_of_range"
		}
		failureLevel := oldLevel
		if ticket.FailureDecrementLevel > 0 {
			decremented := int(oldLevel) - ticket.FailureDecrementLevel
			if decremented < 0 {
				decremented = 0
			}
			failureLevel = byte(decremented)
		}
		return forgingPlan{
			successWeight: ticket.SuccessWeight,
			materialCost:  1,
			ticket:        true,
			successLevel:  byte(targetLevel),
			failureLevel:  failureLevel,
		}, 0, ""
	}

	// 普通锻造：材料表 + 稀有度权重 + 档位材料权重。
	if int(oldLevel) >= resolution.MaxLevel {
		return forgingPlan{}, forgingErrorMaxLevel, "forging_level_at_max"
	}
	level, ok := resolution.ForgingLevelFor(int(oldLevel) + 1)
	if !ok {
		return forgingPlan{}, forgingErrorUnsupported, "table_row_missing"
	}
	if resolution.TargetRarity < 0 || resolution.TargetRarity >= len(resolution.ItemWeightsByRarity) {
		return forgingPlan{}, forgingErrorUnsupported, "target_rarity_out_of_range"
	}
	rule, ok := resolution.MaterialsByGrade[resolution.TargetGrade]
	if !ok || rule.ItemTemplateID <= 0 || rule.BaseCount <= 0 {
		return forgingPlan{}, forgingErrorUnsupported, "grade_material_rule_missing"
	}
	rawCost := float64(rule.BaseCount) * resolution.ItemWeightsByRarity[resolution.TargetRarity] * level.MaterialWeight
	if math.IsNaN(rawCost) || math.IsInf(rawCost, 0) || rawCost > float64(math.MaxInt32) {
		return forgingPlan{}, forgingErrorUnsupported, "material_cost_not_representable"
	}
	// 厂商口径：max(1, (int)(BaseCount × rarityWeight × levelMaterialWeight + 1e-7))。
	cost := int64(rawCost + 1e-7)
	if cost < 1 {
		cost = 1
	}
	if material.ItemID != int64(rule.ItemTemplateID) || material.Count < cost {
		return forgingPlan{}, forgingErrorInvalidMaterial, "material_mismatch_or_insufficient"
	}
	return forgingPlan{
		successWeight: level.SuccessWeight,
		materialCost:  cost,
		successLevel:  oldLevel + 1,
		failureLevel:  oldLevel,
	}, 0, ""
}

// forgingWhitelistContains 判券的 [reinforcement usable item list] 里有没有这件目标。
func forgingWhitelistContains(whitelist []int64, itemID int64) bool {
	for _, candidate := range whitelist {
		if candidate == itemID {
			return true
		}
	}
	return false
}

// forgingConsumableAllowsTargetLevel 判材料槽那一件的 `[check usable itemlevel]`
// 窗口是否覆盖目标装备的 `[minimum level]`。
//
// 口径（厂商同族实现 ItemUpgradeRestriction.AllowsItemLevel：
// ItemLevelMin/Max 来自耗材的 [check usable itemlevel]，入参是 targetMetadata.MinimumLevel）：
//
//	窗口两端都没给（解析器给 -1，或字面量零值 0/0 -- 真档窗口下界恒为 1）
//	  ⇒ 这件耗材不限目标等级，直接放行（锻造券/幸运锻造券就是这种形状，真档实测
//	     125 张券与 3 张幸运券都没有这一段）；
//	只给了一端 ⇒ 只判给出来的那一端；
//	目标 PVF 没写 [minimum level]（解析成 0）而窗口写了 ⇒ 失败关闭（判不进去）。
func forgingConsumableAllowsTargetLevel(resolution alignedcmd.ForgingResolution) bool {
	min := resolution.MaterialUsableItemLevelMin
	max := resolution.MaterialUsableItemLevelMax
	if min == 0 && max == 0 {
		return true
	}
	level := resolution.TargetMinimumLevel
	if min > 0 && level < min {
		return false
	}
	if max > 0 && level > max {
		return false
	}
	return true
}

// forgingDurabilitySatisfied 判"实例耐久 == PVF 元数据耐久"（厂商 separate-upgrade
// 的满耐久门槛）。实例耐久优先读 Extra（durability / current_durability），
// 其次按行格式读 0x77 行 +0x0B / legacy 行 +10（dnfequip.RepairDurabilityWord 是
// 全仓唯一的读偏移判据）；读不到当前耐久（但写了耐久键）时失败关闭。
func forgingDurabilitySatisfied(stack dnfrepo.ItemStack, pvfDurability int64) bool {
	declared := false
	for _, key := range []string{"durability", "current_durability", "max_durability"} {
		if strings.TrimSpace(stack.Extra[key]) != "" {
			declared = true
			break
		}
	}
	current := firstExtraInt(stack.Extra, -1, "durability", "current_durability")
	if current < 0 {
		if declared {
			return false
		}
		word, ok := dnfequip.RepairDurabilityWord(stack.RawEntry, stack.ItemID)
		if !ok {
			return false
		}
		current = word
	}
	if pvfDurability <= 0 {
		// PVF 没写耐久 = 该装备没有耐久属性（厂商 ItemMetadataResolver 也只给
		// 武器/护甲耐久），此时只能要求实例耐久同样是 0。
		return current == 0
	}
	return current == pvfDurability
}

// forgingLevelOf 读锻造等级：Extra 的两个别名优先（投影与客户端行生成读的就是它们），
// 再回落到 0x77 行的 0x57。读不到就是 0（客户端那一格本来就是 0）。
func forgingLevelOf(stack dnfrepo.ItemStack) byte {
	for _, key := range forgingExtraKeys {
		raw := strings.TrimSpace(stack.Extra[key])
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 0, 64)
		if err != nil || value < 0 || value > 255 {
			continue
		}
		return byte(value)
	}
	if len(stack.RawEntry) > forgingRowOffset {
		return stack.RawEntry[forgingRowOffset]
	}
	return 0
}

// forgingSetLevel 写锻造等级：Extra 两个别名 + 0x77 行的 0x57（copy-on-write）。
// 与 services/gmtool 的 itemcfgSetForgingLevel 逐字同口径：只碰这一个字节，
// 0x58/0x59（解封等级/交易限制）不动。
func forgingSetLevel(stack *dnfrepo.ItemStack, level byte) {
	if stack == nil {
		return
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 2)
	}
	text := strconv.Itoa(int(level))
	for _, key := range forgingExtraKeys {
		stack.Extra[key] = text
	}
	if len(stack.RawEntry) == currentItemListEntrySize {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		stack.RawEntry[forgingRowOffset] = level
	}
}

// forgingSetLevelOnEquipmentEntry 把同一份落点写进穿戴行（list 3）：Extra 两个
// 别名 + 当前 0x77 行的 0x57。穿戴行不是 0x77 行时只写 Extra —— 投影读 Extra 优先，
// 且绝不把 legacy 摘要行当 0x77 行补零。
func forgingSetLevelOnEquipmentEntry(entry *dnfrepo.EquipmentEntry, level byte) {
	if entry == nil {
		return
	}
	if entry.Extra == nil {
		entry.Extra = make(map[string]string, 2)
	}
	text := strconv.Itoa(int(level))
	for _, key := range forgingExtraKeys {
		entry.Extra[key] = text
	}
	if len(entry.RawEntry) == currentItemListEntrySize && entry.ItemID > 0 &&
		entry.ItemID <= int64(^uint32(0)) {
		entry.RawEntry = append([]byte(nil), entry.RawEntry...)
		entry.RawEntry[forgingRowOffset] = level
	}
}

// forgingStackFromEquipmentEntry 把穿戴行投影成 owner 内部通用的 ItemStack 视图
// （身份/数量/耐久/Extra 都在里面；写入侧仍回到 EquipmentEntry）。
func forgingStackFromEquipmentEntry(entry dnfrepo.EquipmentEntry) dnfrepo.ItemStack {
	count := int64(1)
	if raw := strings.TrimSpace(entry.Extra["current_exe_inventory_count"]); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil && value >= 0 {
			count = value
		}
	}
	stack := dnfrepo.ItemStack{
		ItemID:   entry.ItemID,
		Count:    count,
		Bind:     entry.Bind,
		ExpireAt: entry.ExpireAt,
		RawEntry: entry.RawEntry,
		Extra:    entry.Extra,
	}
	return cloneStack(stack)
}

// forgingResultReason 把一次锻造结果压成可 grep 的一行（bridge 的 reason= 用它）。
func forgingResultReason(result SeparateUpgradeResult) string {
	base := fmt.Sprintf(
		"target=(%d,%d,item=%d) materialSlot=%d materialItem=%d cost=%d left=%d level=%d->%d roll=%t weight=%d portable=%t ticket=%t",
		result.TargetListType,
		result.TargetSlotIndex,
		result.TargetItemID,
		result.MaterialSlotIndex,
		result.MaterialItemID,
		result.MaterialCost,
		result.MaterialRemainingStackCount,
		result.OldLevel,
		result.NewLevel,
		result.Succeeded,
		result.SuccessWeight,
		result.PortableFurnace,
		result.TicketApplied,
	)
	if result.Rejected && result.RejectionReason != "" {
		return base + " rejected=" + result.RejectionReason
	}
	return base
}
