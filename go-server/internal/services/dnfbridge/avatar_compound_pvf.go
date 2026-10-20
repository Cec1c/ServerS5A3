package dnfbridge

import (
	"fmt"
	"path"
	"strings"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 装扮合成（op99）的 PVF 权威表。
//
// 运行时 Script.pvf 同时存在两份同族配置，二者都按"每个部位一个候选池"描述
// 可合成的外观，只在物品池与合成器消耗品上不同：
//
//	etc/compoundavatar_<job>.etc             经典装扮合成器（[material] 1 21 1，
//	                                         [material_upper] 1 899 1，
//	                                         [material_master] 1 490702265 1）
//	etc/chn_compound/compoundavatar_<job>.etc 国服回忆/记忆合成器变体
//	                                         （[material] 1 2683630 1）
//
// 这两份候选并非每个职业都齐全：实测 job=13/14/15（[demonic lancer]/[at priest]/
// [gun blader]）的 chn_compound 文件是只声明 `[character type]` 的空壳
// （32/37/33 字节），表示该职业没有这套池；现场抓包（conn game-000004，
// job=14 的 test2）选中的 415510014 只出现在经典表里，说明当前客户端读的是经典表，
// 空壳变体必须被跳过而不是让整份配置失败。
//
// 每个 `[<part> avatar]` 段的第一个整数 N 表示紧随其后的 N 条 (itemID, weight)
// 是额外稀有装扮池，其余 (itemID, weight) 是玩家可选的高级外观池；这正是同族
// 既有实现（ServerS4A21 CompoundAvatarFile/CompoundAvatarProbabilityService）
// 读到的形状。命中哪个变体由客户端选定的结果 itemID 落在哪个池里决定，因此
// 服务端不会替客户端猜配置。
const (
	currentAvatarCompoundMapPath = "etc/compoundavatar.etc"
	currentAvatarCompoundCHNRoot = "etc/chn_compound"
	// currentAvatarCompoundMaxItemID 是 itemID 可写入 u32 字段的上界。
	currentAvatarCompoundMaxItemID int64 = 0xffffffff
)

var currentAvatarCompoundPartOrder = []string{"hat", "hair", "face", "neck", "coat", "pants", "belt", "shoes"}

// currentAvatarCompoundPartEquipmentTypes 把合成表里的部位名映射到 PVF 的
// `[equipment type]` 令牌（颈部在 PVF 里是 [breast avatar]，腰部是 [waist avatar]）。
var currentAvatarCompoundPartEquipmentTypes = map[string]string{
	"hat":   "[hat avatar]",
	"hair":  "[hair avatar]",
	"face":  "[face avatar]",
	"neck":  "[breast avatar]",
	"coat":  "[coat avatar]",
	"pants": "[pants avatar]",
	"belt":  "[waist avatar]",
	"shoes": "[shoes avatar]",
}

type currentAvatarCompoundPoolEntry struct {
	ItemID uint32
	Weight int64
}

type currentAvatarCompoundPartPool struct {
	Bonus []currentAvatarCompoundPoolEntry // 额外稀有装扮池（段首 N 条）
	Upper []currentAvatarCompoundPoolEntry // 玩家可选高级外观池
}

type currentAvatarCompoundVariant struct {
	Path          string
	Grade         int64
	UpperGrade    int64
	RareRate      map[string]int64
	UpperRareRate map[string]int64
	Parts         map[string]currentAvatarCompoundPartPool
	MaterialIDs   []uint32
}

// currentAvatarCompoundVariantSkip 记录一份被跳过的候选表及其原因。
// 运行时 PVF 里存在只声明 `[character type]` 的空壳变体（实测
// etc/chn_compound/compoundavatar_atpriest.etc 32 字节、compoundavatar_demonic_lancer.etc
// 37 字节、compoundavatar_gun_blader.etc 33 字节，只有 `[character type]` 段），
// 它们表示"该职业没有这一套池"，不是损坏的数据，必须跳过而不是让整份配置失败。
type currentAvatarCompoundVariantSkip struct {
	Path   string
	Reason string
}

type currentAvatarCompoundConfig struct {
	Job      byte
	JobTag   string
	Variants []currentAvatarCompoundVariant
	Skipped  []currentAvatarCompoundVariantSkip
}

// loadCurrentAvatarCompoundConfig 读取当前职业的装扮合成表。
// 职业编号 → 表文件名的偏移（job+1）与职业 tag 校验都来自同族既有实现
// （CompoundAvatarProbabilityService.LoadCompoundAvatarFile: index = job+1）。
//
// 候选表是"多份同族配置"，任意一份读不出池子都只影响它自己：实测
// etc/chn_compound/compoundavatar_atpriest.etc 是只有 `[character type]` 的 32 字节空壳，
// 早先的写法把它的解析失败当成整份配置失败，结果该职业（job=14，即现场角色
// test2 的 `[at priest]`）任何一次合法合成都会走 compound_config_unavailable，
// 客户端只看到 `00 16 00` 的通用失败文案。这里改成逐份跳过并记录原因，
// 只有一份可用配置都没有时才整体失败。
func loadCurrentAvatarCompoundConfig(source dnfpvf.Source, job byte) (currentAvatarCompoundConfig, error) {
	if source == nil {
		return currentAvatarCompoundConfig{}, errCurrentAvatarCompoundRequestInvalid
	}
	tags := jobmap.QuestTags(int(job))
	if len(tags) == 0 {
		return currentAvatarCompoundConfig{}, fmt.Errorf("%w: job=%d has no PVF job tag", errCurrentAvatarCompoundRequestInvalid, job)
	}
	jobTag := strings.ToLower(strings.TrimSpace(tags[0]))
	file, err := currentAvatarCompoundJobFile(source, int(job)+1)
	if err != nil {
		return currentAvatarCompoundConfig{}, err
	}
	candidates := []string{
		path.Join("etc", strings.ToLower(file)),
		path.Join(currentAvatarCompoundCHNRoot, strings.ToLower(file)),
	}
	config := currentAvatarCompoundConfig{Job: job, JobTag: jobTag}
	for _, candidate := range candidates {
		text, err := source.ReadText(candidate)
		if err != nil {
			config.Skipped = append(config.Skipped, currentAvatarCompoundVariantSkip{
				Path: candidate, Reason: fmt.Sprintf("read: %v", err),
			})
			continue
		}
		if strings.TrimSpace(text) == "" {
			config.Skipped = append(config.Skipped, currentAvatarCompoundVariantSkip{
				Path: candidate, Reason: "empty document",
			})
			continue
		}
		variant, err := parseCurrentAvatarCompoundVariant(candidate, text, jobTag)
		if err != nil {
			config.Skipped = append(config.Skipped, currentAvatarCompoundVariantSkip{
				Path: candidate, Reason: err.Error(),
			})
			continue
		}
		config.Variants = append(config.Variants, variant)
	}
	if len(config.Variants) == 0 {
		return currentAvatarCompoundConfig{}, fmt.Errorf(
			"%w: job=%d tag=%s has no usable compoundavatar config (skipped=%v)",
			errCurrentAvatarCompoundRequestInvalid, job, jobTag, config.Skipped)
	}
	return config, nil
}

func currentAvatarCompoundJobFile(source dnfpvf.Source, index int) (string, error) {
	text, err := source.ReadText(currentAvatarCompoundMapPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", currentAvatarCompoundMapPath, err)
	}
	document, err := dnfpvf.Parse(currentAvatarCompoundMapPath, text)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", currentAvatarCompoundMapPath, err)
	}
	for _, entry := range dnfpvf.ParseList(document) {
		if int(entry.ID) != index {
			continue
		}
		file := strings.TrimSpace(entry.Path)
		if file == "" {
			break
		}
		return file, nil
	}
	return "", fmt.Errorf("%w: %s index=%d missing", errCurrentAvatarCompoundRequestInvalid, currentAvatarCompoundMapPath, index)
}

func parseCurrentAvatarCompoundVariant(documentPath string, text string, jobTag string) (currentAvatarCompoundVariant, error) {
	document, err := dnfpvf.Parse(documentPath, text)
	if err != nil {
		return currentAvatarCompoundVariant{}, fmt.Errorf("parse %s: %w", documentPath, err)
	}
	characterType := ""
	if value, found := document.Text("character type"); found {
		characterType = strings.ToLower(strings.TrimSpace(value))
	}
	if characterType != "" && characterType != jobTag {
		return currentAvatarCompoundVariant{}, fmt.Errorf(
			"%w: %s character type=%q want=%q", errCurrentAvatarCompoundRequestInvalid, documentPath, characterType, jobTag)
	}
	variant := currentAvatarCompoundVariant{
		Path:          documentPath,
		Grade:         -1,
		UpperGrade:    -1,
		RareRate:      map[string]int64{},
		UpperRareRate: map[string]int64{},
		Parts:         map[string]currentAvatarCompoundPartPool{},
	}
	if grade, found := document.Int("grade"); found {
		variant.Grade = grade
	}
	if grade, found := document.Int("upper grade"); found {
		variant.UpperGrade = grade
	}
	variant.RareRate = currentAvatarCompoundRates(document, "rare rate")
	variant.UpperRareRate = currentAvatarCompoundRates(document, "upper rare rate")
	for _, part := range currentAvatarCompoundPartOrder {
		values := document.Ints(part + " avatar")
		if len(values) == 0 {
			continue
		}
		pool, err := currentAvatarCompoundParsePartPool(part, values)
		if err != nil {
			return currentAvatarCompoundVariant{}, fmt.Errorf("%s: %w", documentPath, err)
		}
		variant.Parts[part] = pool
	}
	if len(variant.Parts) == 0 {
		return currentAvatarCompoundVariant{}, fmt.Errorf(
			"%w: %s has no [<part> avatar] pool", errCurrentAvatarCompoundRequestInvalid, documentPath)
	}
	for _, section := range []string{"material", "material_upper", "material_master"} {
		if material, ok := currentAvatarCompoundMaterialItemID(document, section); ok {
			variant.MaterialIDs = append(variant.MaterialIDs, material)
		}
	}
	if len(variant.MaterialIDs) == 0 {
		return currentAvatarCompoundVariant{}, fmt.Errorf(
			"%w: %s declares no [material] item", errCurrentAvatarCompoundRequestInvalid, documentPath)
	}
	return variant, nil
}

// currentAvatarCompoundRates 把 `[rare rate]` / `[upper rare rate]` 段读成
// 部位 → 万分比率的映射。段落里部位名与数字成对出现，因此按 token 顺序配对，
// 而不是把两类 token 分开取后再按位置猜。
func currentAvatarCompoundRates(document *dnfpvf.Document, section string) map[string]int64 {
	if document == nil {
		return nil
	}
	tokens, ok := document.Section(section)
	if !ok {
		return nil
	}
	out := make(map[string]int64)
	for index := 0; index+1 < len(tokens); index++ {
		name := tokens[index]
		if name.Kind != dnfpvf.TokenString && name.Kind != dnfpvf.TokenIdent {
			continue
		}
		value := tokens[index+1]
		if value.Kind != dnfpvf.TokenInt {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(name.Value))] = value.Int
		index++
	}
	return out
}

// currentAvatarCompoundMaterialItemID 读取顶层 `[material*]` 段声明的合成器物品。
// 段值是 "1 <itemID> 1" 形态（合成器本体在中间），与同族既有实现的
// ParseMaterialItemId(tokens[1]) 一致；只有单个数字时按物品号读取。
//
// 必须取"最后一个"同名段：经典表的 `[material]` 会先出现在
// `[common bind cube] 13/14` 块里（那是升级合成器自己的材料），真正的
// 装扮合成器材料在同名段的最后一处。这与同族既有实现按顺序遍历、后者覆盖
// 前者的读法一致。
func currentAvatarCompoundMaterialItemID(document *dnfpvf.Document, section string) (uint32, bool) {
	values := currentAvatarCompoundSectionIntsLast(document, section)
	switch {
	case len(values) >= 3:
		return currentAvatarCompoundItemID(values[1])
	case len(values) == 1:
		return currentAvatarCompoundItemID(values[0])
	default:
		return 0, false
	}
}

// currentAvatarCompoundSectionIntsLast 返回最后一个同名段落的整数 token。
func currentAvatarCompoundSectionIntsLast(document *dnfpvf.Document, name string) []int64 {
	if document == nil {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(name))
	var out []int64
	for _, section := range document.Sections {
		if strings.ToLower(strings.TrimSpace(section.Name)) != key {
			continue
		}
		if section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
			continue
		}
		values := make([]int64, 0, section.End-section.Start)
		for _, token := range document.Tokens[section.Start:section.End] {
			if token.Kind == dnfpvf.TokenInt {
				values = append(values, token.Int)
			}
		}
		out = values
	}
	return out
}

func currentAvatarCompoundItemID(value int64) (uint32, bool) {
	if value <= 0 || value > currentAvatarCompoundMaxItemID {
		return 0, false
	}
	return uint32(value), true
}

func currentAvatarCompoundParsePartPool(part string, values []int64) (currentAvatarCompoundPartPool, error) {
	if len(values) == 0 {
		return currentAvatarCompoundPartPool{}, fmt.Errorf("%w: part=%s empty pool", errCurrentAvatarCompoundRequestInvalid, part)
	}
	bonusCount := values[0]
	if bonusCount < 0 || bonusCount*2+1 > int64(len(values)) {
		return currentAvatarCompoundPartPool{}, fmt.Errorf(
			"%w: part=%s bonus_count=%d values=%d", errCurrentAvatarCompoundRequestInvalid, part, bonusCount, len(values))
	}
	pool := currentAvatarCompoundPartPool{}
	offset := 1
	for index := int64(0); index < bonusCount; index++ {
		entry, ok := currentAvatarCompoundPoolEntryAt(values, offset)
		if !ok {
			return currentAvatarCompoundPartPool{}, fmt.Errorf(
				"%w: part=%s bonus row=%d", errCurrentAvatarCompoundRequestInvalid, part, index)
		}
		pool.Bonus = append(pool.Bonus, entry)
		offset += 2
	}
	for ; offset+1 < len(values); offset += 2 {
		entry, ok := currentAvatarCompoundPoolEntryAt(values, offset)
		if !ok {
			return currentAvatarCompoundPartPool{}, fmt.Errorf(
				"%w: part=%s upper row=%d", errCurrentAvatarCompoundRequestInvalid, part, offset)
		}
		pool.Upper = append(pool.Upper, entry)
	}
	if len(pool.Upper) == 0 {
		return currentAvatarCompoundPartPool{}, fmt.Errorf("%w: part=%s has no upper pool", errCurrentAvatarCompoundRequestInvalid, part)
	}
	return pool, nil
}

func currentAvatarCompoundPoolEntryAt(values []int64, offset int) (currentAvatarCompoundPoolEntry, bool) {
	if offset < 0 || offset+1 >= len(values) {
		return currentAvatarCompoundPoolEntry{}, false
	}
	itemID, ok := currentAvatarCompoundItemID(values[offset])
	if !ok {
		return currentAvatarCompoundPoolEntry{}, false
	}
	weight := values[offset+1]
	if weight < 0 {
		return currentAvatarCompoundPoolEntry{}, false
	}
	return currentAvatarCompoundPoolEntry{ItemID: itemID, Weight: weight}, true
}

// partForItem 返回该外观属于哪个部位；只有出现在池里的 itemID 才会被接受，
// 客户端选定的结果外观因此不需要服务端猜测。
func (v currentAvatarCompoundVariant) partForItem(itemID uint32) (string, bool) {
	for _, part := range currentAvatarCompoundPartOrder {
		pool, ok := v.Parts[part]
		if !ok {
			continue
		}
		if pool.contains(itemID) {
			return part, true
		}
	}
	return "", false
}

func (p currentAvatarCompoundPartPool) contains(itemID uint32) bool {
	for _, entry := range p.Bonus {
		if entry.ItemID == itemID {
			return true
		}
	}
	for _, entry := range p.Upper {
		if entry.ItemID == itemID {
			return true
		}
	}
	return false
}

// variantForItem 选中客户端实际使用的那份配置：结果外观只可能来自客户端读到
// 的那个池，因此以池成员判定变体，避免服务端替客户端选配置。
func (c currentAvatarCompoundConfig) variantForItem(itemID uint32) (currentAvatarCompoundVariant, string, bool) {
	for _, variant := range c.Variants {
		if part, ok := variant.partForItem(itemID); ok {
			return variant, part, true
		}
	}
	return currentAvatarCompoundVariant{}, "", false
}

// acceptsMaterial 判断主背包槽位里的合成器消耗品是否被任一配置声明。
func (c currentAvatarCompoundConfig) acceptsMaterial(itemID uint32) bool {
	if itemID == 0 {
		return false
	}
	for _, variant := range c.Variants {
		for _, declared := range variant.MaterialIDs {
			if declared == itemID {
				return true
			}
		}
	}
	return false
}

// declaredMaterialIDs 汇总所有可用变体声明的合成器物品，供拒绝日志定位。
func (c currentAvatarCompoundConfig) declaredMaterialIDs() []uint32 {
	seen := map[uint32]bool{}
	out := make([]uint32, 0, len(c.Variants))
	for _, variant := range c.Variants {
		for _, declared := range variant.MaterialIDs {
			if seen[declared] {
				continue
			}
			seen[declared] = true
			out = append(out, declared)
		}
	}
	return out
}

// usablePaths 返回真正被采纳的变体路径，供拒绝日志区分"池里没有"
// 与"配置根本没读到"。
func (c currentAvatarCompoundConfig) usablePaths() []string {
	out := make([]string, 0, len(c.Variants))
	for _, variant := range c.Variants {
		out = append(out, variant.Path)
	}
	return out
}

// bonusRateFor 计算额外稀有装扮的命中率（万分比）。
//
// 数值全部来自 PVF：两件材料同为 [grade] 时用 [rare rate]；同为 [upper grade]
// 时叠加 [upper rare rate]（与合成器说明"两个高级装扮合成时稀有几率增大"一致）；
// 一高一普时取两者中点（沿用同族既有实现的三档写法）。
// 黄金/钻石合成器的额外加成需要 etc/serverparameter.etc 的字段证据，本轮不猜，
// 保持不加成。
func (v currentAvatarCompoundVariant) bonusRateFor(part string, grade1 int64, grade2 int64) int64 {
	rare := v.RareRate[part]
	upper := v.UpperRareRate[part]
	var rate int64
	switch {
	case v.Grade >= 0 && grade1 == v.Grade && grade2 == v.Grade:
		rate = rare
	case v.UpperGrade >= 0 && grade1 == v.UpperGrade && grade2 == v.UpperGrade:
		rate = rare + upper
	default:
		rate = rare + upper/2
	}
	if rate < 0 {
		return 0
	}
	if rate > currentAvatarCompoundRateScale {
		return currentAvatarCompoundRateScale
	}
	return rate
}

// currentRollCurrentAvatarCompoundBonus 按权重从额外稀有装扮池取一件。
func currentRollCurrentAvatarCompoundBonus(pool []currentAvatarCompoundPoolEntry, roll func(int) int) (currentAvatarCompoundPoolEntry, bool) {
	if len(pool) == 0 || roll == nil {
		return currentAvatarCompoundPoolEntry{}, false
	}
	total := 0
	for _, entry := range pool {
		if entry.Weight <= 0 {
			continue
		}
		if entry.Weight > int64(^uint(0)>>1)-int64(total) {
			return currentAvatarCompoundPoolEntry{}, false
		}
		total += int(entry.Weight)
	}
	if total <= 0 {
		return currentAvatarCompoundPoolEntry{}, false
	}
	value := roll(total)
	if value < 0 || value >= total {
		return currentAvatarCompoundPoolEntry{}, false
	}
	accumulated := 0
	for _, entry := range pool {
		if entry.Weight <= 0 {
			continue
		}
		accumulated += int(entry.Weight)
		if value < accumulated {
			return entry, true
		}
	}
	return currentAvatarCompoundPoolEntry{}, false
}
