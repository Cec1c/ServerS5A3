package dnfbridge

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const (
	currentUpgradeSuccessBase    = 100000
	currentUpgradeNoticeDefault  = -1
	currentUpgradePVFPath        = "etc/upgrade.etc"
	currentAmplifyUpgradePVFPath = "etc/amplifyupgrade.etc"

	currentUpgradeTableShortRowWidth = 16
	currentUpgradeTableFullRowWidth  = 24
	currentUpgradeTableTailWidth     = 8
)

// currentUpgradeTableRow is one level row from [table type] in etc/upgrade.etc.
type currentUpgradeTableRow struct {
	TargetLevel    int
	FailureWeight  int // out of 100000
	PenaltyType    int // 0=no change, 1=downgrade, 3=destroy
	PenaltyValue   int
	MaterialItemID int
	MaterialCount  int
}

func (r currentUpgradeTableRow) SuccessWeight() int {
	w := currentUpgradeSuccessBase - r.FailureWeight
	if w < 0 {
		return 0
	}
	return w
}

// currentUpgradeTable is the parsed PVF upgrade/amplify configuration.
type currentUpgradeTable struct {
	Rows                    []currentUpgradeTableRow
	NoticeLevel             int
	Costs                   []int
	CostWeightsByRarity     []float64
	TypeWeights             []float64
	DestroyLevelByRarity    map[string]int
	DisjointBonusItemID     int
	DisjointEquipLevelConst int
	DisjointWeightByRarity  map[string]float64
	DisjointLevelConst      int
}

func (t *currentUpgradeTable) RowForLevel(level int) (currentUpgradeTableRow, bool) {
	if level < 1 || level > len(t.Rows) {
		return currentUpgradeTableRow{}, false
	}
	return t.Rows[level-1], true
}

func (t *currentUpgradeTable) DestroyLevel(rarity int) int {
	name := currentUpgradeRarityName(rarity)
	if lv, ok := t.DestroyLevelByRarity[name]; ok {
		return lv
	}
	return -1
}

var (
	currentUpgradeTableMu      sync.Mutex
	currentUpgradeTableCache   *currentUpgradeTable
	currentUpgradeTableLoadErr error
	currentAmplifyTableCache   *currentUpgradeTable
	currentAmplifyTableLoadErr error
)

var currentUpgradeTokenRE = regexp.MustCompile("`[^`]*`|-?\\d+(?:\\.\\d+)?")

func (s *Service) currentUpgradeTableForMode(mode string) (*currentUpgradeTable, error) {
	currentUpgradeTableMu.Lock()
	defer currentUpgradeTableMu.Unlock()
	path := currentUpgradePVFPath
	if mode == "amplify" {
		if currentAmplifyTableCache != nil {
			return currentAmplifyTableCache, nil
		}
		if currentAmplifyTableLoadErr != nil {
			return nil, currentAmplifyTableLoadErr
		}
		path = currentAmplifyUpgradePVFPath
	} else {
		if currentUpgradeTableCache != nil {
			return currentUpgradeTableCache, nil
		}
		if currentUpgradeTableLoadErr != nil {
			return nil, currentUpgradeTableLoadErr
		}
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		s.setCurrentUpgradeTableLoadErrLocked(mode, err)
		s.logPacketEvent("dnf-upgrade-table-load-failed", "mode", mode, "path", path, "stage", "open_pvf", "error", err)
		return nil, err
	}
	text, err := archive.ReadText(path)
	if err != nil {
		loadErr := fmt.Errorf("read %s: %w", path, err)
		s.setCurrentUpgradeTableLoadErrLocked(mode, loadErr)
		s.logPacketEvent("dnf-upgrade-table-load-failed", "mode", mode, "path", path, "stage", "read", "error", loadErr)
		return nil, loadErr
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		loadErr := fmt.Errorf("parse %s: %w", path, err)
		s.setCurrentUpgradeTableLoadErrLocked(mode, loadErr)
		s.logPacketEvent("dnf-upgrade-table-load-failed", "mode", mode, "path", path, "stage", "parse", "error", loadErr)
		return nil, loadErr
	}
	table := parseCurrentUpgradeTable(doc, text)
	if mode == "amplify" {
		currentAmplifyTableCache = table
	} else {
		currentUpgradeTableCache = table
	}
	s.logPacketEvent("dnf-upgrade-table-loaded", "mode", mode, "rows", len(table.Rows), "notice_level", table.NoticeLevel)
	return table, nil
}

func (s *Service) setCurrentUpgradeTableLoadErrLocked(mode string, err error) {
	if mode == "amplify" {
		currentAmplifyTableLoadErr = err
		return
	}
	currentUpgradeTableLoadErr = err
}

func parseCurrentUpgradeTable(doc *dnfpvf.Document, rawText string) *currentUpgradeTable {
	table := &currentUpgradeTable{
		NoticeLevel:             currentUpgradeNoticeDefault,
		DestroyLevelByRarity:    make(map[string]int),
		DisjointWeightByRarity:  make(map[string]float64),
		DisjointBonusItemID:     -1,
		DisjointEquipLevelConst: -1,
		DisjointLevelConst:      -1,
	}
	// Parse [table] rows by their authoritative gameplay tail. The current
	// runtime PVF is not the old fixed-17-column shape: target level +1 is a
	// short row, later target levels are full rows, and both shapes end with
	// failure/penalty controls plus material item/count.
	tableTokens := extractCurrentUpgradeTableTokens(rawText, "table")
	for offset := 0; offset < len(tableTokens); {
		values, next, ok := nextCurrentUpgradeTableRowValues(tableTokens, offset)
		if !ok {
			break
		}
		tail := values[len(values)-currentUpgradeTableTailWidth:]
		table.Rows = append(table.Rows, currentUpgradeTableRow{
			TargetLevel:    len(table.Rows) + 1,
			FailureWeight:  int(math.Round(tail[0])),
			PenaltyType:    int(math.Round(tail[2])),
			PenaltyValue:   int(math.Round(tail[3])),
			MaterialItemID: int(math.Round(tail[6])),
			MaterialCount:  int(math.Round(tail[7])),
		})
		offset = next
	}
	// [notice]
	if values := doc.Ints("notice"); len(values) > 0 {
		table.NoticeLevel = int(values[0])
	}
	// [cost]
	for _, v := range doc.Ints("cost") {
		table.Costs = append(table.Costs, int(v))
	}
	// [cost weights by rarity]
	table.CostWeightsByRarity = parseFloats(doc.Ints("cost weights by rarity"))
	// [type]
	table.TypeWeights = parseFloats(doc.Ints("type"))
	// [destroy level by rarity]
	parseCurrentUpgradeStringIntPairs(rawText, "destroy level by rarity", table.DestroyLevelByRarity)
	// [disjoint] section
	parseCurrentUpgradeDisjoint(rawText, table)
	return table
}

func nextCurrentUpgradeTableRowValues(tokens []float64, offset int) ([]float64, int, bool) {
	for _, width := range []int{currentUpgradeTableShortRowWidth, currentUpgradeTableFullRowWidth} {
		end := offset + width
		if end > len(tokens) {
			continue
		}
		tailStart := end - currentUpgradeTableTailWidth
		if !isCurrentUpgradeTableTail(tokens[tailStart:end]) {
			continue
		}
		return tokens[offset:end], end, true
	}
	return nil, offset, false
}

func isCurrentUpgradeTableTail(values []float64) bool {
	if len(values) != currentUpgradeTableTailWidth {
		return false
	}
	itemID := int(math.Round(values[6]))
	count := int(math.Round(values[7]))
	if math.Abs(values[6]-float64(itemID)) > 0.0001 || math.Abs(values[7]-float64(count)) > 0.0001 {
		return false
	}
	return itemID >= 1000 && count > 0
}

func parseCurrentUpgradeDisjoint(rawText string, table *currentUpgradeTable) {
	lower := strings.ToLower(rawText)
	idx := strings.Index(lower, "[disjoint bonus item]")
	if idx >= 0 {
		after := rawText[idx+len("[disjoint bonus item]"):]
		tokens := currentUpgradeTokenRE.FindAllString(after, 1)
		if len(tokens) > 0 {
			if v, err := strconv.Atoi(strings.TrimSpace(tokens[0])); err == nil {
				table.DisjointBonusItemID = v
			}
		}
	}
	idx = strings.Index(lower, "[equip level const]")
	if idx >= 0 {
		after := rawText[idx+len("[equip level const]"):]
		tokens := currentUpgradeTokenRE.FindAllString(after, 1)
		if len(tokens) > 0 {
			if v, err := strconv.Atoi(strings.TrimSpace(tokens[0])); err == nil {
				table.DisjointEquipLevelConst = v
			}
		}
	}
	idx = strings.Index(lower, "[upgrade const for bonus item count]")
	if idx >= 0 {
		after := rawText[idx+len("[upgrade const for bonus item count]"):]
		tokens := currentUpgradeTokenRE.FindAllString(after, 1)
		if len(tokens) > 0 {
			if v, err := strconv.Atoi(strings.TrimSpace(tokens[0])); err == nil {
				table.DisjointLevelConst = v
			}
		}
	}
	parseCurrentUpgradeStringFloatPairs(rawText, "upgrade failed bonus weight by rarity", table.DisjointWeightByRarity)
}

// extractCurrentUpgradeTableTokens reads the numeric values inside [table]...[/table],
// skipping the first token if it's a string (table type name like `normal`).
func extractCurrentUpgradeTableTokens(rawText, section string) []float64 {
	lower := strings.ToLower(rawText)
	startTag := "[" + section + "]"
	endTag := "[/" + section + "]"
	startIdx := strings.Index(lower, startTag)
	if startIdx < 0 {
		return nil
	}
	startIdx += len(startTag)
	endIdx := strings.Index(lower[startIdx:], endTag)
	var content string
	if endIdx >= 0 {
		content = rawText[startIdx : startIdx+endIdx]
	} else {
		content = rawText[startIdx:]
	}
	// Remove nested section tags like [table type]
	content = regexp.MustCompile(`(?i)\[/?table[^\]]*\]`).ReplaceAllString(content, " ")
	tokens := currentUpgradeTokenRE.FindAllString(content, -1)
	var values []float64
	for _, token := range tokens {
		if strings.HasPrefix(token, "`") {
			continue // skip string tokens (table type name)
		}
		if v, err := strconv.ParseFloat(token, 64); err == nil {
			values = append(values, v)
		}
	}
	return values
}

func parseCurrentUpgradeStringIntPairs(rawText, section string, out map[string]int) {
	lower := strings.ToLower(rawText)
	startTag := "[" + section + "]"
	endTag := "[/" + section + "]"
	startIdx := strings.Index(lower, startTag)
	if startIdx < 0 {
		return
	}
	startIdx += len(startTag)
	endIdx := strings.Index(lower[startIdx:], endTag)
	var content string
	if endIdx >= 0 {
		content = rawText[startIdx : startIdx+endIdx]
	} else {
		content = rawText[startIdx:]
	}
	tokens := currentUpgradeTokenRE.FindAllString(content, -1)
	for i := 0; i+1 < len(tokens); i += 2 {
		name := strings.Trim(tokens[i], "` ")
		if v, err := strconv.ParseFloat(tokens[i+1], 64); err == nil {
			out[strings.ToLower(name)] = int(math.Round(v))
		}
	}
}

func parseCurrentUpgradeStringFloatPairs(rawText, section string, out map[string]float64) {
	lower := strings.ToLower(rawText)
	startTag := "[" + section + "]"
	endTag := "[/" + section + "]"
	startIdx := strings.Index(lower, startTag)
	if startIdx < 0 {
		return
	}
	startIdx += len(startTag)
	endIdx := strings.Index(lower[startIdx:], endTag)
	var content string
	if endIdx >= 0 {
		content = rawText[startIdx : startIdx+endIdx]
	} else {
		content = rawText[startIdx:]
	}
	tokens := currentUpgradeTokenRE.FindAllString(content, -1)
	for i := 0; i+1 < len(tokens); i += 2 {
		name := strings.Trim(tokens[i], "` ")
		if v, err := strconv.ParseFloat(tokens[i+1], 64); err == nil {
			out[strings.ToLower(name)] = v
		}
	}
}

func currentUpgradeRarityName(rarity int) string {
	switch rarity {
	case 0:
		return "common"
	case 1:
		return "uncommon"
	case 2:
		return "rare"
	case 3:
		return "unique"
	case 4:
		return "epic"
	case 5:
		return "chronicle"
	case 6:
		return "legendary"
	default:
		return ""
	}
}

func parseFloats(ints []int64) []float64 {
	out := make([]float64, len(ints))
	for i, v := range ints {
		out[i] = float64(v)
	}
	return out
}

// alignedUpgradePolicyResolver returns a closure that resolves the PVF upgrade
// table row for a given mode and current level, providing success weight and
// penalty type to the inventory owner's RNG path.
func (s *Service) alignedUpgradePolicyResolver() func(mode string, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
	return func(mode string, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
		table, err := s.currentUpgradeTableForMode(mode)
		if err != nil {
			s.logPacketEvent("dnf-upgrade-policy-resolve-failed",
				"mode", mode,
				"current_level", currentLevel,
				"error", err)
			return alignedcmd.UpgradePolicyResolution{}, err
		}
		targetLevel := currentLevel + 1
		row, ok := table.RowForLevel(targetLevel)
		if !ok {
			// Beyond table range: guaranteed success (no penalty data).
			s.logPacketEvent("dnf-upgrade-policy-resolved",
				"mode", mode,
				"current_level", currentLevel,
				"target_level", targetLevel,
				"table_rows", len(table.Rows),
				"material_item_id", 0,
				"material_count", 0,
				"reason", "beyond_table_range")
			return alignedcmd.UpgradePolicyResolution{SuccessWeight: currentUpgradeSuccessBase}, nil
		}
		penaltyType := row.PenaltyType
		// Override to destroy if current level >= destroy threshold for rarity.
		// Rarity is not available here; use the table's default destroy level (common=0).
		if destroyLv := table.DestroyLevel(0); destroyLv >= 0 && currentLevel >= destroyLv && penaltyType < 3 {
			penaltyType = 3
		}
		resolution := alignedcmd.UpgradePolicyResolution{
			SuccessWeight:  row.SuccessWeight(),
			PenaltyType:    penaltyType,
			MaterialItemID: row.MaterialItemID,
			MaterialCount:  row.MaterialCount,
			NoticeLevel:    table.NoticeLevel,
		}
		if penaltyType == 3 && table.DisjointBonusItemID > 0 {
			resolution.DestroyBonusItemID = table.DisjointBonusItemID
			resolution.DestroyBonusCount = currentUpgradeDestroyBonusCount(table, currentLevel)
			// 摧毁补偿道具在运行时 PVF 里的身份与 [stack limit]，交给 inventory owner
			// 让"并入既有同身份行"按身份判、按上限封顶（<=0 = PVF 没写这一段 = 无上限，
			// 现场 3243 强烈之痕迹 / 3244 浓缩的纯洁之骸 都是这种形状）。
			resolution.DestroyBonusIdentity = s.currentUpgradeDestroyBonusIdentity(resolution.DestroyBonusItemID)
			// 落位容器/槽段同样交给 owner：新开格必须落在道具自己的 PVF 家族段
			// （`[material]` → list 0 121..176），否则材料会被放进装备页 9..64
			// ——现场 2026-09-28T16:24:33Z 增幅档就是这么把 3244 放进 0:12 的。
			resolution.DestroyBonusContainer = s.currentUpgradeDestroyBonusContainer(resolution.DestroyBonusItemID)
		}
		s.logPacketEvent("dnf-upgrade-policy-resolved",
			"mode", mode,
			"current_level", currentLevel,
			"target_level", targetLevel,
			"table_rows", len(table.Rows),
			"success_weight", resolution.SuccessWeight,
			"penalty_type", resolution.PenaltyType,
			"material_item_id", resolution.MaterialItemID,
			"material_count", resolution.MaterialCount,
			"destroy_bonus_item_id", resolution.DestroyBonusItemID,
			"destroy_bonus_count", resolution.DestroyBonusCount,
			"destroy_bonus_stack_limit", resolution.DestroyBonusIdentity.StackLimit,
			"destroy_bonus_pvf_path", resolution.DestroyBonusIdentity.PVFPath,
			"destroy_bonus_item_kind", resolution.DestroyBonusIdentity.ItemKind,
			"destroy_bonus_stackable_type", resolution.DestroyBonusIdentity.StackableType,
			"destroy_bonus_container_list", resolution.DestroyBonusContainer.ListType,
			"destroy_bonus_container_slot_start", resolution.DestroyBonusContainer.SlotStart,
			"destroy_bonus_container_slot_end", resolution.DestroyBonusContainer.SlotEnd)
		return resolution, nil
	}
}

// currentUpgradeDestroyBonusContainer 用同一个启动期运行时 PVF 目录 + 唯一的容器判定
// `currentPVFItemTargetContainer` 解析补偿道具该落在哪个容器的哪一段。
// 解析不出来时返回零值（= 没有容器信息），owner 会退回历史落点顺序；不静默：
// 记一条具名事件，复测时直接看出"这次是按无容器信息落的"。
func (s *Service) currentUpgradeDestroyBonusContainer(itemID int) alignedcmd.StackGrantContainer {
	if s == nil || itemID <= 0 {
		return alignedcmd.StackGrantContainer{}
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logPacketEvent("dnf-upgrade-destroy-bonus-container-unresolved",
			"item_id", itemID,
			"stage", "item_catalog",
			"treat_as", "legacy_first_empty_slot_order",
			"error", err)
		return alignedcmd.StackGrantContainer{}
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		s.logPacketEvent("dnf-upgrade-destroy-bonus-container-unresolved",
			"item_id", itemID,
			"stage", "resolve_item",
			"treat_as", "legacy_first_empty_slot_order",
			"error", err)
		return alignedcmd.StackGrantContainer{}
	}
	container := currentPVFItemTargetContainer(definition)
	return alignedcmd.StackGrantContainer{
		ListType:  container.ListType,
		SlotStart: container.SlotStart,
		SlotEnd:   container.SlotEnd,
	}
}

// currentUpgradeDestroyBonusIdentity 用启动期就已就绪的运行时 PVF 目录
// （`preloadPVFItemCatalog`，service.go:303）解析摧毁补偿道具的落位身份。
// 解析不出来的情形返回零值（= 身份键全部缺省 + 无上限，与 PVF 自己缺段时的语义一致），
// 但绝不静默：记一条具名事件，复测时能直接看出"这次是按无身份/无上限判的"。
func (s *Service) currentUpgradeDestroyBonusIdentity(itemID int) alignedcmd.StackGrantIdentity {
	if s == nil || itemID <= 0 {
		return alignedcmd.StackGrantIdentity{}
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logPacketEvent("dnf-upgrade-destroy-bonus-identity-unresolved",
			"item_id", itemID,
			"stage", "item_catalog",
			"treat_as", "no_identity_keys_unlimited_stack",
			"error", err)
		return alignedcmd.StackGrantIdentity{}
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		s.logPacketEvent("dnf-upgrade-destroy-bonus-identity-unresolved",
			"item_id", itemID,
			"stage", "resolve_item",
			"treat_as", "no_identity_keys_unlimited_stack",
			"error", err)
		return alignedcmd.StackGrantIdentity{}
	}
	identity := alignedcmd.StackGrantIdentity{
		PVFPath:       definition.PVFPath,
		ItemKind:      string(definition.Kind),
		StackableType: definition.StackableType,
	}
	if definition.StackLimit > 0 {
		identity.StackLimit = definition.StackLimit
	}
	return identity
}

// currentUpgradeDestroyBonusCount calculates the compensation item count on
// destruction using the 86JP formula: (level - const)^2 * rarityWeight.
func currentUpgradeDestroyBonusCount(table *currentUpgradeTable, currentLevel int) int {
	if table.DisjointLevelConst < 0 || currentLevel <= table.DisjointLevelConst {
		return 1
	}
	diff := currentLevel - table.DisjointLevelConst
	weight := 1.0
	if w, ok := table.DisjointWeightByRarity["rare"]; ok && w > 0 {
		weight = w
	}
	count := int(float64(diff*diff) * weight)
	if count <= 0 {
		return 1
	}
	return count
}
