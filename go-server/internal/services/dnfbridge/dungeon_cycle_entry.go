package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ── 艾肯副本（`[dungeon cycle]`）每日入场次数 ────────────────────────────────────
//
// DOC-094：「角色 小小栗子 艾肯地下城不会消耗进场次数 艾肯装备升级没反应」。
//
// 真档 PVF 证据 `etc/dungeoncyclesystem.etc`（四份现存 Script.pvf 逐字节一致）：
//
//	[dungeon cycle]
//	45 -1 5097 3 0  45 -1 5098 1 1  45 0 3570 3 0  45 0 3571 1 1
//	[/dungeon cycle]
//
// 行是 5 个整数一组，第 3 个整数是副本 id，紧跟其后的是该副本的每日入场次数：
//
//	5097 `dungeon/Legend/Ekern/Warriors/Warriors.dgn`（艾肯（单人）- 遗落的神殿）→ 3
//	5098 `dungeon/Legend/Ekern/Duel/Ekern2nd.dgn`（艾肯（单人）- 试炼）→ 1
//	3570 `dungeon/Legend/Ekern/Warriors/CDNF_Party_Warriors.dgn`（组队版遗落的神殿）→ 3
//	3571 `dungeon/Legend/Ekern/Duel/CDNF_Party_Ekern2nd.dgn`（组队版试炼）→ 1
//
// 单人与组队版本同一内容取同一额度（5097/3570 = 3，5098/3571 = 1），与该文件只覆盖区域 45
// 「魔界集结地」(`worldmap/Legend.wdm` 的 5097/5098/3570/3571) 完全吻合；四份真档一致说明这
// 不是某一版的临时数据。第 2、5 个整数（-1/0 与 0/1）本仓**没有**可引用的解析器，因此这里
// 只使用"副本 id 后一位是额度"这一条，其余位置不解释、不臆造——额度取不到时 fail closed。
//
// 该规则与魔界裂缝 316（`[dimension dungeon]` 之外的独立规则）以及异次元裂缝共享池都不同：
// 艾肯按副本各记一份日计数，只复用既有的 `dungeon_daily_entry_<id>` 角色计数器。
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
// 副本的每日入场次数。缺段 = 没有声明（返回空表、declared 真）；行不完整、额度非正或副本 id
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

// consumeCurrentDungeonCycleDailyEntry 在运行时准备完成、活动运行闸门已持有时，把该副本的
// 日计数与同批入场资产（门票、入场房间疲劳）放进同一个角色事务提交；调用方
// `consumeCurrentDailyCountEntry` 负责槽位刷新与疲劳房间标记。
//
// 门票只在既有锦标赛开关打开时随本事务扣除（与改动前 prepare 阶段的消耗条件/数量一致）；
// 开关关闭时一行都不扣，与改动前相同。
func (s *Service) consumeCurrentDungeonCycleDailyEntry(session *gameSession, runtime *runtimeDungeonState, assets currentDailyCountEntryAssets) (dnfrepo.CharacterRecord, []dnfdungeon.EntryItemConsumedStack, error) {
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
	result, err := owner.ConsumeDailyEntry(ctx, dnfdungeon.DailyEntryCommand{
		CharacterID:   strconv.Itoa(int(session.selectedCharacterID)),
		DungeonID:     runtime.Dungeon.ID,
		Limit:         limit,
		Requirements:  assets.requirements,
		UpdatedAt:     s.gameplayNow(),
		Project:       assets.project,
		InTransaction: assets.inTransaction,
	})
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	s.logGameEvent(session, "game-dungeon-cycle-daily-entry-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"used", result.Used,
		"limit", limit,
		"source", currentDungeonCycleConfigPath,
		"entry_items", len(result.Consumed),
		"fatigue_commit", assets.debitRoomFatigue,
		"reset", "06:00_UTC+8")
	return result.Character, result.Consumed, nil
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
