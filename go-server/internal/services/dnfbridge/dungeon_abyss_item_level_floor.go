package dnfbridge

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 默认85/90是高级深渊的既定物品等级策略，不能推广成所有副本的全局门槛。
// 未显式配置且两个副本等级均为正、均低于85时，碎片成品与装备共用既有
// hellPartyRewardGradeRangeForLevels 的grade闭区间；读取真实.equ grade，
// 不从使用等级猜grade。不完整副本等级不启用低级适配。
// 显式整数、非法配置的默认回落及legacy保持原语义。高级装备的历史未知
// minimum level兼容行为保持；新增低级分支只接受已知物品等级/grade。
const (
	abyssItemLevelFloorEnv   = "DNFBRIDGE_ABYSS_ITEM_LEVEL_FLOOR"
	abyssItemLevelCeilingEnv = "DNFBRIDGE_ABYSS_ITEM_LEVEL_CEILING"

	// abyssItemLevelFloorDefault / CeilingDefault = 操作者口径「应该是 85-90」。
	abyssItemLevelFloorDefault   = 85
	abyssItemLevelCeilingDefault = 90

	// abyssItemLevelLegacyValue 是唯一被承认的回退字面量。
	abyssItemLevelLegacyValue = "legacy"

	// abyssItemLevelSource 是"等级从哪来"的可判读取值（进事件）。
	abyssItemLevelSource = "epicpieceinfo_piece_list_output_equipment_minimum_level"
)

// abyssItemLevelBand 是生效中的物品等级带（`Active=false` ⇒ 逐字节等于今天）。
type abyssItemLevelBand struct {
	Floor              int64
	Ceiling            int64
	Active             bool
	DefaultConfig      bool  // 仅两个原始配置均为空；不能用日志哨兵"-"代替。
	GradeMin, GradeMax int64 // 非零表示低级图使用真实成品grade过滤。
	// FloorSwitch / CeilingSwitch 是操作者写的原始取值（未设置 = `-`）。
	FloorSwitch   string
	CeilingSwitch string
}

// readAbyssItemLevelBand 解析两个取值。**只有字面量 `legacy` 才回退**；
// 未设置/空/任意整数都取默认档（与 `readDungeonHellRoomMode` 同一约定）。
func readAbyssItemLevelBand(floorValue, ceilingValue string) abyssItemLevelBand {
	band := abyssItemLevelBand{
		Floor:         abyssItemLevelFloorDefault,
		Ceiling:       abyssItemLevelCeilingDefault,
		Active:        true,
		DefaultConfig: strings.TrimSpace(floorValue) == "" && strings.TrimSpace(ceilingValue) == "",
		FloorSwitch:   abyssSwitchText(floorValue),
		CeilingSwitch: abyssSwitchText(ceilingValue),
	}
	if strings.EqualFold(strings.TrimSpace(floorValue), abyssItemLevelLegacyValue) ||
		strings.EqualFold(strings.TrimSpace(ceilingValue), abyssItemLevelLegacyValue) {
		band.Active = false
		band.Floor = 0
		band.Ceiling = 0
		return band
	}
	if parsed, ok := abyssItemLevelFromValue(floorValue); ok {
		band.Floor = parsed
	}
	if parsed, ok := abyssItemLevelFromValue(ceilingValue); ok {
		band.Ceiling = parsed
	}
	if band.Ceiling < band.Floor {
		band.Ceiling = band.Floor
	}
	return band
}

func abyssItemLevelFromValue(value string) (int64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 || parsed > 200 {
		return 0, false
	}
	return parsed, true
}

func abyssSwitchText(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return trimmed
}

// abyssItemLevelBandFromEnv 在**消费点**自读两个开关。
func abyssItemLevelBandFromEnv() abyssItemLevelBand {
	return readAbyssItemLevelBand(os.Getenv(abyssItemLevelFloorEnv), os.Getenv(abyssItemLevelCeilingEnv))
}

// forDungeon only adapts the unconfigured default. Explicit operator bands
// remain authoritative; missing metadata never broadens the candidate set.
func (band abyssItemLevelBand) forDungeon(minimum, basis int64) abyssItemLevelBand {
	if !band.Active || !band.DefaultConfig ||
		minimum <= 0 || basis <= 0 || minimum >= abyssItemLevelFloorDefault || basis >= abyssItemLevelFloorDefault {
		return band
	}
	lo, hi, ok := hellPartyRewardGradeRangeForLevels(minimum, basis)
	if !ok {
		return band
	}
	band.Floor, band.Ceiling = 1, hellPartyRewardPoolGradeCeiling
	band.GradeMin, band.GradeMax = lo, hi
	return band
}

// admits 报告"等级已知的这个数值在不在生效带内"（回退档下恒 true）。
func (band abyssItemLevelBand) admits(level int64) bool {
	if !band.Active {
		return true
	}
	return level >= band.Floor && level <= band.Ceiling
}

// permitsFragmentLevel 是**碎片路径**的判据（fail-closed：等级未知 ⇒ 拒绝）。
func (band abyssItemLevelBand) permitsFragmentLevel(level int64) bool {
	if !band.Active {
		return true
	}
	return level > 0 && band.admits(level)
}

// permitsEquipmentLevel 保留高级兼容策略；低级适配不接受未知等级。
func (band abyssItemLevelBand) permitsEquipmentLevel(level int64) bool {
	if !band.Active {
		return true
	}
	if band.GradeMin > 0 {
		return level > 0 && band.admits(level)
	}
	return level <= 0 || band.admits(level)
}

// String 是事件字段 `item_level_band=` 的取值。
func (band abyssItemLevelBand) String() string {
	if !band.Active {
		return abyssItemLevelLegacyValue
	}
	if band.GradeMin > 0 {
		return fmt.Sprintf("dungeon_grade:%d-%d", band.GradeMin, band.GradeMax)
	}
	return fmt.Sprintf("%d-%d", band.Floor, band.Ceiling)
}

// ── 碎片 → 成品装备等级索引（每个 monster catalog 只建一次）──────────────────

// currentAbyssEpicPieceLevelIndex 是"碎片 itemID → 成品装备 `[minimum level]`"。
type currentAbyssEpicPieceLevelIndex struct {
	levels map[uint32]int64
	grades map[uint32]int64
	// Resolved / Unresolved 是台账（进事件，让"等级读不出来多少件"一眼可见）。
	Resolved   int
	Unresolved int
}

var currentAbyssEpicPieceLevelIndexes sync.Map

// currentAbyssEpicPieceLevelIndexFor 建/取索引。扫描口径与
// `loadHellPartyRewardEquipmentPool` 完全相同（`equipment/equipment.lst` → `equipment/<path>`），
// 解析口径与 `dungeon_drop_catalog.go` 相同（`[minimum level]`）。
//
// 键必须是 `*pvfDungeonMonsterCatalog` 指针（与 `currentEpicPieceCatalogs` 同一原因：
// 测试源的 `dnfpvf.Source` 可能是不可比较的 map）。
func currentAbyssEpicPieceLevelIndexFor(
	monsterCatalog *pvfDungeonMonsterCatalog,
) (*currentAbyssEpicPieceLevelIndex, error) {
	if monsterCatalog == nil || monsterCatalog.source == nil {
		return nil, errDungeonDropSourceRequired
	}
	if cached, found := currentAbyssEpicPieceLevelIndexes.Load(monsterCatalog); found {
		entry := cached.(currentAbyssEpicPieceLevelIndexEntry)
		return entry.index, entry.err
	}
	index, err := loadCurrentAbyssEpicPieceLevelIndex(monsterCatalog)
	entry := currentAbyssEpicPieceLevelIndexEntry{index: index, err: err}
	actual, _ := currentAbyssEpicPieceLevelIndexes.LoadOrStore(monsterCatalog, entry)
	return actual.(currentAbyssEpicPieceLevelIndexEntry).index, actual.(currentAbyssEpicPieceLevelIndexEntry).err
}

type currentAbyssEpicPieceLevelIndexEntry struct {
	index *currentAbyssEpicPieceLevelIndex
	err   error
}

func loadCurrentAbyssEpicPieceLevelIndex(
	monsterCatalog *pvfDungeonMonsterCatalog,
) (*currentAbyssEpicPieceLevelIndex, error) {
	catalog, err := currentEpicPieceCatalogFor(monsterCatalog)
	if err != nil {
		return nil, err
	}
	source := monsterCatalog.source
	text, err := source.ReadText(dungeonDropEquipmentList)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dungeonDropEquipmentList, err)
	}
	document, err := dnfpvf.Parse(dungeonDropEquipmentList, text)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dungeonDropEquipmentList, err)
	}
	paths := make(map[int64]string, 1024)
	for _, entry := range dnfpvf.ParseList(document) {
		clean := cleanDungeonDropPath(entry.Path)
		if entry.ID <= 0 || clean == "" {
			continue
		}
		if _, exists := paths[entry.ID]; exists {
			continue
		}
		paths[entry.ID] = clean
	}
	index := &currentAbyssEpicPieceLevelIndex{levels: make(map[uint32]int64, catalog.count()), grades: make(map[uint32]int64, catalog.count())}
	for _, piece := range catalog.pieces {
		output, found := catalog.outputFor(piece)
		if !found || output == 0 {
			index.Unresolved++
			continue
		}
		relative, found := paths[int64(output)]
		if !found {
			index.Unresolved++
			continue
		}
		_, body, readErr := readDungeonDropText(source, hellPartyRewardEquipmentListRoot, relative)
		if readErr != nil {
			index.Unresolved++
			continue
		}
		parsed, parseErr := dnfpvf.Parse(relative, body)
		if parseErr != nil {
			index.Unresolved++
			continue
		}
		level, found := parsed.Int("minimum level")
		if !found || level <= 0 {
			index.Unresolved++
			continue
		}
		index.levels[piece] = level
		grade, gradeOK := parsed.Int("grade")
		rarity, rarityOK := parsed.Int("rarity")
		if gradeOK && grade > 0 && grade <= hellPartyRewardPoolGradeCeiling && rarityOK && rarity == 4 {
			index.grades[piece] = grade
		}
		index.Resolved++
	}
	if index.Resolved == 0 {
		return nil, fmt.Errorf("%w: no [minimum level] resolved for %s", errDungeonDropItemUnresolved, currentEpicPieceInfoPath)
	}
	return index, nil
}

// levelOf 返回碎片成品装备的 `[minimum level]`。
func (index *currentAbyssEpicPieceLevelIndex) levelOf(pieceID uint32) (int64, bool) {
	if index == nil {
		return 0, false
	}
	level, found := index.levels[pieceID]
	return level, found
}

// abyssItemLevelList 把"这一抽选中的每一件的等级"写成事件字段（与 `fragments=` 同序）。
func abyssItemLevelList(levels []int64) string {
	if len(levels) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(levels))
	for _, level := range levels {
		parts = append(parts, strconv.FormatInt(level, 10))
	}
	return strings.Join(parts, ",")
}

func abyssLevelResolvedCount(index *currentAbyssEpicPieceLevelIndex) int {
	if index == nil {
		return 0
	}
	return index.Resolved
}

func abyssLevelUnresolvedCount(index *currentAbyssEpicPieceLevelIndex) int {
	if index == nil {
		return 0
	}
	return index.Unresolved
}

// currentAbyssEpicPieceLevelCandidates 按生效等级带过滤 `[piece list]`：
// 回退档（`Active=false`）逐字节返回原顺序的全部候选（今天的抽法）。
//
// 返回 (候选, 台账文字, 错误)：台账是"全量多少件 / 带内多少件 / 等级未知多少件"，
// 直接进事件，所以"为什么这一抽只可能是 85-90"在日志里可复核。
func currentAbyssEpicPieceLevelCandidates(
	monsterCatalog *pvfDungeonMonsterCatalog,
	catalog *currentEpicPieceCatalog,
	band abyssItemLevelBand,
) ([]uint32, *currentAbyssEpicPieceLevelIndex, error) {
	if catalog == nil || catalog.count() == 0 {
		return nil, nil, errCurrentEpicPieceCatalogEmpty
	}
	if !band.Active {
		// 回退档：不建索引、不读任何 `.equ`，逐字节等于今天。
		return catalog.pieces, nil, nil
	}
	index, err := currentAbyssEpicPieceLevelIndexFor(monsterCatalog)
	if err != nil {
		return nil, nil, err
	}
	filtered := make([]uint32, 0, len(catalog.pieces))
	for _, piece := range catalog.pieces {
		level, found := index.levelOf(piece)
		if !found {
			continue
		}
		if band.GradeMin > 0 {
			grade, known := index.grades[piece]
			if !known || grade < band.GradeMin || grade > band.GradeMax {
				continue
			}
		}
		if band.permitsFragmentLevel(level) {
			filtered = append(filtered, piece)
		}
	}
	return filtered, index, nil
}
