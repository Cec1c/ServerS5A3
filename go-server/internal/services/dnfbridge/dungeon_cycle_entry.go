package dnfbridge

import (
	"context"
	"fmt"
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
// 第 3 个整数是副本 id，第 4 个整数与真档每日任务逐字对应，且这些次数是**通关(clear)**
// 次数而不是入场次数：
//
//	`n_quest/new_quest/daily_90_legend_01.qst`（单人，`[dungeon info] 5097 -1 5098 -1`）
//	  `[grade] [daily mission]`、`[type] [hunt enemy]`、
//	  `[int data] 5097 -1 53764 3 3  5098 -1 107001865 1 1`
//	  （53764 = 5097 的 destroy-object 通关目标）、
//	  条件文案「通关3次『艾肯（单人） - 遗落的神殿』、 1次『艾肯（单人） - 试炼』。
//	  (通关3次『遗落的神殿』后会出现『试炼』地下城。)」
//	`n_quest/new_quest/daily_90_legend_cdnf_01.qst`（组队，`3570/3571`）同构，
//	  `[type] [condition under clear]`、`[int data] 3571 -1`，文案同样写「通关3次…1次」。
//
// 因此：
//
//  1. 次数在**通关**时提交（`consumeCurrentDungeonCycleClearCount`），中止/失败的入场不吃当天次数；
//  2. 入场仍要校验额度（用尽则拒绝读图），并在同一个事务里扣门票与入场房间疲劳
//     （`consumeCurrentDungeonCycleEntryAssets`）——门票仍是入场材料，真档在 prepare 阶段
//     只是被我们的旧顺序预扣，现在与疲劳同事务；
//  3. 第 2、5 个整数（-1/0 与 0/1）**仍未证**：C# 参考服务端没有 `[dungeon cycle]` 解析器，
//     当前 EXE 里也读不到任何 `dungeon cycle`/`dungeoncyclesystem` 字符串（ASCII 与 UTF-16 都为零命中），
//     即客户端根本不打开这张表；这里只使用第 4 个整数，其余位置不解释。
//
// 与魔界裂缝 316（次数与票本来就在一个事务里）以及异次元裂缝共享池（按入场计，见
// `etc/dungeonetc/dimensiondungeoninfo.etc` 的 `[hidden boss appear rate by enter count]`）都不同。
const (
	currentDungeonCycleConfigPath      = "etc/dungeoncyclesystem.etc"
	currentDungeonCycleSection         = "dungeon cycle"
	currentDungeonCycleRowLength       = 5
	currentDungeonCycleDungeonIndex    = 2
	currentDungeonCycleEnterCountIndex = 3
)

// currentDungeonCycleDailyLimits 只读真档 `etc/dungeoncyclesystem.etc`。
// declared 为假表示该资产没有这张表（不同分支的 PVF 可能没有），此时不施加任何规则；
// 表存在但行不完整/额度非正/副本 id 重复时返回错误，由调用方 fail closed。
func (s *Service) currentDungeonCycleDailyLimits() (map[int64]int64, bool, error) {
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
	return parseCurrentDungeonCycleDailyLimits(text)
}

// parseCurrentDungeonCycleDailyLimits 是纯解析：5 个整数一行、第 3 个是副本 id、第 4 个是该
// 副本的每日通关次数。缺段 = 没有声明（返回空表、declared 真）；行不完整、额度非正或副本 id
// 重复都硬失败，绝不猜一个额度出来。
func parseCurrentDungeonCycleDailyLimits(text string) (map[int64]int64, bool, error) {
	document, err := dnfpvf.Parse(currentDungeonCycleConfigPath, text)
	if err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", currentDungeonCycleConfigPath, err)
	}
	values := document.Ints(currentDungeonCycleSection)
	if len(values) == 0 {
		return map[int64]int64{}, true, nil
	}
	if len(values)%currentDungeonCycleRowLength != 0 {
		return nil, true, fmt.Errorf("%s [%s] has %d integers, which is not a whole number of rows", currentDungeonCycleConfigPath, currentDungeonCycleSection, len(values))
	}
	limits := make(map[int64]int64, len(values)/currentDungeonCycleRowLength)
	for row := 0; row < len(values); row += currentDungeonCycleRowLength {
		dungeonID := values[row+currentDungeonCycleDungeonIndex]
		enterCount := values[row+currentDungeonCycleEnterCountIndex]
		if dungeonID <= 0 || enterCount <= 0 {
			return nil, true, fmt.Errorf("%s [%s] row %d declares dungeon %d with daily count %d", currentDungeonCycleConfigPath, currentDungeonCycleSection, row/currentDungeonCycleRowLength, dungeonID, enterCount)
		}
		if _, exists := limits[dungeonID]; exists {
			return nil, true, fmt.Errorf("%s [%s] declares dungeon %d twice", currentDungeonCycleConfigPath, currentDungeonCycleSection, dungeonID)
		}
		limits[dungeonID] = enterCount
	}
	return limits, true, nil
}

// validateCurrentDungeonCycleDailyCount 只判每日次数；票与等级等仍由既有准入链负责。
func (s *Service) validateCurrentDungeonCycleDailyCount(character dnfrepo.CharacterRecord, dungeonID, limit int64) error {
	used, err := dnfdungeon.DailyEntryUsed(character, dungeonID, s.gameplayNow())
	if err != nil {
		return err
	}
	if used >= limit {
		return dnfdungeon.ErrDailyEntryLimit
	}
	return nil
}

// currentDungeonCycleDailyCountForDungeon 供准入链查询：返回 (额度, 是否声明, 错误)。
func (s *Service) currentDungeonCycleDailyCountForDungeon(dungeonID int64) (int64, bool, error) {
	limits, declared, err := s.currentDungeonCycleDailyLimits()
	if err != nil || !declared {
		return 0, false, err
	}
	limit, ok := limits[dungeonID]
	return limit, ok, nil
}

// consumeCurrentDungeonCycleEntryAssets 在**入场**只提交门票与入场房间疲劳（同一个角色事务），
// 不写每日次数；门票只在既有锦标赛开关打开时扣除（与改动前的条件/数量逐字一致）。
func (s *Service) consumeCurrentDungeonCycleEntryAssets(session *gameSession, runtime *runtimeDungeonState, assets currentDailyCountEntryAssets) (dnfrepo.CharacterRecord, []dnfdungeon.EntryItemConsumedStack, error) {
	if runtime == nil || s == nil || s.options.currentClientVersion != "official-24.9" {
		return dnfrepo.CharacterRecord{}, nil, nil
	}
	limits, declared, err := s.currentDungeonCycleDailyLimits()
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	if !declared {
		return dnfrepo.CharacterRecord{}, nil, nil
	}
	limit, cycleDungeon := limits[runtime.Dungeon.ID]
	if !cycleDungeon {
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
		CharacterID:   strconv.Itoa(int(session.selectedCharacterID)),
		Requirements:  assets.requirements,
		UpdatedAt:     s.gameplayNow(),
		Project:       assets.project,
		InTransaction: assets.inTransaction,
		// 通关计数的额度只在事务内复核（不在这里自增），避免 prepare 与 commit 之间的窗口。
		AllowanceDungeonID: runtime.Dungeon.ID,
		AllowanceLimit:     limit,
	})
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	s.logGameEvent(session, "game-dungeon-cycle-entry-assets-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"daily_limit", limit,
		"counts_on", "clear",
		"entry_items", len(result.Consumed),
		"fatigue_commit", assets.debitRoomFatigue,
		"source", currentDungeonCycleConfigPath)
	return result.Character, result.Consumed, nil
}

// consumeCurrentDungeonCycleClearCount 在**通关**时记一次当日通关次数（不扣门票、不发奖励）。
// 调用方（boss die check 结算）保证每轮只调用一次。
func (s *Service) consumeCurrentDungeonCycleClearCount(session *gameSession, runtime *runtimeDungeonState) error {
	if runtime == nil || s == nil || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	limit, declared, err := s.currentDungeonCycleDailyCountForDungeon(runtime.Dungeon.ID)
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
		DungeonID:   runtime.Dungeon.ID,
		Limit:       limit,
		UpdatedAt:   s.gameplayNow(),
	})
	if err != nil {
		return err
	}
	runtime.Character = result.Character
	s.logGameEvent(session, "game-dungeon-cycle-clear-count-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"used", result.Used,
		"limit", limit,
		"counts_on", "clear",
		"source", currentDungeonCycleConfigPath,
		"reset", "06:00_UTC+8")
	return nil
}
