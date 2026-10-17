package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Cycle progress and admission allowances are distinct. Current NOTI656
// projects A=successful clears and C=committed entry consumption. Native
// aggregation shares solo/team by the PVF Order. The admission policy implements
// the user's reported entry-count requirement; no historical abandoned entries
// are fabricated. Gear upgrading remains deferred.
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
	rows, declared, err := s.currentDungeonCycleRows()
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
	rule, found, err := s.currentCycleAdmission(requirement.DungeonID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cycle allowance disappeared")
	}
	return dnfdungeon.ValidateCycleEntryAllowance(character, rule, s.gameplayNow())
}

// consumeCurrentDungeonCycleEntryAssets 在入场时提交独立次数、门票与房间疲劳，
// 同一事务复核共享额度和通关前置；失败整批回滚，成功通关进度另行记录。
func (s *Service) consumeCurrentDungeonCycleEntryAssets(session *gameSession, runtime *runtimeDungeonState, assets currentDailyCountEntryAssets) (dnfrepo.CharacterRecord, []dnfdungeon.EntryItemConsumedStack, error) {
	if runtime == nil || s == nil || s.options.currentClientVersion != "official-24.9" {
		return dnfrepo.CharacterRecord{}, nil, nil
	}
	rule, found, err := s.currentCycleAdmission(runtime.Dungeon.ID)
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	if !found {
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
	if err := ensureCurrentDungeonCycleClearNonce(runtime); err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	at := s.gameplayNow()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ConsumeDailyEntry(ctx, dnfdungeon.DailyEntryCommand{
		CharacterID: strconv.Itoa(int(session.selectedCharacterID)), DungeonID: runtime.Dungeon.ID, Limit: rule.Limit,
		Requirements: assets.requirements, Project: assets.project, UpdatedAt: at, CycleEntry: true,
		ClearNonceHi: runtime.dungeonCycleClearNonceHi, ClearNonceLo: runtime.dungeonCycleClearNonceLo,
		InTransaction: func(character *dnfrepo.CharacterRecord) error {
			if err := dnfdungeon.ValidateCycleEntryAllowance(*character, rule, at); err != nil {
				return err
			}
			if assets.inTransaction != nil {
				return assets.inTransaction(character)
			}
			return nil
		},
	})
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	s.logGameEvent(session, "game-dungeon-cycle-entry-committed", "dungeon_id", runtime.Dungeon.ID, "used", result.Used, "limit", rule.Limit, "counts_on", "entry", "entry_items", len(result.Consumed))
	return result.Character, result.Consumed, nil
}

// consumeCurrentDungeonCycleClearCount 在**通关**时记一次当日通关次数（不扣门票、不发奖励）。
// ensureCurrentDungeonCycleClearNonce 给本轮生成一次**独立随机**的通关计数 nonce 并固定在本轮运行时上：
// 生成一次后所有重试都用同一对值（幂等回放），不同会话/不同轮次必然不同。
// 用 128bit（两个 int64 半段）而不是单个 64bit：同一天内多轮也几乎不可能撞号；全零视为未生成，重取。
func ensureCurrentDungeonCycleClearNonce(runtime *runtimeDungeonState) error {
	if runtime == nil {
		return nil
	}
	if runtime.dungeonCycleClearNonceHi != 0 || runtime.dungeonCycleClearNonceLo != 0 {
		return nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return fmt.Errorf("generate dungeon cycle clear nonce: %w", err)
		}
		hi := int64(binary.LittleEndian.Uint64(raw[0:8]))
		lo := int64(binary.LittleEndian.Uint64(raw[8:16]))
		if hi == 0 && lo == 0 {
			continue
		}
		runtime.dungeonCycleClearNonceHi, runtime.dungeonCycleClearNonceLo = hi, lo
		return nil
	}
	return fmt.Errorf("generate dungeon cycle clear nonce: zero value repeated")
}

// 每轮 nonce 由 ensureCurrentDungeonCycleClearNonce 独立随机生成：同一轮的重试是幂等回放只计一次，
// 不同会话/不同轮次不会撞号（重登后的新一轮一定计数）。
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
	if err := ensureCurrentDungeonCycleClearNonce(runtime); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ConsumeDailyEntry(ctx, dnfdungeon.DailyEntryCommand{
		CharacterID:  strconv.Itoa(int(session.selectedCharacterID)),
		DungeonID:    requirement.DungeonID,
		Limit:        requirement.DailyCount,
		UpdatedAt:    s.gameplayNow(),
		ClearNonceHi: runtime.dungeonCycleClearNonceHi,
		ClearNonceLo: runtime.dungeonCycleClearNonceLo,
	})
	if err != nil {
		return err
	}
	runtime.Character = result.Character
	s.refreshCurrentDungeonCycleInfo(session, runtime, "clear_commit")
	s.logGameEvent(session, "game-dungeon-cycle-clear-count-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"used", result.Used,
		"limit", requirement.DailyCount,
		"counts_on", "clear",
		"clear_nonce_hi", runtime.dungeonCycleClearNonceHi,
		"clear_nonce_lo", runtime.dungeonCycleClearNonceLo,
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
