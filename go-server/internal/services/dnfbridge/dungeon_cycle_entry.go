package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ── 艾肯副本（`[dungeon cycle]`）每日次数 ──────────────────────────────────────
//
// DOC-094：「角色 小小栗子 艾肯地下城不会消耗进场次数 艾肯装备升级没反应」。
//
// 真档 PVF 证据 `etc/dungeoncyclesystem.etc`（四份现存 Script.pvf 逐字节一致）：
//
//	[dungeon cycle]
//	45 -1 5097 3 0  45 -1 5098 1 1  45 0 3570 3 0  45 0 3571 1 1
//	[/dungeon cycle]
//
// 次数是**通关(clear)**次数而不是入场次数，且试炼有前置：
//
//	`n_quest/new_quest/daily_90_legend_01.qst`（单人，`[dungeon info] 5097 -1 5098 -1`）
//	  `[grade] [daily mission]`、`[type] [hunt enemy]`、
//	  `[int data] 5097 -1 53764 3 3  5098 -1 107001865 1 1`
//	  （53764 = 5097 的 destroy-object 通关目标），条件文案「通关3次『艾肯（单人） - 遗落的神殿』、
//	  1次『艾肯（单人） - 试炼』。(通关3次『遗落的神殿』后会出现『试炼』地下城。)」；
//	`n_quest/new_quest/daily_90_legend_cdnf_01.qst`（组队 3570/3571）同构：
//	  `[type] [condition under clear]`、`[int data] 3571 -1`，文案同样写「通关3次…1次」。
//
// 服务端据此执行两条已证准入：
//
//  1. 5098 需要同服务日已通关 5097 三次、3571 需要已通关 3570 三次（试炼只在前置满三次后
//     出现）。校验既在读图前的准入链里，也在门票/疲劳的同一个事务里复核（失败不扣任何资产）。
//  2. 次数在**通关**时提交，且每轮只计一次：入场只扣门票与入场房间疲劳；中止的入场不吃次数。
//     通关计数用每轮 nonce 做幂等回放，提交失败时**不确认这一轮**（调用方不进入结算/不发 op115），
//     客户端重传会重试；重试成功只计一次，失败也不会绕过每日上限。
//
// 第 2、5 个整数（-1/0 与 0/1）的**原生**含义仍未证（C# 参考服务端没有 `[dungeon cycle]` 解析器；
// 当前 EXE 里这些标签没有以明文出现，但字符串池/哈希仍可能间接持有，因此也不能据此断定客户端
// 不读该表）。本文件只使用「副本 id 后一位是当日通关上限」与「同 worldmap、同第 2 列分组内，
// 第 5 列=1 的副本以第 5 列=0 的副本为前置，所需次数取其第 4 列」这两条**由真档任务文案钉住的**
// 结构性关系，其余位置不解释。
const (
	currentDungeonCycleConfigPath      = "etc/dungeoncyclesystem.etc"
	currentDungeonCycleSection         = "dungeon cycle"
	currentDungeonCycleRowLength       = 5
	currentDungeonCycleDungeonIndex    = 2
	currentDungeonCycleEnterCountIndex = 3
	currentDungeonCycleGroupKeyIndex   = 1
	currentDungeonCycleOrderIndex      = 4
)

// currentDungeonCycleRow 是一行 `[dungeon cycle]` 的结构化读法（含未解释的两列）。
type currentDungeonCycleRow struct {
	WorldMapIndex int64
	GroupKey      int64
	DungeonID     int64
	DailyCount    int64
	Order         int64
}

// currentDungeonCycleRequirement 是某个艾肯副本的准入与计数要求。
type currentDungeonCycleRequirement struct {
	DungeonID              int64
	DailyCount             int64
	PrerequisiteDungeonID  int64
	PrerequisiteDailyCount int64
}

// currentDungeonCycleDailyLimits 只读真档 `etc/dungeoncyclesystem.etc`。
// declared 为假表示该资产没有这张表（不同分支的 PVF 可能没有），此时不施加任何规则；
// 表存在但行不完整/额度非正/副本 id 重复时返回错误，由调用方 fail closed。
func (s *Service) currentDungeonCycleDailyLimits() (map[int64]int64, bool, error) {
	requirements, declared, err := s.currentDungeonCycleRequirements()
	if err != nil || !declared {
		return nil, declared, err
	}
	limits := make(map[int64]int64, len(requirements))
	for dungeonID, requirement := range requirements {
		limits[dungeonID] = requirement.DailyCount
	}
	return limits, true, nil
}

// currentDungeonCycleRequirements 读出全部行并推导每个副本的每日次数与试炼前置。
func (s *Service) currentDungeonCycleRequirements() (map[int64]currentDungeonCycleRequirement, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil || archive == nil {
		return nil, false, nil
	}
	text, _, err := readInitialPVFText(archive, currentDungeonCycleConfigPath)
	if err != nil {
		return nil, false, nil
	}
	rows, declared, err := parseCurrentDungeonCycleRows(text)
	if err != nil || !declared {
		return nil, declared, err
	}
	return deriveCurrentDungeonCycleRequirements(rows), true, nil
}

// parseCurrentDungeonCycleRows 是纯解析：5 个整数一行。缺段 = 没有声明（empty、declared 真）；
// 行不完整、额度非正或副本 id 重复都硬失败，绝不猜一个额度出来。
func parseCurrentDungeonCycleRows(text string) ([]currentDungeonCycleRow, bool, error) {
	document, err := dnfpvf.Parse(currentDungeonCycleConfigPath, text)
	if err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", currentDungeonCycleConfigPath, err)
	}
	values := document.Ints(currentDungeonCycleSection)
	if len(values) == 0 {
		return nil, true, nil
	}
	if len(values)%currentDungeonCycleRowLength != 0 {
		return nil, true, fmt.Errorf("%s [%s] has %d integers, which is not a whole number of rows", currentDungeonCycleConfigPath, currentDungeonCycleSection, len(values))
	}
	rows := make([]currentDungeonCycleRow, 0, len(values)/currentDungeonCycleRowLength)
	seen := make(map[int64]bool, len(values)/currentDungeonCycleRowLength)
	for row := 0; row < len(values); row += currentDungeonCycleRowLength {
		entry := currentDungeonCycleRow{
			WorldMapIndex: values[row],
			GroupKey:      values[row+currentDungeonCycleGroupKeyIndex],
			DungeonID:     values[row+currentDungeonCycleDungeonIndex],
			DailyCount:    values[row+currentDungeonCycleEnterCountIndex],
			Order:         values[row+currentDungeonCycleOrderIndex],
		}
		if entry.DungeonID <= 0 || entry.DailyCount <= 0 {
			return nil, true, fmt.Errorf("%s [%s] row %d declares dungeon %d with daily count %d", currentDungeonCycleConfigPath, currentDungeonCycleSection, row/currentDungeonCycleRowLength, entry.DungeonID, entry.DailyCount)
		}
		if seen[entry.DungeonID] {
			return nil, true, fmt.Errorf("%s [%s] declares dungeon %d twice", currentDungeonCycleConfigPath, currentDungeonCycleSection, entry.DungeonID)
		}
		seen[entry.DungeonID] = true
		rows = append(rows, entry)
	}
	return rows, true, nil
}

// parseCurrentDungeonCycleDailyLimits 保留给既有测试与调用方：只要 id → 额度。
func parseCurrentDungeonCycleDailyLimits(text string) (map[int64]int64, bool, error) {
	rows, declared, err := parseCurrentDungeonCycleRows(text)
	if err != nil || !declared {
		return nil, declared, err
	}
	limits := make(map[int64]int64, len(rows))
	for _, row := range rows {
		limits[row.DungeonID] = row.DailyCount
	}
	return limits, true, nil
}

// deriveCurrentDungeonCycleRequirements 由表结构推导前置：同一 (worldmap, 第 2 列) 分组内，
// 第 5 列=1 的副本以第 5 列=0 的副本为前置，所需次数 = 前置副本的第 4 列（真档任务文案
// 「通关3次『遗落的神殿』后会出现『试炼』地下城」逐字对应 5098←5097≥3、3571←3570≥3）。
func deriveCurrentDungeonCycleRequirements(rows []currentDungeonCycleRow) map[int64]currentDungeonCycleRequirement {
	type groupKey struct {
		worldMap int64
		group    int64
	}
	groups := make(map[groupKey][]currentDungeonCycleRow, len(rows))
	for _, row := range rows {
		key := groupKey{worldMap: row.WorldMapIndex, group: row.GroupKey}
		groups[key] = append(groups[key], row)
	}
	requirements := make(map[int64]currentDungeonCycleRequirement, len(rows))
	for _, row := range rows {
		requirements[row.DungeonID] = currentDungeonCycleRequirement{DungeonID: row.DungeonID, DailyCount: row.DailyCount}
	}
	keys := make([]groupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].worldMap != keys[j].worldMap {
			return keys[i].worldMap < keys[j].worldMap
		}
		return keys[i].group < keys[j].group
	})
	for _, key := range keys {
		members := groups[key]
		var prerequisite *currentDungeonCycleRow
		for index := range members {
			if members[index].Order == 0 && (prerequisite == nil || members[index].DailyCount > prerequisite.DailyCount) {
				prerequisite = &members[index]
			}
		}
		if prerequisite == nil {
			continue
		}
		for _, member := range members {
			if member.Order == 0 {
				continue
			}
			entry := requirements[member.DungeonID]
			entry.PrerequisiteDungeonID = prerequisite.DungeonID
			entry.PrerequisiteDailyCount = prerequisite.DailyCount
			requirements[member.DungeonID] = entry
		}
	}
	return requirements
}

// currentDungeonCycleRequirementForDungeon 供准入链查询。
func (s *Service) currentDungeonCycleRequirementForDungeon(dungeonID int64) (currentDungeonCycleRequirement, bool, error) {
	requirements, declared, err := s.currentDungeonCycleRequirements()
	if err != nil || !declared {
		return currentDungeonCycleRequirement{}, false, err
	}
	requirement, ok := requirements[dungeonID]
	return requirement, ok, nil
}

// currentDungeonCycleDailyCountForDungeon 供既有调用方（ClearNonce 之外的只读查询）。
func (s *Service) currentDungeonCycleDailyCountForDungeon(dungeonID int64) (int64, bool, error) {
	requirement, ok, err := s.currentDungeonCycleRequirementForDungeon(dungeonID)
	if err != nil || !ok {
		return 0, false, err
	}
	return requirement.DailyCount, true, nil
}

// validateCurrentDungeonCycleDailyCount 只判每日次数与试炼前置；票与等级等仍由既有准入链负责。
func (s *Service) validateCurrentDungeonCycleDailyCount(character dnfrepo.CharacterRecord, requirement currentDungeonCycleRequirement) error {
	used, err := dnfdungeon.DailyEntryUsed(character, requirement.DungeonID, s.gameplayNow())
	if err != nil {
		return err
	}
	if used >= requirement.DailyCount {
		return dnfdungeon.ErrDailyEntryLimit
	}
	if requirement.PrerequisiteDungeonID > 0 && requirement.PrerequisiteDailyCount > 0 {
		cleared, err := dnfdungeon.DailyEntryUsed(character, requirement.PrerequisiteDungeonID, s.gameplayNow())
		if err != nil {
			return err
		}
		if cleared < requirement.PrerequisiteDailyCount {
			return dnfdungeon.ErrEntryPrerequisiteMissing
		}
	}
	return nil
}

// consumeCurrentDungeonCycleEntryAssets 在**入场**只提交门票与入场房间疲劳（同一个角色事务），
// 并在同一事务内复核每日次数与试炼前置；失败整批回滚，不扣任何资产。
func (s *Service) consumeCurrentDungeonCycleEntryAssets(session *gameSession, runtime *runtimeDungeonState, assets currentDailyCountEntryAssets) (dnfrepo.CharacterRecord, []dnfdungeon.EntryItemConsumedStack, error) {
	if runtime == nil || s == nil || s.options.currentClientVersion != "official-24.9" {
		return dnfrepo.CharacterRecord{}, nil, nil
	}
	requirement, declared, err := s.currentDungeonCycleRequirementForDungeon(runtime.Dungeon.ID)
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	if !declared {
		return dnfrepo.CharacterRecord{}, nil, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.CharacterAssets == nil {
		return dnfrepo.CharacterRecord{}, nil, dnfdungeon.ErrOwnerUnavailable
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ConsumeEntryAssets(ctx, dnfdungeon.EntryAssetsCommand{
		CharacterID:           strconv.Itoa(int(session.selectedCharacterID)),
		Requirements:          assets.requirements,
		UpdatedAt:             s.gameplayNow(),
		Project:               assets.project,
		InTransaction:         assets.inTransaction,
		AllowanceDungeonID:    requirement.DungeonID,
		AllowanceLimit:        requirement.DailyCount,
		PrerequisiteDungeonID: requirement.PrerequisiteDungeonID,
		PrerequisiteCount:     requirement.PrerequisiteDailyCount,
	})
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	s.logGameEvent(session, "game-dungeon-cycle-entry-assets-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"daily_limit", requirement.DailyCount,
		"counts_on", "clear",
		"prerequisite_dungeon", requirement.PrerequisiteDungeonID,
		"prerequisite_count", requirement.PrerequisiteDailyCount,
		"entry_items", len(result.Consumed),
		"fatigue_commit", assets.debitRoomFatigue,
		"source", currentDungeonCycleConfigPath)
	return result.Character, result.Consumed, nil
}

// consumeCurrentDungeonCycleClearCount 在**通关**时记一次当日通关次数（不扣门票、不发奖励）。
// 每轮 nonce = 运行时生命周期 token：同一个 nonce 的重试是幂等回放，只计一次。
func (s *Service) consumeCurrentDungeonCycleClearCount(session *gameSession, runtime *runtimeDungeonState) error {
	if runtime == nil || s == nil || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	requirement, declared, err := s.currentDungeonCycleRequirementForDungeon(runtime.Dungeon.ID)
	if err != nil {
		return err
	}
	if !declared {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.CharacterAssets == nil {
		return dnfdungeon.ErrOwnerUnavailable
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ConsumeDailyEntry(ctx, dnfdungeon.DailyEntryCommand{
		CharacterID: strconv.Itoa(int(session.selectedCharacterID)),
		DungeonID:   requirement.DungeonID,
		Limit:       requirement.DailyCount,
		UpdatedAt:   s.gameplayNow(),
		ClearNonce:  int64(runtime.lifecycleToken),
	})
	if err != nil {
		return err
	}
	runtime.Character = result.Character
	s.logGameEvent(session, "game-dungeon-cycle-clear-count-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"used", result.Used,
		"limit", requirement.DailyCount,
		"counts_on", "clear",
		"nonce", runtime.lifecycleToken,
		"source", currentDungeonCycleConfigPath,
		"reset", "06:00_UTC+8")
	return nil
}

// commitCurrentDungeonCycleClearCountLocked 是通关/结算路径上的**阻塞前置**：只有次数提交成功
// 才把这一轮标记为已计。失败时保持未标记并向上返回错误——调用方必须停止后续结算（不发 op115、
// 不进入结果流程），由客户端重传触发重试，因此持久化失败不会被当成成功，也不能绕过每日上限。
func (s *Service) commitCurrentDungeonCycleClearCountLocked(session *gameSession, runtime *runtimeDungeonState) error {
	if runtime == nil || runtime.dungeonCycleClearCountCommitted {
		return nil
	}
	if err := s.consumeCurrentDungeonCycleClearCount(session, runtime); err != nil {
		return err
	}
	runtime.dungeonCycleClearCountCommitted = true
	return nil
}
