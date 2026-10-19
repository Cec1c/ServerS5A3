package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ── 异次元裂缝（`[dimension dungeon]`）每日入场次数 ─────────────────────────────
//
// DOC-083：「地下城 异次元裂缝-异界的强者 … 通关后每日入场次数未扣除，入场材料消耗正常」。
//
// 真档 PVF 证据（`worldmap/DimensionGate.wdm` + 各 DGN）：区域 13「异次元裂缝」的 16 个副本
// 都声明 `[dimension dungeon]`：
//
//	7100 `dungeon/Impossible/dimension.dgn`（异次元裂缝 - 异界的强者）
//	7165-7167 `dungeon/DimensionCrack/Blackearth_michaela/*`（黑色大地外围，难度 0/1/2）
//	7168-7170 `dungeon/DimensionCrack/Blackearth/*`（黑色大地）
//	7171-7173 `dungeon/DimensionCrack/Behemoth/*`（罗特斯的堡垒）
//	7174-7176 `dungeon/DimensionCrack/Bakalcastle/*`（巴卡尔之城）
//	7177-7179 `dungeon/DimensionCrack/Lesphon/*`（痛苦之村列瑟芬）
//
// 额度取 `etc/dimensiongate.etc` 的 `[daily default enter count]`（真档 5）与
// `[daily default extra enter count]`（真档 0），由既有 `currentDimensionGateEntryLimitForSession`
// 读出——同一个 reader 已经在旧档通知路径上用过，出处与厂商 `DimensionGateEntryLimitConfigProvider`
// 一致。厂商 `DungeonEntryHandler.cs:4405-4432/4480-4519` 只按 `[dimension dungeon]` 判定，
// 不按副本 id 特判，因此这里同样只看 PVF 标记，**不写死 7100/7165-7179**。
//
// 魔界裂缝 316（`dungeon/MetroCenter/Hell/Hell.dgn`，type `particular hell`）**没有**该标记，
// 走的是既有的 `currentRiftEntryEnabled` 3 次规则，两者不得互相套用。
const currentDimensionGateDungeonPVFFlag = "dimension dungeon"

func (s *Service) currentDimensionGateDailyEntryEnabled(dungeon worldmap.Dungeon) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && dungeon.Metadata.Flags[currentDimensionGateDungeonPVFFlag]
}

// currentDimensionGateDailyEntryLimit 返回共享池的初始额度（不含用券获得的追加次数）。
// 读不到正额度时拒绝入场，而不是静默放行。
func (s *Service) currentDimensionGateDailyEntryLimit() (int64, string, error) {
	limit := s.currentDimensionGateEntryLimitForSession()
	total := int64(limit.enterCount) + int64(limit.extraCount)
	if total <= 0 {
		return 0, limit.source, fmt.Errorf("dimension gate daily enter limit is not positive: %d/%d source=%s", limit.enterCount, limit.extraCount, limit.source)
	}
	return total, limit.source, nil
}

const currentDimensionDungeonInfoPath = "etc/dungeonetc/dimensiondungeoninfo.etc"

// The new-family maxima are ceilings, not free entries. The local ticket PVF
// explicitly says at most five extra grants per day, reset at 06:00.
func parseCurrentDimensionGateExtraLimit(text string, base int64) (int64, error) {
	doc, err := dnfpvf.Parse(currentDimensionDungeonInfoPath, text)
	if err != nil {
		return 0, err
	}
	read := func(name string) (int64, error) {
		count := 0
		for _, section := range doc.Sections {
			if section.Name == name {
				count++
			}
		}
		values, ok := doc.Section(name)
		if count != 1 || !ok || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int < 0 {
			return 0, fmt.Errorf("invalid dimension entry policy: %s", name)
		}
		return values[0].Int, nil
	}
	maximum, err := read("max daily enter count")
	if err != nil {
		return 0, err
	}
	extra, err := read("max extra enter count")
	if err != nil {
		return 0, err
	}
	if base <= 0 || maximum < base || extra > maximum-base {
		return 0, fmt.Errorf("inconsistent dimension entry policy: base=%d max=%d extra=%d", base, maximum, extra)
	}
	return extra, nil
}

func (s *Service) currentDimensionGateDailyExtraLimit(base int64) (int64, error) {
	if s == nil {
		return 0, dnfdungeon.ErrOwnerUnavailable
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return 0, err
	}
	if archive == nil {
		return 0, dnfdungeon.ErrOwnerUnavailable
	}
	text, _, err := readInitialPVFText(archive, currentDimensionDungeonInfoPath)
	if err != nil {
		return 0, err
	}
	return parseCurrentDimensionGateExtraLimit(text, base)
}

// validateCurrentDimensionGateDailyCount 只判每日次数：等级/队伍/疲劳/门票仍由调用方既有的
// 准入链负责，避免把两条独立规则搅在一起。
func (s *Service) validateCurrentDimensionGateDailyCount(character dnfrepo.CharacterRecord) error {
	limit, _, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		return err
	}
	extraLimit, err := s.currentDimensionGateDailyExtraLimit(limit)
	if err != nil {
		return err
	}
	used, _, total, err := dnfdungeon.DimensionGateDailyAllowance(character, s.gameplayNow(), limit, extraLimit)
	if err != nil {
		return err
	}
	if used >= total {
		return dnfdungeon.ErrDailyEntryLimit
	}
	return nil
}

// consumeCurrentDimensionGateDailyEntry 在整轮运行时准备完成、活动运行闸门已持有时，把共享
// 日计数与同批入场资产（门票、入场房间疲劳）放进同一个角色事务提交；调用方
// `consumeCurrentDailyCountEntry` 负责槽位刷新与疲劳房间标记。
//
// v141 的 812 已确认读取基础已用、额外可用及每周隐藏领主计数。
// 接收门仍关闭，且每周状态尚未实现；此处继续不发包，见
// Docs/DIMENSION_COUNT_NATIVE_V141_20261019.md。
func (s *Service) consumeCurrentDimensionGateDailyEntry(session *gameSession, runtime *runtimeDungeonState, assets currentDailyCountEntryAssets) (dnfrepo.CharacterRecord, []dnfdungeon.EntryItemConsumedStack, error) {
	if runtime == nil || !s.currentDimensionGateDailyEntryEnabled(runtime.Dungeon) {
		return dnfrepo.CharacterRecord{}, nil, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.CharacterAssets == nil {
		return dnfrepo.CharacterRecord{}, nil, dnfdungeon.ErrOwnerUnavailable
	}
	limit, source, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	extraLimit, err := s.currentDimensionGateDailyExtraLimit(limit)
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ConsumeDimensionGateEntry(ctx, dnfdungeon.DimensionGateEntryCommand{
		CharacterID:   strconv.Itoa(int(session.selectedCharacterID)),
		Limit:         limit,
		ExtraLimit:    extraLimit,
		Requirements:  assets.requirements,
		UpdatedAt:     s.gameplayNow(),
		Project:       assets.project,
		InTransaction: assets.inTransaction,
	})
	if err != nil {
		return dnfrepo.CharacterRecord{}, nil, err
	}
	s.logGameEvent(session, "game-dimension-gate-daily-entry-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"used", result.Used,
		"base_limit", limit,
		"extra_limit", extraLimit,
		"limit_source", source,
		"entry_items", len(result.Consumed),
		"fatigue_commit", assets.debitRoomFatigue,
		"client_projection", "pending_native_count_ui_verification",
		"reset", "06:00_UTC+8")
	return result.Character, result.Consumed, nil
}
