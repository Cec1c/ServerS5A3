package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件是 op438「锻造 / 别升级（separate upgrade）」的**运行时 PVF 真源**：
// bridge 只做解析，不做任何仓储写入、也不决定掷点。
//
// 表（真档 dump 见 Docs/D33_FORGING_20261014.md 第 2 节）：
//
//	etc/upgrade_separate.etc
//	  [separate upgrade max]        锻造等级/次数上限（真档 8）
//	  [table]                       10 档 × 4 个数：档位、?、成功率（百分数×100）、材料权重
//	  [item weights by grade]       99 组三元组（装备 [grade]、材料模板 3326、基础数量）
//	  [item weights by rarity]      7 个稀有度权重（0.40 0.70 1 1.25 1.40 1.10 1.30）
//
// 材料槽那一件 stackable 的三种身份（逐字比对记号，不做前缀匹配）：
//
//	[action type] `[portable genuine damage upgrade upgrede]`   便携式锻造炉（真档 103 件）
//	[equipment separate reinforcement ticket]                        锻造券（真档 125 件）
//	[action type] `[lucky enchant deed]` 2                      幸运锻造券（10007408）
//
// 券的目标白名单是券自己 document 里的 `[reinforcement usable item list]`。
// 目标装备侧只需要 `[equipment type]`（必须 weapon）/ `[grade]` /
// `[rarity]` / `[durability]`。
const (
	// currentForgingSeparateUpgradePVFPath 与 gmtool 的 itemcfgSeparateUpgradePVFPath
	// 和厂商 SeparateUpgradeTableProvider.cs:11 逐字相同。
	currentForgingSeparateUpgradePVFPath = "etc/upgrade_separate.etc"

	currentForgingPortableActionToken = "[portable genuine damage upgrade upgrede]"
	currentForgingTicketSection       = "equipment separate reinforcement ticket"
	currentForgingWhitelistSection    = "reinforcement usable item list"
	// currentForgingUsableItemLevelSection 是耗材的可用等级窗口段。实测（本仓真档）：便携式
	// 锻造炉 10164035 = 1..90、10090052 = 1..85；同族 1阶..5阶强化器的窗口是五段
	// 1..29 / 30..39 / 40..49 / 50..59 / 60..69，与物品名的 1阶..5阶 一一对应；
	// 凯丽的强化器 (Lv1~90) / 一次性增幅器 (Lv1~90) 这两个**物品名**里也逐字写着同一个窗口。
	//
	// 窗口比的是哪一个等级：厂商同族实现给的是**目标装备的** [minimum level]
	// （ItemUpgradeRestriction.AllowsItemLevel 的入参 context.EquipmentLevel 就是
	// targetMetadata.MinimumLevel，不是 [grade]、也不是角色等级），本仓按同一口径执行。
	// 该选择是有区别的：真档 101040493 的 [minimum level] 是 90、[grade] 是 96，
	// 在 1..90 的窗口下前者放行、后者拒绝（本仓的真档用例按 minimum-level 口径断言）。
	// 客户端原生行为（UI 是否也按同一端拦截）仍待主代理验收，见
	// Docs/D33_FORGING_20261014.md 第 5 节。
	currentForgingUsableItemLevelSection = "check usable itemlevel"

	currentForgingLuckyEnchantDeedAction   = "[lucky enchant deed]"
	currentForgingLuckyRefineParam         = 2
	currentForgingLuckyRefineTargetLevel   = 1
	currentForgingLuckyRefineSuccessWeight = 5000

	// currentForgingSuccessWeightMax 是 [table] 成功率的满值（掷点 rand(10000)）。
	currentForgingSuccessWeightMax = 10000
)

// currentForgingTable 是 etc/upgrade_separate.etc 解析结果（一次请求只读一次）。
type currentForgingTable struct {
	maxLevel         int
	levels           []alignedcmd.ForgingLevelRow
	materialsByGrade map[int]alignedcmd.ForgingMaterialRule
	rarityWeights    []float64
}

// alignedForgingResolverForCommand 只在 op438 上给出解析器；其它命令连 PVF 目录都不开。
// 解析失败（读不到/结构不合法）时返回错误 ⇒ bridge 记事件并把 ForgingResolver 留空，
// 领域层随后按"不支持"回失败体，绝不猜表。
func (s *Service) alignedForgingResolverForCommand(opcode dnfenum.CmdPacket) (alignedcmd.ForgingResolver, error) {
	if opcode != dnfenum.CmdPacketUpgradeItemSeparate {
		return nil, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errDungeonDropSourceRequired
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	table, err := loadCurrentForgingTable(source)
	if err != nil {
		return nil, err
	}
	s.logPacketEvent("dnf-forging-table-loaded",
		"path", currentForgingSeparateUpgradePVFPath,
		"max_level", table.maxLevel,
		"levels", len(table.levels),
		"grades", len(table.materialsByGrade),
		"rarity_weights", len(table.rarityWeights))
	return currentForgingResolver(catalog, source, table), nil
}

// loadCurrentForgingTable 读并校验 etc/upgrade_separate.etc。
func loadCurrentForgingTable(source dnfpvf.Source) (currentForgingTable, error) {
	if source == nil {
		return currentForgingTable{}, errDungeonDropSourceRequired
	}
	text, err := source.ReadText(currentForgingSeparateUpgradePVFPath)
	if err != nil {
		return currentForgingTable{}, fmt.Errorf("read %s: %w", currentForgingSeparateUpgradePVFPath, err)
	}
	document, err := dnfpvf.Parse(currentForgingSeparateUpgradePVFPath, text)
	if err != nil {
		return currentForgingTable{}, fmt.Errorf("parse %s: %w", currentForgingSeparateUpgradePVFPath, err)
	}
	return parseCurrentForgingTable(document)
}

// parseCurrentForgingTable 是纯解析 + 结构校验。校验口径照厂商
// SeparateUpgradeTableProvider.cs:12-25（上限 1..255、档数 >= 上限、材料组非空且
// 模板/数量为正、稀有度权重非空且为正），任何一条不满足都明确失败。
func parseCurrentForgingTable(document *dnfpvf.Document) (currentForgingTable, error) {
	table := currentForgingTable{materialsByGrade: make(map[int]alignedcmd.ForgingMaterialRule)}
	if document == nil {
		return table, errors.New("separate upgrade document is nil")
	}
	numbers := document.Numbers("table")
	if len(numbers) == 0 || len(numbers)%4 != 0 {
		return table, fmt.Errorf("%s: [table] has %d numbers", currentForgingSeparateUpgradePVFPath, len(numbers))
	}
	for index := 0; index+3 < len(numbers); index += 4 {
		table.levels = append(table.levels, alignedcmd.ForgingLevelRow{
			TargetLevel:    len(table.levels) + 1,
			SuccessWeight:  int(math.Round(numbers[index+2])),
			MaterialWeight: numbers[index+3],
		})
	}
	if maxLevel, found := document.Int("separate upgrade max"); found {
		table.maxLevel = int(maxLevel)
	}
	grades := document.Ints("item weights by grade")
	if len(grades) == 0 || len(grades)%3 != 0 {
		return table, fmt.Errorf("%s: [item weights by grade] has %d numbers", currentForgingSeparateUpgradePVFPath, len(grades))
	}
	for index := 0; index+2 < len(grades); index += 3 {
		grade := int(grades[index])
		if _, duplicate := table.materialsByGrade[grade]; duplicate {
			return table, fmt.Errorf("%s: duplicate material grade %d", currentForgingSeparateUpgradePVFPath, grade)
		}
		table.materialsByGrade[grade] = alignedcmd.ForgingMaterialRule{
			ItemTemplateID: int(grades[index+1]),
			BaseCount:      int(grades[index+2]),
		}
	}
	table.rarityWeights = document.Numbers("item weights by rarity")

	if table.maxLevel <= 0 || table.maxLevel > 255 {
		return table, fmt.Errorf("%s: [separate upgrade max] = %d is outside 1..255", currentForgingSeparateUpgradePVFPath, table.maxLevel)
	}
	if len(table.levels) < table.maxLevel {
		return table, fmt.Errorf("%s: [table] has %d levels, less than max %d", currentForgingSeparateUpgradePVFPath, len(table.levels), table.maxLevel)
	}
	for _, level := range table.levels[:table.maxLevel] {
		if level.SuccessWeight < 0 || level.SuccessWeight > currentForgingSuccessWeightMax {
			return table, fmt.Errorf("%s: level %d success weight %d", currentForgingSeparateUpgradePVFPath, level.TargetLevel, level.SuccessWeight)
		}
		if level.MaterialWeight <= 0 || math.IsNaN(level.MaterialWeight) || math.IsInf(level.MaterialWeight, 0) {
			return table, fmt.Errorf("%s: level %d material weight %v", currentForgingSeparateUpgradePVFPath, level.TargetLevel, level.MaterialWeight)
		}
	}
	for grade, rule := range table.materialsByGrade {
		if grade < 0 || rule.ItemTemplateID <= 0 || rule.BaseCount <= 0 {
			return table, fmt.Errorf("%s: grade %d material rule %+v", currentForgingSeparateUpgradePVFPath, grade, rule)
		}
	}
	if len(table.rarityWeights) == 0 {
		return table, fmt.Errorf("%s: [item weights by rarity] is empty", currentForgingSeparateUpgradePVFPath)
	}
	for _, weight := range table.rarityWeights {
		if weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
			return table, fmt.Errorf("%s: rarity weight %v", currentForgingSeparateUpgradePVFPath, weight)
		}
	}
	return table, nil
}

// currentForgingResolver 用一次请求就解析好的表 + 运行时物品目录，把
// （材料模板, 目标模板）翻成领域层需要的全部 PVF 事实。
//
// 逐件解析失败（模板不在目录里 / 不是那一类）不返回错误，而是保持对应字段为零值：
// 领域层据此按"材料不合法 / 目标不是武器"等既有错误码拒绝，并留下具名原因。
func currentForgingResolver(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, table currentForgingTable) alignedcmd.ForgingResolver {
	return func(materialItemID int64, targetItemID int64) (alignedcmd.ForgingResolution, error) {
		if catalog == nil || source == nil {
			return alignedcmd.ForgingResolution{}, errDungeonDropSourceRequired
		}
		resolution := alignedcmd.ForgingResolution{
			TablePVFPath:        currentForgingSeparateUpgradePVFPath,
			MaxLevel:            table.maxLevel,
			Levels:              append([]alignedcmd.ForgingLevelRow(nil), table.levels...),
			MaterialsByGrade:    make(map[int]alignedcmd.ForgingMaterialRule, len(table.materialsByGrade)),
			ItemWeightsByRarity: append([]float64(nil), table.rarityWeights...),
		}
		for grade, rule := range table.materialsByGrade {
			resolution.MaterialsByGrade[grade] = rule
		}

		if materialItemID > 0 && materialItemID <= int64(^uint32(0)) {
			definition, err := catalog.ResolveItem(uint32(materialItemID))
			switch {
			case err == nil && definition.Kind == dungeonDropItemStackable:
				resolution.MaterialItemID = int(materialItemID)
				resolution.MaterialPVFPath = definition.PVFPath
				document, docErr := parseDungeonCardPVFDocument(source, definition.PVFPath)
				if docErr != nil {
					return alignedcmd.ForgingResolution{}, fmt.Errorf("parse forging material item=%d path=%s: %w", materialItemID, definition.PVFPath, docErr)
				}
				classifyCurrentForgingMaterial(document, definition.PVFPath, &resolution)
			case err == nil:
				// 不是 stackable ⇒ 材料槽那一件永远进不了 portable/券档。
			case errors.Is(err, errDungeonDropItemUnresolved):
				// 目录里没有这一件：保持零值，领域层按材料不合法拒绝。
			default:
				return alignedcmd.ForgingResolution{}, fmt.Errorf("resolve forging material item=%d: %w", materialItemID, err)
			}
		}

		if targetItemID > 0 && targetItemID <= int64(^uint32(0)) {
			definition, err := catalog.ResolveItem(uint32(targetItemID))
			switch {
			case err == nil && definition.Kind == dungeonDropItemEquipment:
				resolution.TargetPVFPath = definition.PVFPath
				document, docErr := parseDungeonCardPVFDocument(source, definition.PVFPath)
				if docErr != nil {
					return alignedcmd.ForgingResolution{}, fmt.Errorf("parse forging target item=%d path=%s: %w", targetItemID, definition.PVFPath, docErr)
				}
				resolution.TargetIsWeapon = currentForgingEquipmentIsWeapon(document)
				if grade, found := document.Int("grade"); found {
					resolution.TargetGrade = int(grade)
				}
				if rarity, found := document.Int("rarity"); found {
					resolution.TargetRarity = int(rarity)
				}
				// 目标装备的 `[minimum level]`：材料槽那一件的
				// `[check usable itemlevel]` 窗口比的就是它（厂商
				// ItemUpgradeRestriction.AllowsItemLevel(context.EquipmentLevel)，
				// context.EquipmentLevel = targetMetadata.MinimumLevel）。
				if minimum, found := document.Int("minimum level"); found {
					resolution.TargetMinimumLevel = int(minimum)
				}
				resolution.TargetDurability = int64(definition.Durability)
			case err == nil:
				// 不是装备 ⇒ 目标不是武器。
			case errors.Is(err, errDungeonDropItemUnresolved):
				// 目录里没有这一件：目标不是武器。
			default:
				return alignedcmd.ForgingResolution{}, fmt.Errorf("resolve forging target item=%d: %w", targetItemID, err)
			}
		}
		return resolution, nil
	}
}

// classifyCurrentForgingMaterial 认材料槽那一件的 portable / 券身份与白名单。
// 记号逐字比对（trim 反引号后 EqualFold），绝不前缀匹配。
func classifyCurrentForgingMaterial(document *dnfpvf.Document, path string, resolution *alignedcmd.ForgingResolution) {
	action := ""
	if text, found := document.Text("action type"); found {
		action = strings.Trim(strings.TrimSpace(text), "`")
	}
	if strings.EqualFold(action, currentForgingPortableActionToken) {
		resolution.PortableFurnace = true
	}
	// 券的真源优先 `[equipment separate reinforcement ticket]`（厂商
	// SeparateUpgradeTicketDefinition.TryParse）；这一段缺席时才是幸运锻造券。
	if _, found := document.Section(currentForgingTicketSection); found {
		values := document.Ints(currentForgingTicketSection)
		modes := document.Texts(currentForgingTicketSection)
		applyMode := ""
		if len(modes) > 0 {
			applyMode = strings.ToLower(strings.Trim(strings.TrimSpace(modes[len(modes)-1]), "`"))
		}
		targetLevel := 0
		successPercent := 0
		if len(values) > 0 {
			targetLevel = int(values[0])
		}
		if len(values) > 1 {
			successPercent = int(values[1])
		}
		applyValue := -1
		if len(values) > 2 {
			applyValue = int(values[2])
		}
		isFixed := applyMode == "fixed"
		isAdditional := applyMode == "additional"
		if targetLevel > 0 && targetLevel <= 255 && successPercent >= 0 && successPercent <= 100 && (isFixed || isAdditional) {
			resolution.Ticket = alignedcmd.ForgingTicketDefinition{
				TargetLevel:   targetLevel,
				SuccessWeight: successPercent * 100,
				IsFixed:       isFixed,
				IsAdditional:  isAdditional,
				ApplyValue:    applyValue,
				PVFPath:       path,
			}
		}
	} else if strings.EqualFold(action, currentForgingLuckyEnchantDeedAction) {
		params := document.Ints("action type")
		if len(params) > 0 && params[0] == currentForgingLuckyRefineParam {
			resolution.Ticket = alignedcmd.ForgingTicketDefinition{
				TargetLevel:   currentForgingLuckyRefineTargetLevel,
				SuccessWeight: currentForgingLuckyRefineSuccessWeight,
				IsAdditional:  true,
				ApplyValue:    -1,
				PVFPath:       path,
			}
		}
	}
	if whitelist := document.Ints(currentForgingWhitelistSection); len(whitelist) > 0 {
		resolution.TicketWhitelist = append([]int64(nil), whitelist...)
	}
	// `[check usable itemlevel]`：这件耗材只允许用在"装备等级"（装备 PVF 的
	// `[minimum level]`）落在窗口内的目标上。真档便携式锻造炉带这一段
	// （10164035 = 1..90、10090052 = 1..85），锻造券/幸运锻造券都不带。
	window := document.Ints(currentForgingUsableItemLevelSection)
	resolution.MaterialUsableItemLevelMin = -1
	resolution.MaterialUsableItemLevelMax = -1
	if len(window) > 0 {
		resolution.MaterialUsableItemLevelMin = int(window[0])
	}
	if len(window) > 1 {
		resolution.MaterialUsableItemLevelMax = int(window[1])
	}
}

// currentForgingEquipmentIsWeapon 判目标装备的 `[equipment type]` 是不是武器。
// 运行时 PVF 的 token 是 `[weapon]`（真档 101040493.equ = `[weapon]` 25），
// 与 gmtool itemcfgIsForgeableWeapon 归一化后的 weapon 同判据。
func currentForgingEquipmentIsWeapon(document *dnfpvf.Document) bool {
	if document == nil {
		return false
	}
	text, found := document.Text("equipment type")
	if !found {
		return false
	}
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(text), "` []"))
	return normalized == "weapon"
}
