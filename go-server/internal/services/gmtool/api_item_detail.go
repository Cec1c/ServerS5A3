package gmtool

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 物品详情（对应上游 0.1.8 dnfbridge/gm.go 的 GMItemDetail + 它的 8 个 helper）。
//
// 纯读：不写库、不需要开关。摘要字段取本仓 gmtool 的 itemEntry（PVF 索引缓存），
// 详情段按需从运行时 PVF 读**该件物品自己的文档**——与上游
// gm.go:1461 `catalog.source.ReadText(definition.PVFPath)` 同一个数据源。
// 之所以不在 itemEntry 里多存这些段：那会把 itemIndexVersion 顶到 4，让线上
// 下次启动重建 5 万条的磁盘索引，收益却只是一次单文档读取。
//
// 数据来源纪律：PVF 没有的段一律留空（数字 0、文本 ""、数组为空），界面显示
// 「PVF 未提供」。没有一处默认文案、没有跨物品复制、没有合成属性。
//
// 上下游取证（90CN 真档 Script.pvf，sha256 cc517a6e…aacee1）：
//   - `[icon]` 段确实存在，形如 `Item/stackable/consumption.img` 2066，
//     即「路径 + 序号」——上游 dungeon_drop_catalog.go:258-259 也是读 "icon"。
//   - `etc/equipmentpartset.etc` 的 `character/partset/*.equ` 引用加上
//     `equipment/` 前缀后确实能命中归档（如 equipment/character/partset/2choroset1.equ）。
//   - `[piece set ability]` 块确实按「件数 + 子段（skill data up / parameter
//     basic explain …）」组织，并由 `[/piece set ability]` 收口。

const (
	// equipmentPartSetListPath 是上游 gm.go:1619 的同一个列表。
	equipmentPartSetListPath = "etc/equipmentpartset.etc"
	// topQualityMultiplier 是上游 gm.go:1551 的展示口径：详情页按「满品级」
	// 展示基础攻防，即基础值 ×1.1 再叠加 add 段。它是**展示系数**，不是 PVF
	// 字段；响应里以 qualityPercent=100 标明这一点。
	topQualityMultiplier = 1.1
)

func (s *Server) handleItemDetail(w http.ResponseWriter, r *http.Request) {
	itemID, err := strconv.ParseUint(strings.TrimSpace(r.PathValue("itemId")), 10, 32)
	if err != nil || itemID == 0 {
		writeFailure(w, fmt.Sprintf("物品编号无效: %s", r.PathValue("itemId")))
		return
	}
	if failure := s.items.failure(); failure != nil {
		writeFailure(w, "索引构建失败: "+failure.Error())
		return
	}
	if !s.items.isReady() {
		writeFailure(w, "物品索引还在构建中, 稍等几秒再查")
		return
	}
	entry, found := s.items.lookup(uint32(itemID))
	if !found {
		writeFailure(w, fmt.Sprintf("物品 %d 不在当前运行时 Script.pvf 的物品表里", itemID))
		return
	}

	ctx := r.Context()
	source, err := s.source()
	if err != nil {
		writeFailure(w, "PVF 不可用: "+err.Error())
		return
	}
	document, err := itemDocument(ctx, source, entry)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}

	payload := map[string]any{
		"item":          itemSummaryPayload(entry, document),
		"explain":       itemDetailText(document, "explain"),
		"basicExplain":  itemDetailText(document, "basic explain"),
		"detailExplain": itemDetailText(document, "detail explain"),
		"flavorText":    itemDetailText(document, "flavor text"),
	}
	// 数值段只在 PVF **确实有该段**时才出现：缺段就让字段缺省，界面据此显示
	// 「PVF 未提供」，而不是把一个和「PVF 里写了 0」无法区分的 0 送出去。
	putDocumentInt(payload, document, "weight", "weight")
	putDocumentInt(payload, document, "repairPrice", "repair price")
	putDocumentInt(payload, document, "value", "value")

	if entry.Kind == "equipment" {
		// 装备专属段。上游 gm.go:1480-1493 的同一组。
		payload["qualityPercent"] = int64(100)
		payload["weaponAttackSpeed"] = equipmentWeaponAttackSpeed(document, entry.Tag)
		payload["itemGroupName"] = itemDetailText(document, "item group name")
		putDocumentInt(payload, document, "partSetIndex", "part set index")
		partSetIndex, _ := document.Int("part set index")
		if partSetIndex > 0 {
			set, setErr := s.partSetDetail(ctx, partSetIndex)
			if setErr != nil {
				// 上游 gm.go:1486-1489 的口径：套装**文件读失败 = 报错**，
				// 不静默降级、更不合成加成。
				writeFailure(w, setErr.Error())
				return
			}
			// 列表里找不到该 setID 时上游返回 (nil,nil) —— 静默无套装。
			if set != nil {
				payload["equipmentSet"] = set
			}
		}
		payload["elementalProperties"] = equipmentElementalProperties(document)
		payload["attributes"] = equipmentDetailAttributes(document)
	}
	writeOK(w, payload)
}

// putDocumentInt 只在 PVF 里确实存在该段时把整数写进响应。缺段 -> 字段缺省。
func putDocumentInt(payload map[string]any, document *dnfpvf.Document, key, section string) {
	if value, ok := document.Int(section); ok {
		payload[key] = value
	}
}

// itemDocument 读出该物品自己的 PVF 文档。路径解析规则与 items_index.parseItemEntry
// 完全一致（itemEntry.documentPath 是唯一实现）。
func itemDocument(ctx context.Context, source dnfpvf.Source, entry itemEntry) (*dnfpvf.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	documentPath := entry.documentPath()
	text, err := source.ReadText(documentPath)
	if err != nil {
		return nil, fmt.Errorf("读取物品文档失败 item=%d path=%s: %w", entry.ID, documentPath, err)
	}
	document, err := dnfpvf.Parse(documentPath, text)
	if err != nil {
		return nil, fmt.Errorf("解析物品文档失败 item=%d path=%s: %w", entry.ID, documentPath, err)
	}
	return document, nil
}

// itemSummaryPayload 汇总一件物品的目录信息。目录字段来自 itemEntry（PVF 索引），
// 只有 attachType / price 两段是上游 GMItemSummary 有、本仓索引没缓存的，按需从
// 刚读到的文档里取；段不存在就是空串 / 省略 price。
func itemSummaryPayload(entry itemEntry, document *dnfpvf.Document) map[string]any {
	usableJobs := entry.UsableJobLabels
	if usableJobs == nil {
		usableJobs = []string{}
	}
	payload := map[string]any{
		"itemId":           entry.ID,
		"name":             entry.Name,
		"kind":             entry.Kind,
		"pvfPath":          entry.documentPath(),
		"minimumLevel":     entry.MinLevel,
		"rarity":           entry.Rarity,
		"stackLimit":       entry.StackLimit,
		"equipmentType":    "",
		"usableJobs":       usableJobs,
		"usableJob":        entry.UsableJob,
		"grade":            entry.Grade,
		"stackableType":    "",
		"usablePeriodDays": entry.UsablePeriodDays,
		"expirationDate":   entry.AbsoluteExpireUnix,
		"iconPath":         "",
		"iconIndex":        int64(0),
	}
	// 类型串回读 PVF 原文（entry.Tag 只是第一个 [tag] 的规范化结果，会丢掉复合串上的
	// 其它标注），与上游 GMItemSummary.{StackableType,EquipmentType} 同义。
	if stackableType, ok := document.Text("stackable type"); ok {
		payload["stackableType"] = strings.TrimSpace(stackableType)
	}
	if equipmentType, ok := document.Text("equipment type"); ok {
		payload["equipmentType"] = strings.TrimSpace(equipmentType)
	}
	// 上游 dungeon_drop_catalog.go:258-259 用的就是 "icon" 段（路径 + 序号）。
	if iconPath, ok := document.Text("icon"); ok {
		payload["iconPath"] = strings.TrimSpace(iconPath)
	}
	if iconIndex, ok := document.Int("icon"); ok {
		payload["iconIndex"] = iconIndex
	}
	if attachType, ok := document.Text("attach type"); ok {
		payload["attachType"] = strings.TrimSpace(attachType)
	}
	if price, ok := document.Int("price"); ok && price >= 0 {
		payload["price"] = price
	}
	return payload
}

// itemDetailText 等价上游 gmItemDetailText（gm.go:1497）：逐段裁剪、丢弃空段、
// 还原 `%%` -> `%`、多段以换行连接；段不存在返回 ""。
func itemDetailText(document *dnfpvf.Document, section string) string {
	if document == nil {
		return ""
	}
	parts := make([]string, 0, 1)
	for _, value := range document.Texts(section) {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, strings.ReplaceAll(value, "%%", "%"))
		}
	}
	return strings.Join(parts, "\n")
}

// pvfStatNumber 是本包版的 equipmentStatNumber（dnfbridge/equipment_stats.go:185）。
// gmtool 不能 import dnfbridge（依赖方向相反），所以这里保持同语义的本地实现。
func pvfStatNumber(document *dnfpvf.Document, names ...string) int64 {
	if document == nil {
		return 0
	}
	for _, name := range names {
		if values := document.Numbers(name); len(values) > 0 {
			return int64(values[0])
		}
		if texts := document.Texts(name); len(texts) > 0 {
			value, err := strconv.ParseFloat(strings.TrimSpace(texts[0]), 64)
			if err == nil {
				return int64(value)
			}
		}
	}
	return 0
}

// normalizePVFType 等价 dnfbridge 的 normalizeMagicBoxPVFType（magic_box_pvf.go:288）。
func normalizePVFType(value string) string {
	return strings.ToLower(strings.Trim(value, "` []"))
}

func equipmentTopQualityStat(document *dnfpvf.Document, baseSection, addSection string) int64 {
	base := pvfStatNumber(document, baseSection)
	add := pvfStatNumber(document, addSection)
	if base == 0 {
		return add
	}
	return int64(math.Round(float64(base)*topQualityMultiplier)) + add
}

func equipmentDetailSum(document *dnfpvf.Document, sections ...string) int64 {
	var total int64
	seen := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		key := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(section)), " "))
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		total += pvfStatNumber(document, section)
	}
	return total
}

func equipmentWeaponAttackSpeed(document *dnfpvf.Document, equipmentType string) string {
	if document == nil || normalizePVFType(equipmentType) != "weapon" {
		return ""
	}
	values := document.Numbers("attack speed")
	if len(values) == 0 {
		return ""
	}
	switch speed := values[0]; {
	case speed >= 100:
		return "very_fast"
	case speed >= 50:
		return "fast"
	case speed > -50:
		return "normal"
	case speed > -100:
		return "slow"
	default:
		return "very_slow"
	}
}

func equipmentElementalProperties(document *dnfpvf.Document) []string {
	if document == nil {
		return []string{}
	}
	values := document.Texts("elemental property")
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizePVFType(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

type itemAttribute struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

func equipmentDetailAttributes(document *dnfpvf.Document) []itemAttribute {
	candidates := []itemAttribute{
		{Name: "physical_attack", Value: equipmentTopQualityStat(document, "equipment physical attack", "add equipment physical attack")},
		{Name: "magical_attack", Value: equipmentTopQualityStat(document, "equipment magical attack", "add equipment magical attack")},
		{Name: "independent_attack", Value: equipmentTopQualityStat(document, "separate attack", "add separate attack")},
		{Name: "physical_defense", Value: equipmentTopQualityStat(document, "equipment physical defense", "add equipment physical defense")},
		{Name: "magical_defense", Value: equipmentTopQualityStat(document, "equipment magical defense", "add equipment magical defense")},
		{Name: "strength", Value: equipmentTopQualityStat(document, "physical attack", "add physical attack") + equipmentDetailSum(document, "strength", "add strength")},
		{Name: "intelligence", Value: equipmentTopQualityStat(document, "magical attack", "add magical attack") + equipmentDetailSum(document, "intelligence", "add intelligence")},
		{Name: "vitality", Value: equipmentTopQualityStat(document, "physical defense", "add physical defense") + equipmentDetailSum(document, "vitality", "add vitality")},
		{Name: "spirit", Value: equipmentTopQualityStat(document, "magical defense", "add magical defense") + equipmentDetailSum(document, "spirit", "add spirit")},
		{Name: "hp_max", Value: equipmentDetailSum(document, "HP MAX", "hp max", "add HP MAX", "add hp max")},
		{Name: "mp_max", Value: equipmentDetailSum(document, "MP MAX", "mp max", "add MP MAX", "add mp max")},
		{Name: "attack_speed", Value: equipmentDetailSum(document, "add attack speed")},
		{Name: "move_speed", Value: equipmentDetailSum(document, "move speed", "add move speed")},
		{Name: "cast_speed", Value: equipmentDetailSum(document, "cast speed", "add cast speed")},
		{Name: "hit_recovery", Value: equipmentDetailSum(document, "hit recovery", "add hit recovery")},
		{Name: "jump_power", Value: equipmentDetailSum(document, "jump power", "add jump power")},
		{Name: "hit_rate", Value: -equipmentDetailSum(document, "stuck", "add stuck")},
		{Name: "physical_critical", Value: equipmentDetailSum(document, "physical critical hit", "add physical critical hit")},
		{Name: "magical_critical", Value: equipmentDetailSum(document, "magical critical hit", "add magical critical hit")},
		{Name: "all_element_enhancement", Value: equipmentDetailSum(document, "all elemental attack", "add all elemental attack")},
		{Name: "fire_element_enhancement", Value: equipmentDetailSum(document, "fire attack", "fire elemental attack", "add fire attack", "add fire elemental attack")},
		{Name: "water_element_enhancement", Value: equipmentDetailSum(document, "water attack", "water elemental attack", "add water attack", "add water elemental attack")},
		{Name: "light_element_enhancement", Value: equipmentDetailSum(document, "light attack", "light elemental attack", "add light attack", "add light elemental attack")},
		{Name: "dark_element_enhancement", Value: equipmentDetailSum(document, "dark attack", "dark elemental attack", "add dark attack", "add dark elemental attack")},
		{Name: "fire_resistance", Value: equipmentDetailSum(document, "fire resistance", "add fire resistance")},
		{Name: "water_resistance", Value: equipmentDetailSum(document, "water resistance", "add water resistance")},
		{Name: "light_resistance", Value: equipmentDetailSum(document, "light resistance", "add light resistance")},
		{Name: "dark_resistance", Value: equipmentDetailSum(document, "dark resistance", "add dark resistance")},
		{Name: "anti_magic", Value: equipmentDetailSum(document, "anti evil")},
	}
	attributes := make([]itemAttribute, 0, len(candidates))
	for _, attribute := range candidates {
		if attribute.Value != 0 {
			attributes = append(attributes, attribute)
		}
	}
	return attributes
}

// partSetDetail 解析 [part set index] 指向的套装文件。
//
// 分级（与上游 gm.go:1621-1669 一致）：
//   - setID <= 0           -> (nil, nil) 静默无套装；
//   - 列表里没有该 setID   -> (nil, nil) 静默无套装；
//   - 列表文件读/解析失败  -> 报错；
//   - 被引用套装文件读/解析失败 -> 报错。
//
// 不合成任何加成：`[piece set ability]` 段不存在就是空 Bonuses。
func (s *Server) partSetDetail(ctx context.Context, setID int64) (map[string]any, error) {
	if setID <= 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := s.source()
	if err != nil {
		return nil, fmt.Errorf("PVF 不可用: %w", err)
	}
	references, err := s.pvf.equipmentPartSets(s)
	if err != nil {
		return nil, err
	}
	reference, found := references[setID]
	if !found {
		return nil, nil
	}
	setPath := strings.ReplaceAll(strings.TrimSpace(reference.Path), "\\", "/")
	setPath = path.Clean(setPath)
	if !strings.HasPrefix(strings.ToLower(setPath), "equipment/") {
		setPath = path.Clean(path.Join("equipment", setPath))
	}
	setText, err := source.ReadText(setPath)
	if err != nil {
		return nil, fmt.Errorf("读取套装文件失败 set=%d path=%s: %w", setID, setPath, err)
	}
	setDocument, err := dnfpvf.Parse(setPath, setText)
	if err != nil {
		return nil, fmt.Errorf("解析套装文件失败 set=%d path=%s: %w", setID, setPath, err)
	}
	bonuses := equipmentSetBonuses(setDocument)
	payload := map[string]any{
		"setId":   setID,
		"name":    strings.Join(strings.Fields(reference.Name), " "),
		"pvfPath": setPath,
	}
	if len(bonuses) > 0 {
		payload["bonuses"] = bonuses
	}
	return payload, nil
}

type equipmentSetBonus struct {
	RequiredPieces int             `json:"requiredPieces"`
	BasicExplain   string          `json:"basicExplain,omitempty"`
	DetailExplain  string          `json:"detailExplain,omitempty"`
	Attributes     []itemAttribute `json:"attributes,omitempty"`
}

// equipmentSetBonuses 等价上游 gmEquipmentSetBonuses（gm.go:1671）：
// 逐块扫描 `[piece set ability]`，块内第一个正整数是「需要件数」，块内其余子段
// 合并成一份临时文档后再按属性名求和。件数 <=0 的块（例如只有 [if]/[then]
// 触发式效果而没写件数的块）上游直接丢弃，这里保持同一取舍。
func equipmentSetBonuses(document *dnfpvf.Document) []equipmentSetBonus {
	if document == nil {
		return nil
	}
	bonuses := make([]equipmentSetBonus, 0, 3)
	var current *dnfpvf.Document
	requiredPieces := 0
	finish := func() {
		if current == nil || requiredPieces <= 0 {
			return
		}
		bonuses = append(bonuses, equipmentSetBonus{
			RequiredPieces: requiredPieces,
			BasicExplain:   itemDetailText(current, "parameter basic explain"),
			DetailExplain:  itemDetailText(current, "parameter detail explain"),
			Attributes:     equipmentSetAttributes(current),
		})
	}
	for _, section := range document.Sections {
		name := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(section.Name)), " "))
		switch name {
		case "piece set ability":
			finish()
			current = &dnfpvf.Document{Path: document.Path}
			requiredPieces = 0
			for _, token := range documentSectionTokens(document, section) {
				if token.Kind == dnfpvf.TokenInt && token.Int > 0 && token.Int <= math.MaxInt32 {
					requiredPieces = int(token.Int)
					break
				}
			}
			continue
		case "/piece set ability":
			finish()
			current = nil
			requiredPieces = 0
			continue
		}
		if current == nil {
			continue
		}
		tokens := documentSectionTokens(document, section)
		start := len(current.Tokens)
		current.Tokens = append(current.Tokens, tokens...)
		current.Sections = append(current.Sections, dnfpvf.Section{
			Name: section.Name, Start: start, End: len(current.Tokens),
		})
	}
	finish()
	return bonuses
}

func documentSectionTokens(document *dnfpvf.Document, section dnfpvf.Section) []dnfpvf.Token {
	if document == nil || section.Start < 0 || section.End < section.Start || section.End > len(document.Tokens) {
		return nil
	}
	return document.Tokens[section.Start:section.End]
}

// equipmentSetAttributes 与上游 gm.go:1728 的同一张表：套装属性**不做**
// ×1.1 满品级投影，直接把 基础段 + add 段 相加。
func equipmentSetAttributes(document *dnfpvf.Document) []itemAttribute {
	candidates := []itemAttribute{
		{Name: "physical_attack", Value: equipmentDetailSum(document, "equipment physical attack", "add equipment physical attack")},
		{Name: "magical_attack", Value: equipmentDetailSum(document, "equipment magical attack", "add equipment magical attack")},
		{Name: "independent_attack", Value: equipmentDetailSum(document, "separate attack", "add separate attack")},
		{Name: "physical_defense", Value: equipmentDetailSum(document, "equipment physical defense", "add equipment physical defense")},
		{Name: "magical_defense", Value: equipmentDetailSum(document, "equipment magical defense", "add equipment magical defense")},
		{Name: "strength", Value: equipmentDetailSum(document, "physical attack", "add physical attack", "strength", "add strength")},
		{Name: "intelligence", Value: equipmentDetailSum(document, "magical attack", "add magical attack", "intelligence", "add intelligence")},
		{Name: "vitality", Value: equipmentDetailSum(document, "physical defense", "add physical defense", "vitality", "add vitality")},
		{Name: "spirit", Value: equipmentDetailSum(document, "magical defense", "add magical defense", "spirit", "add spirit")},
		{Name: "hp_max", Value: equipmentDetailSum(document, "HP MAX", "hp max", "add HP MAX", "add hp max")},
		{Name: "mp_max", Value: equipmentDetailSum(document, "MP MAX", "mp max", "add MP MAX", "add mp max")},
		{Name: "attack_speed", Value: equipmentDetailSum(document, "attack speed", "add attack speed")},
		{Name: "move_speed", Value: equipmentDetailSum(document, "move speed", "add move speed")},
		{Name: "cast_speed", Value: equipmentDetailSum(document, "cast speed", "add cast speed")},
		{Name: "hit_recovery", Value: equipmentDetailSum(document, "hit recovery", "add hit recovery")},
		{Name: "jump_power", Value: equipmentDetailSum(document, "jump power", "add jump power")},
		{Name: "hit_rate", Value: -equipmentDetailSum(document, "stuck", "add stuck")},
		{Name: "physical_critical", Value: equipmentDetailSum(document, "physical critical hit", "add physical critical hit")},
		{Name: "magical_critical", Value: equipmentDetailSum(document, "magical critical hit", "add magical critical hit")},
		{Name: "all_element_enhancement", Value: equipmentDetailSum(document, "all elemental attack", "add all elemental attack")},
		{Name: "fire_element_enhancement", Value: equipmentDetailSum(document, "fire attack", "fire elemental attack", "add fire attack", "add fire elemental attack")},
		{Name: "water_element_enhancement", Value: equipmentDetailSum(document, "water attack", "water elemental attack", "add water attack", "add water elemental attack")},
		{Name: "light_element_enhancement", Value: equipmentDetailSum(document, "light attack", "light elemental attack", "add light attack", "add light elemental attack")},
		{Name: "dark_element_enhancement", Value: equipmentDetailSum(document, "dark attack", "dark elemental attack", "add dark attack", "add dark elemental attack")},
		{Name: "fire_resistance", Value: equipmentDetailSum(document, "fire resistance", "add fire resistance")},
		{Name: "water_resistance", Value: equipmentDetailSum(document, "water resistance", "add water resistance")},
		{Name: "light_resistance", Value: equipmentDetailSum(document, "light resistance", "add light resistance")},
		{Name: "dark_resistance", Value: equipmentDetailSum(document, "dark resistance", "add dark resistance")},
		{Name: "anti_magic", Value: equipmentDetailSum(document, "anti evil")},
	}
	attributes := make([]itemAttribute, 0, len(candidates))
	for _, attribute := range candidates {
		if attribute.Value != 0 {
			attributes = append(attributes, attribute)
		}
	}
	return attributes
}
