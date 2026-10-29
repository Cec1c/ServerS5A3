package dnfbridge

import (
	cryptorand "crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const currentDisjointIndividualCustomInfoPath = "etc/customability/individualcustominfo.etc"

// 返回的规则只读；随机产物和物品校验仍在每次操作中计算。
func (c *pvfDungeonDropCatalog) currentDisjointPVFConfig() (currentDisjointPVFConfig, error) {
	if c == nil {
		return currentDisjointPVFConfig{}, errCurrentDisjointUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disjointConfig != nil {
		return *c.disjointConfig, nil
	}
	config, err := loadCurrentDisjointPVFConfig(c.source)
	if err == nil {
		c.disjointConfig = &config
	}
	return config, err
}

func loadCurrentDisjointPVFConfig(source dnfpvf.Source) (currentDisjointPVFConfig, error) {
	if source == nil {
		return currentDisjointPVFConfig{}, errCurrentDisjointUnavailable
	}
	disjoint, err := parseDungeonCardPVFDocument(source, "etc/disjoint.etc")
	if err != nil {
		return currentDisjointPVFConfig{}, err
	}
	config := currentDisjointPVFConfig{CubeBase: 150, AvatarByJob: make(map[string][]uint32), EmblemBoosters: make(map[[2]int]uint32)}
	for _, section := range disjoint.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "cube index") {
			continue
		}
		tokens := currentDisjointSectionTokens(disjoint, section)
		if len(tokens) == 0 || !strings.EqualFold(strings.TrimSpace(strings.Trim(tokens[0].Value, "`")), "[no element]") {
			continue
		}
		for _, token := range tokens[1:] {
			if token.Kind == dnfpvf.TokenInt && token.Int > 0 && token.Int <= math.MaxUint32 {
				config.CubeItemID = uint32(token.Int)
				break
			}
		}
	}
	values := disjoint.Numbers("cube creation const")
	if len(values) > 0 && values[0] > 0 {
		config.CubeBase = values[0]
		config.CubeMultipliers = append(config.CubeMultipliers, values[1:]...)
	}
	config.Additional = currentDisjointParseAdditional(disjoint.Ints("additional result"))
	config.AdditionalConsts = currentDisjointParseAdditionalConsts(disjoint.Numbers("additional result const"), len(config.Additional))
	expandItems := disjoint.Ints("additional result expand")
	expandValues := disjoint.Numbers("additional result expand const")
	config.Expands = currentDisjointParseExpands(expandItems, expandValues)
	if (len(expandItems) != 0 || len(expandValues) != 0) && config.Expands == nil {
		return currentDisjointPVFConfig{}, errCurrentDisjointRewardInvalid
	}
	config.AvatarByJob = currentDisjointParseAvatarByJob(disjoint)
	config.AvatarEmblemTables = currentDisjointLoadAvatarEmblemTables(source)
	if customInfoText, readErr := source.ReadText(currentDisjointIndividualCustomInfoPath); readErr == nil {
		customInfo, parseErr := dnfpvf.Parse(currentDisjointIndividualCustomInfoPath, customInfoText)
		if parseErr != nil {
			return currentDisjointPVFConfig{}, fmt.Errorf("parse %s: %w", currentDisjointIndividualCustomInfoPath, parseErr)
		}
		disabled, parseErr := currentDisjointParseDisabledByCustomAbility(customInfo)
		if parseErr != nil {
			return currentDisjointPVFConfig{}, parseErr
		}
		config.CustomAbilityDisableLoaded = true
		config.DisabledByCustomAbilityType = disabled
	}
	// A missing optional table keeps ordinary equipment usable, but leaves the
	// explicit loaded=false state so any custom-ability source fails closed in
	// currentDisjointFilterDisabledRewards.

	emblem, err := parseDungeonCardPVFDocument(source, "etc/emblemcompound.etc")
	if err != nil {
		return currentDisjointPVFConfig{}, err
	}
	config.EmblemBoosters = currentDisjointParseEmblemBoosters(emblem.Ints("emblem compound info"))
	if config.CubeItemID == 0 || len(config.CubeMultipliers) == 0 || len(config.AvatarByJob) == 0 || len(config.EmblemBoosters) == 0 {
		return currentDisjointPVFConfig{}, errCurrentDisjointRewardInvalid
	}
	return config, nil
}

// currentDisjointParseDisabledByCustomAbility reads the custom-ability-family
// contribution rules used by etc/disjoint.etc. Keep the parsed table local to
// the immutable disjoint configuration so ordinary legendary equipment cannot
// inherit Aiken-specific suppression.
func currentDisjointParseDisabledByCustomAbility(document *dnfpvf.Document) (map[string]map[uint32]struct{}, error) {
	if document == nil {
		return nil, fmt.Errorf("%w: missing %s", errCurrentDisjointRewardInvalid, currentDisjointIndividualCustomInfoPath)
	}
	var customAbilityType string
	typeSections := 0
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "custom ability type") {
			continue
		}
		typeSections++
		if section.Start < 0 || section.End > len(document.Tokens) || section.End-section.Start != 1 {
			return nil, fmt.Errorf("%w: malformed custom ability type", errCurrentDisjointRewardInvalid)
		}
		token := document.Tokens[section.Start]
		if token.Kind != dnfpvf.TokenString && token.Kind != dnfpvf.TokenIdent {
			return nil, fmt.Errorf("%w: custom ability type is not text", errCurrentDisjointRewardInvalid)
		}
		customAbilityType = currentDisjointCustomAbilityTypeKey(token.Value)
	}
	if typeSections != 1 || customAbilityType == "" {
		return nil, fmt.Errorf("%w: want one non-empty custom ability type", errCurrentDisjointRewardInvalid)
	}
	disabled := make(map[uint32]struct{})
	disableSections := 0
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "disjoint disable item") {
			continue
		}
		disableSections++
		if section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
			return nil, fmt.Errorf("%w: malformed disjoint disable item", errCurrentDisjointRewardInvalid)
		}
		for _, token := range document.Tokens[section.Start:section.End] {
			if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > math.MaxUint32 {
				return nil, fmt.Errorf("%w: invalid disjoint disable item", errCurrentDisjointRewardInvalid)
			}
			itemID := uint32(token.Int)
			if _, duplicate := disabled[itemID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate disjoint disable item=%d", errCurrentDisjointRewardInvalid, itemID)
			}
			disabled[itemID] = struct{}{}
		}
	}
	if disableSections > 1 {
		return nil, fmt.Errorf("%w: duplicate disjoint disable item sections", errCurrentDisjointRewardInvalid)
	}
	if disableSections == 0 {
		return map[string]map[uint32]struct{}{}, nil
	}
	return map[string]map[uint32]struct{}{customAbilityType: disabled}, nil
}

func currentDisjointCustomAbilityTypeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(value, "`")))
}

func currentDisjointDocumentCustomAbilityType(document *dnfpvf.Document) (string, error) {
	if document == nil {
		return "", errCurrentDisjointRewardInvalid
	}
	var result string
	sections := 0
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "custom ability type") {
			continue
		}
		sections++
		if section.Start < 0 || section.End > len(document.Tokens) || section.End-section.Start != 1 {
			return "", fmt.Errorf("%w: malformed source custom ability type", errCurrentDisjointRewardInvalid)
		}
		token := document.Tokens[section.Start]
		if token.Kind != dnfpvf.TokenString && token.Kind != dnfpvf.TokenIdent {
			return "", fmt.Errorf("%w: source custom ability type is not text", errCurrentDisjointRewardInvalid)
		}
		result = currentDisjointCustomAbilityTypeKey(token.Value)
	}
	if sections > 1 || (sections == 1 && result == "") {
		return "", fmt.Errorf("%w: malformed source custom ability type", errCurrentDisjointRewardInvalid)
	}
	return result, nil
}

func currentDisjointFilterDisabledRewards(config currentDisjointPVFConfig, document *dnfpvf.Document, rewards []currentDisjointReward) ([]currentDisjointReward, error) {
	customAbilityType, err := currentDisjointDocumentCustomAbilityType(document)
	if err != nil {
		return nil, err
	}
	if customAbilityType == "" {
		return rewards, nil
	}
	if !config.CustomAbilityDisableLoaded {
		return nil, fmt.Errorf("%w: custom ability rules were not loaded for %q", errCurrentDisjointRewardInvalid, customAbilityType)
	}
	disabled, configured := config.DisabledByCustomAbilityType[customAbilityType]
	if !configured {
		if customAbilityType == "warriors dungeon" {
			return nil, fmt.Errorf("%w: missing disable table for custom ability type %q", errCurrentDisjointRewardInvalid, customAbilityType)
		}
		return rewards, nil
	}
	filtered := make([]currentDisjointReward, 0, len(rewards))
	for _, reward := range rewards {
		if _, blocked := disabled[reward.ItemID]; blocked {
			continue
		}
		filtered = append(filtered, reward)
	}
	return filtered, nil
}

func currentDisjointSectionTokens(document *dnfpvf.Document, section dnfpvf.Section) []dnfpvf.Token {
	if document == nil || section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
		return nil
	}
	return document.Tokens[section.Start:section.End]
}

func currentDisjointParseAdditional(values []int64) [][]uint32 {
	out := make([][]uint32, 0)
	for offset := 0; offset < len(values); {
		count := int(values[offset])
		offset++
		if count < 0 || offset+count > len(values) {
			return nil
		}
		row := make([]uint32, 0, count)
		for _, value := range values[offset : offset+count] {
			if value > 0 && value <= math.MaxUint32 {
				row = append(row, uint32(value))
			}
		}
		offset += count
		out = append(out, row)
	}
	return out
}

func currentDisjointParseAdditionalConsts(values []float64, rows int) []currentDisjointAdditionalConst {
	out := make([]currentDisjointAdditionalConst, 0, rows)
	offset := 0
	for row := 0; row < rows && offset < len(values); row++ {
		remaining := rows - row - 1
		if len(values)-offset == 1+remaining*4 {
			out = append(out, currentDisjointAdditionalConst{GreatDivisor: values[offset]})
			offset++
			continue
		}
		if offset+3 >= len(values) {
			return nil
		}
		out = append(out, currentDisjointAdditionalConst{GreatDivisor: values[offset], NormalDivisor: values[offset+1], GreatChance: values[offset+2]})
		offset += 4
	}
	return out
}

// Each rarity is a counted item row, including a single zero for disabled
// rows. Constants likewise retain one placeholder for a disabled row and
// three values for an enabled row. Never shift legendary onto chronicle's
// disabled row or silently accept a partial reward table.
func currentDisjointParseExpands(ids []int64, values []float64) []currentDisjointExpand {
	var out []currentDisjointExpand
	for offset := 0; offset < len(ids); {
		count := ids[offset]
		offset++
		row := currentDisjointExpand{}
		switch count {
		case 0:
		case 1:
			if offset >= len(ids) || ids[offset] <= 0 || ids[offset] > math.MaxUint32 {
				return nil
			}
			row.Enabled, row.ItemID = true, uint32(ids[offset])
			offset++
		default:
			return nil
		}
		out = append(out, row)
	}
	offset := 0
	for index := range out {
		width := 1
		if out[index].Enabled {
			width = 3
		}
		if len(values)-offset < width {
			return nil
		}
		for _, value := range values[offset : offset+width] {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return nil
			}
		}
		if out[index].Enabled {
			out[index].LevelDivisor, out[index].GreatChance, out[index].NormalChance = values[offset], values[offset+1], values[offset+2]
		}
		offset += width
	}
	if offset != len(values) {
		return nil
	}
	return out
}

// currentDisjointLoadAvatarEmblemTables reads the per-grade avatar disjoint
// reward tables etc/avatardisjoint/emblemlistinfo_<grade>.etc until the first
// missing or unusable file. Missing tables are not an error here; only the
// avatar disjoint reward path requires them.
func currentDisjointLoadAvatarEmblemTables(source dnfpvf.Source) []currentAvatarEmblemGradeTable {
	tables := make([]currentAvatarEmblemGradeTable, 0, 6)
	for grade := 0; grade < 32; grade++ {
		document, err := parseDungeonCardPVFDocument(source, "etc/avatardisjoint/emblemlistinfo_"+strconv.Itoa(grade)+".etc")
		if err != nil {
			break
		}
		table := currentDisjointParseAvatarEmblemTable(document)
		if len(table.Pools) == 0 {
			break
		}
		tables = append(tables, table)
	}
	return tables
}

func currentDisjointParseAvatarEmblemTable(document *dnfpvf.Document) currentAvatarEmblemGradeTable {
	table := currentAvatarEmblemGradeTable{}
	if document == nil {
		return table
	}
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "result info") {
			continue
		}
		values := currentDisjointTokensInts(currentDisjointSectionTokens(document, section))
		if len(values) < 5 || values[0] <= 0 {
			continue
		}
		pool := currentAvatarEmblemPool{PickCount: int(values[0])}
		for offset := 1; offset+3 < len(values); offset += 4 {
			item, weight, count := values[offset], values[offset+1], values[offset+2]
			if item <= 0 || item > math.MaxUint32 || weight <= 0 || weight > math.MaxUint32 || count <= 0 || count > math.MaxUint32 {
				continue
			}
			pool.Rewards = append(pool.Rewards, currentAvatarEmblemReward{
				ItemID:  uint32(item),
				Weight:  uint32(weight),
				Count:   uint32(count),
				Special: values[offset+3] != 0,
			})
		}
		if len(pool.Rewards) > 0 {
			table.Pools = append(table.Pools, pool)
		}
	}
	return table
}

func currentDisjointParseAvatarByJob(document *dnfpvf.Document) map[string][]uint32 {
	out := make(map[string][]uint32)
	active := false
	for _, section := range document.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		switch name {
		case "avatar disjoint info":
			active = true
			continue
		case "/avatar disjoint info":
			active = false
			continue
		}
		if !active || strings.HasPrefix(name, "/") {
			continue
		}
		values := make([]uint32, 0, 15)
		for _, token := range currentDisjointSectionTokens(document, section) {
			if token.Kind == dnfpvf.TokenInt && token.Int > 0 && token.Int <= math.MaxUint32 {
				values = append(values, uint32(token.Int))
			}
		}
		if len(values) >= 15 {
			out[currentDisjointNormalizeJob(name)] = append([]uint32(nil), values[:15]...)
		}
	}
	return out
}

func currentDisjointParseEmblemBoosters(values []int64) map[[2]int]uint32 {
	out := make(map[[2]int]uint32)
	for offset := 0; offset+1 < len(values); {
		grade, maxCount := int(values[offset]), int(values[offset+1])
		offset += 2
		if grade <= 0 || maxCount < 2 || offset+maxCount-1 > len(values) {
			return map[[2]int]uint32{}
		}
		for inputCount := 2; inputCount <= maxCount; inputCount++ {
			value := values[offset]
			offset++
			if value > 0 && value <= math.MaxUint32 {
				out[[2]int{grade, inputCount}] = uint32(value)
			}
		}
	}
	return out
}

func currentEquipmentDisjointRewards(config currentDisjointPVFConfig, document *dnfpvf.Document) ([]currentDisjointReward, error) {
	if document == nil || config.CubeItemID == 0 || config.CubeBase <= 0 {
		return nil, errCurrentDisjointRewardInvalid
	}
	// 86JP PvfLib EquipmentFile.Rarity 的默认值是 -1：运行时 PVF 里
	// equipment/character/common/etc/100300084..100300089 这 6 个装备文档
	// （`[name] `黑色小晶块效果`` 等）既没有 [rarity] 也没有 [value]/[price]，
	// 参考实现照样把它们分解成至少一个无色小晶块，只有 `[rarity]` 真的越界时才
	// 按下面的索引回退走第 0 行。旧代码把"没有 [rarity]"直接判成来源非法，客户端
	// 于是把这几件显示成"目前无法分解"。
	rarity, rarityFound := document.Int("rarity")
	if !rarityFound {
		rarity = -1
	}
	// 86JP DisjointFile.GetCubeCreationMultiplier 对超出 [cube creation const]
	// 表长的 rarity 回退到索引 0，而不是让整次分解失败；客户端的同名计算也是
	// 这个回退，服务端必须与它一致，否则一件异常 rarity 的装备就会把整批分解
	// 打成失败。
	multiplierIndex := currentDisjointRarityRowIndex(rarity)
	if multiplierIndex >= len(config.CubeMultipliers) {
		multiplierIndex = 0
	}
	minimumLevel, levelFound := document.Int("minimum level")
	if !levelFound || minimumLevel < 1 {
		minimumLevel = 1
	}
	// 86JP DisjointResultCalculator.CalculateCubeCount 用
	// `Math.Max(1, metadata.SellGold ?? 1)` 取卖价：装备行没有 [value]/[price]
	// 时按 1 计，仍然产出至少一个无色小晶块。运行时 PVF 里绝大多数
	// `[equipment type] [title name]` 称号与 `[crest]` 纹章都没有 [value]/[price]，
	// 旧代码在这里直接报"奖励映射非法"，客户端的分解请求因此被拒并被显示成
	// "物品栏空间不足"。
	sellValue, valueFound := document.Int("value")
	if !valueFound || sellValue <= 0 {
		sellValue, valueFound = document.Int("price")
	}
	if !valueFound || sellValue <= 0 {
		sellValue = 1
	}
	cubeCount := uint32(math.Max(1, math.Floor(float64(sellValue)*config.CubeMultipliers[multiplierIndex]/config.CubeBase)))
	rewards := []currentDisjointReward{{ItemID: config.CubeItemID, Count: cubeCount}}
	// 86JP DisjointFile.GetAdditionalItems 走 MapRarityToAdditionalIndex：负值
	// （缺 [rarity]）同样落到第 0 行，而不是"没有附加产物"。
	additionalIndex := currentDisjointRarityRowIndex(rarity)
	if additionalIndex < len(config.Additional) && len(config.Additional[additionalIndex]) > 0 {
		index, err := currentDisjointRandomIndex(len(config.Additional[additionalIndex]))
		if err != nil {
			return nil, err
		}
		count, err := currentDisjointAdditionalCount(config, additionalIndex, int(minimumLevel))
		if err != nil {
			return nil, err
		}
		currentDisjointMergeReward(&rewards, currentDisjointReward{ItemID: config.Additional[additionalIndex][index], Count: count})
	}
	// Keep each current-PVF rarity at its own counted row; a disabled row
	// suppresses only that rarity and does not shift the following souls.
	expandIndex := currentDisjointRarityExpandIndex(rarity)
	if expandIndex >= 0 && expandIndex < len(config.Expands) {
		expand := config.Expands[expandIndex]
		if expand.Enabled && expand.ItemID > 0 {
			roll, err := currentDisjointRandomPercent()
			if err != nil {
				return nil, err
			}
			if roll < currentDisjointClampPercent(expand.NormalChance) {
				count, countErr := currentDisjointLevelDivisorCount(int(minimumLevel), expand.LevelDivisor)
				if countErr != nil {
					return nil, countErr
				}
				greatRoll, rollErr := currentDisjointRandomPercent()
				if rollErr != nil {
					return nil, rollErr
				}
				if greatRoll < currentDisjointClampPercent(expand.GreatChance) {
					count++
				}
				currentDisjointMergeReward(&rewards, currentDisjointReward{ItemID: expand.ItemID, Count: count})
			}
		}
	}
	return currentDisjointFilterDisabledRewards(config, document, rewards)
}

// currentAvatarDisjointRewards resolves the avatar's PVF [grade] into the
// matching etc/avatardisjoint grade table and rolls every pool's weighted
// picks (86JP AvatarDisjointConfigProvider.Calculate), merging duplicate
// items. The rewards are real emblem items, not the old fixed booster boxes.
func currentAvatarDisjointRewards(config currentDisjointPVFConfig, document *dnfpvf.Document) ([]currentDisjointReward, error) {
	if document == nil {
		return nil, errCurrentDisjointRewardInvalid
	}
	grade, gradeFound := document.Int("grade")
	equipmentType, typeFound := document.Text("equipment type")
	rule, supported := currentEquipmentPlacementRuleForPVFType(equipmentType)
	if !gradeFound || !typeFound || !supported || rule.class != currentEquipmentPlacementClassAvatar {
		return nil, errCurrentDisjointRewardInvalid
	}
	if grade < 0 || grade >= int64(len(config.AvatarEmblemTables)) {
		return nil, errCurrentDisjointRewardInvalid
	}
	rewards := make([]currentDisjointReward, 0, 4)
	for _, pool := range config.AvatarEmblemTables[int(grade)].Pools {
		for pick := 0; pick < pool.PickCount; pick++ {
			reward, err := currentDisjointRollAvatarEmblem(pool.Rewards)
			if err != nil {
				return nil, err
			}
			merged := false
			for index := range rewards {
				if rewards[index].ItemID == reward.ItemID {
					rewards[index].Count += reward.Count
					merged = true
					break
				}
			}
			if !merged {
				rewards = append(rewards, reward)
			}
		}
	}
	if len(rewards) == 0 {
		return nil, errCurrentDisjointRewardInvalid
	}
	return rewards, nil
}

func currentDisjointRollAvatarEmblem(candidates []currentAvatarEmblemReward) (currentDisjointReward, error) {
	var total uint64
	for _, candidate := range candidates {
		total += uint64(candidate.Weight)
	}
	if total == 0 {
		return currentDisjointReward{}, errCurrentDisjointRewardInvalid
	}
	roll, err := currentDisjointRandomUint64(total)
	if err != nil {
		return currentDisjointReward{}, err
	}
	for _, candidate := range candidates {
		if roll < uint64(candidate.Weight) {
			return currentDisjointReward{ItemID: candidate.ItemID, Count: candidate.Count}, nil
		}
		roll -= uint64(candidate.Weight)
	}
	return currentDisjointReward{}, errCurrentDisjointRewardInvalid
}

func currentDisjointDocumentIsAvatar(document *dnfpvf.Document) bool {
	if document == nil {
		return false
	}
	equipmentType, found := document.Text("equipment type")
	if !found {
		return false
	}
	rule, supported := currentEquipmentPlacementRuleForPVFType(equipmentType)
	return supported && rule.class == currentEquipmentPlacementClassAvatar
}

func currentRollEmblemBoosterReward(catalog *pvfDungeonDropCatalog, boosterID uint32) (currentDisjointReward, error) {
	definition, err := catalog.ResolveItem(boosterID)
	if err != nil || definition.Kind != dungeonDropItemStackable {
		return currentDisjointReward{}, errCurrentDisjointRewardInvalid
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(documentText(document, "stackable type"))), "[booster]") {
		return currentDisjointReward{}, errCurrentDisjointRewardInvalid
	}
	active := false
	for _, section := range document.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		switch name {
		case "booster info":
			active = true
			continue
		case "/booster info":
			return currentDisjointReward{}, errCurrentDisjointRewardInvalid
		}
		if !active || name != "stackable" {
			continue
		}
		return currentRollEmblemBoosterPool(currentDisjointTokensInts(currentDisjointSectionTokens(document, section)))
	}
	return currentDisjointReward{}, errCurrentDisjointRewardInvalid
}

func currentRollEmblemBoosterPool(values []int64) (currentDisjointReward, error) {
	start := 0
	if len(values) >= 4 && (len(values)-1)%3 == 0 {
		start = 1 // draw-count; op256 supports one popup result only.
	}
	if (len(values)-start)%3 != 0 || len(values) == start {
		return currentDisjointReward{}, errCurrentDisjointRewardInvalid
	}
	type candidate struct{ itemID, weight, count uint32 }
	candidates := make([]candidate, 0, (len(values)-start)/3)
	var total uint64
	for offset := start; offset+2 < len(values); offset += 3 {
		if values[offset] <= 0 || values[offset] > math.MaxUint32 || values[offset+1] <= 0 || values[offset+1] > math.MaxUint32 || values[offset+2] <= 0 || values[offset+2] > math.MaxUint32 {
			continue
		}
		entry := candidate{uint32(values[offset]), uint32(values[offset+1]), uint32(values[offset+2])}
		total += uint64(entry.weight)
		candidates = append(candidates, entry)
	}
	if total == 0 || len(candidates) == 0 {
		return currentDisjointReward{}, errCurrentDisjointRewardInvalid
	}
	roll, err := currentDisjointRandomUint64(total)
	if err != nil {
		return currentDisjointReward{}, err
	}
	for _, candidate := range candidates {
		if roll < uint64(candidate.weight) {
			return currentDisjointReward{ItemID: candidate.itemID, Count: candidate.count}, nil
		}
		roll -= uint64(candidate.weight)
	}
	return currentDisjointReward{}, errCurrentDisjointRewardInvalid
}

func documentText(document *dnfpvf.Document, name string) string {
	if document == nil {
		return ""
	}
	value, _ := document.Text(name)
	return value
}

func currentDisjointTokensInts(tokens []dnfpvf.Token) []int64 {
	values := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			values = append(values, token.Int)
		}
	}
	return values
}

func currentDisjointAdditionalCount(config currentDisjointPVFConfig, rarity, level int) (uint32, error) {
	if rarity < 0 || rarity >= len(config.AdditionalConsts) {
		return 1, nil
	}
	consts := config.AdditionalConsts[rarity]
	divisor := consts.NormalDivisor
	roll, err := currentDisjointRandomPercent()
	if err != nil {
		return 0, err
	}
	if roll < currentDisjointClampPercent(consts.GreatChance) && consts.GreatDivisor > 0 {
		divisor = consts.GreatDivisor
	}
	return currentDisjointLevelDivisorCount(level, divisor)
}

func currentDisjointLevelDivisorCount(level int, divisor float64) (uint32, error) {
	if divisor <= 0 {
		return 1, nil
	}
	value := float64(max(1, level)) / divisor
	count := math.Floor(value)
	roll, err := currentDisjointRandomPercent()
	if err != nil {
		return 0, err
	}
	if roll/100 < value-count {
		count++
	}
	if count > math.MaxUint32 {
		return 0, errCurrentDisjointRewardInvalid
	}
	// 86JP DisjointResultCalculator.CalculateLevelDivisorCount clamps the
	// rolled count to at least 1; a low-level source still yields one
	// additional/expand item instead of failing the whole disjoint.
	if count < 1 {
		count = 1
	}
	return uint32(count), nil
}

func currentDisjointMergeReward(rewards *[]currentDisjointReward, reward currentDisjointReward) {
	if rewards == nil || reward.ItemID == 0 || reward.Count == 0 {
		return
	}
	for index := range *rewards {
		if (*rewards)[index].ItemID == reward.ItemID {
			(*rewards)[index].Count += reward.Count
			return
		}
	}
	*rewards = append(*rewards, reward)
}

func currentDisjointNormalizeJob(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.Trim(value, "`")))
	return strings.TrimSpace(strings.Trim(value, "[]"))
}

// currentDisjointRarityRowIndex mirrors 86JP PvfLib
// DisjointFile.MapRarityToCubeCreationIndex / MapRarityToAdditionalIndex: a
// negative rarity (the PvfLib EquipmentFile default of -1, i.e. a runtime PVF
// document without [rarity]) selects row 0 instead of failing the disjoint.
// Rows above the configured table length are clamped by the callers.
func currentDisjointRarityRowIndex(rarity int64) int {
	if rarity < 0 {
		return 0
	}
	return int(rarity)
}

// The current PVF retains all seven rarity rows, including the disabled
// chronicle row at 5. Legendary therefore reads row 6, not a compacted row 5.
func currentDisjointRarityExpandIndex(rarity int64) int {
	if rarity >= 0 && rarity <= 6 {
		return int(rarity)
	}
	return -1
}

func currentDisjointClampPercent(value float64) float64 {
	return math.Max(0, math.Min(100, value))
}

func currentDisjointRandomIndex(limit int) (int, error) {
	if limit <= 0 {
		return 0, errCurrentDisjointRewardInvalid
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func currentDisjointRandomUint64(limit uint64) (uint64, error) {
	if limit == 0 || limit > math.MaxInt64 {
		return 0, errCurrentDisjointRewardInvalid
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, err
	}
	return uint64(value.Int64()), nil
}

func currentDisjointRandomPercent() (float64, error) {
	value, err := currentDisjointRandomUint64(1_000_000)
	if err != nil {
		return 0, err
	}
	return float64(value) / 10_000, nil
}
