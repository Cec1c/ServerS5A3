package dnfbridge

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件是操作者缺陷「**深渊派对掉落了 lv80 的史诗碎片，这边应该没有 lv80 的才对，
// 应该是 85-90**」的修复，**同时**回答操作者追加的问题
// 「**现在深渊会掉落 lv80 的碎片，那会不会掉落 lv80 的史诗呢**」。
//
// ── 两条路径的等级约束（真档实测，见 `_dist/abyss-round3.md` §4）──────────────
//
//	碎片路径（`dungeon_hell_party_epic_fragment.go`）：
//	    候选 = `etc/epicpieceinfo.etc [piece list]` 的**全部 966 件**，
//	    抽法是 `pieces[rng.Next(len(pieces))]`（均匀、**完全不看等级**）。
//	    每件碎片的等级 = 它的 `[equipment recipe info]` 成品装备（`[piece list]` 的
//	    左列 itemID）在 `equipment/<path>.equ` 里的 `[minimum level]`。
//	    真档实测（live Script.pvf, sha256 afa00e75…）：
//	      `[minimum level]` 直方图 = 65:67 70:200 75:195 **80:256** **85:138** **90:110**
//	    ⇒ 966 件里有 **256 件是 lv80**（26.5%）—— 这就是操作者看到的那一件。
//	    **这条路径既没有副本等级约束，也没有物品等级约束**（把 65/70/75 的碎片也一起发了）。
//
//	史诗装备路径（`dungeon_hell_party_reward_pool.go` 的 `.equ` 池）：
//	    等级区间 = 厂商 `EquipmentDropLevelRule`（`[min(minLv,basis), basis+7]` **grade 闭区间**），
//	    副本 310 的 `[minimum required level]=87` / `[basis level]=90` ⇒ `[87,97]`。
//	    真档实测该区间六个 rarity 桶的候选装备 `[minimum level]`：
//	      rarity=4（史诗）248 件 = `85:138 90:110`（grade `91:138 96:110`）
//	      rarity=3 429 件 = `85:313 90:116`、rarity=2 135 件 = `85:67 90:68`
//	      rarity=1 68 件 = `90:68`、rarity=0 68 件 = `90:68`、rarity=5 0 件
//	    ⇒ **一个候选的 `[minimum level]` 都不低于 85**：这份 PVF 里史诗装备的 grade 只有
//	    91/96 两档（= lv85 / lv90），区间下界 87 把 grade 86（= lv80 史诗）挡在门外。
//	    但那**不是**全局保证，只是"副本 310 的 `[minimum required level]` 恰好是 87"：
//	    换一个 `[minimum required level] <= 86` 的副本，同一个函数就会放进 lv80 史诗。
//	    本文件因此给**两条路径**都加上同一条**按物品等级的显式地板**。
//
// ── 开关（house idiom：默认 = 修复，消费点自读 env，`options.go` 未动）────────
//
// DNFBRIDGE_ABYSS_ITEM_LEVEL_FLOOR / DNFBRIDGE_ABYSS_ITEM_LEVEL_CEILING
//
//	未设置 / 空 / 任意整数 = 生效档（默认 85 / 90，操作者口径「应该是 85-90」）。
//	字面量 `legacy`（任一个即可）= **两处都回到今天**：碎片回调全 966 件均匀抽、
//	    装备池不加等级过滤（逐字节等于本改动前）。
//	非法取值（`80x`、负数、空串以外的怪值）按默认档处理，并把原始取值写进
//	    `item_level_floor_switch=` / `item_level_ceiling_switch=`，拼错不会被静默吞掉。
//
// 两条路径的语义差异（**刻意**，必须读）：
//
//	碎片：**fail-closed** —— 只接受"等级已知且在 [floor,ceiling] 内"的碎片。
//	    真档 966/966 都能解析出等级，所以线上没有一件碎片因此丢失；
//	    等级解析不出来的碎片**不发**（宁可少发也不发一件等级不明的）。
//	装备：**fail-open** —— 等级字段缺失（0 = 未知）的候选**不过滤**，只有"等级已知
//	    且低于地板"才剔除。理由是这条池子还有 fixture/降级口径在同一函数上跑，
//	    而真档该区间 948/948 候选都有等级（实测 `levels<=80=0`）⇒ 线上零行为差。
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
	Floor   int64
	Ceiling int64
	Active  bool
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

// permitsEquipmentLevel 是**装备池**的判据（fail-open：等级未知 ⇒ 放行；见文件头）。
func (band abyssItemLevelBand) permitsEquipmentLevel(level int64) bool {
	if !band.Active {
		return true
	}
	return level <= 0 || band.admits(level)
}

// String 是事件字段 `item_level_band=` 的取值。
func (band abyssItemLevelBand) String() string {
	if !band.Active {
		return abyssItemLevelLegacyValue
	}
	return fmt.Sprintf("%d-%d", band.Floor, band.Ceiling)
}

// ── 碎片 → 成品装备等级索引（每个 monster catalog 只建一次）──────────────────

// currentAbyssEpicPieceLevelIndex 是"碎片 itemID → 成品装备 `[minimum level]`"。
type currentAbyssEpicPieceLevelIndex struct {
	levels map[uint32]int64
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
	index := &currentAbyssEpicPieceLevelIndex{levels: make(map[uint32]int64, catalog.count())}
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
		if band.admits(level) {
			filtered = append(filtered, piece)
		}
	}
	return filtered, index, nil
}
