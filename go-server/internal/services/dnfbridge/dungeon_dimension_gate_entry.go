package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
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

// currentDimensionGateDailyEntryLimit 返回共享池的准入总额度（默认档 + 额外档）。
// 读不到正额度时拒绝入场，而不是静默放行。
func (s *Service) currentDimensionGateDailyEntryLimit() (int64, string, error) {
	limit := s.currentDimensionGateEntryLimitForSession()
	total := int64(limit.enterCount) + int64(limit.extraCount)
	if total <= 0 {
		return 0, limit.source, fmt.Errorf("dimension gate daily enter limit is not positive: %d/%d source=%s", limit.enterCount, limit.extraCount, limit.source)
	}
	return total, limit.source, nil
}

// validateCurrentDimensionGateDailyCount 只判每日次数：等级/队伍/疲劳/门票仍由调用方既有的
// 准入链负责，避免把两条独立规则搅在一起。
func (s *Service) validateCurrentDimensionGateDailyCount(character dnfrepo.CharacterRecord) error {
	limit, _, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		return err
	}
	used, err := dnfdungeon.DimensionGateDailyUsed(character, s.gameplayNow())
	if err != nil {
		return err
	}
	if used >= limit {
		return dnfdungeon.ErrDailyEntryLimit
	}
	return nil
}

// consumeCurrentDimensionGateDailyEntry 在整轮运行时准备完成、活动运行闸门已持有时提交共享
// 日计数（与 `consumeCurrentRiftEntry` / `consumeCurrentDayDungeonEntry` 同一边界）。重传的
// SELECT_DUNGEON 落在同一个活动运行上，不会再扣一次。
//
// 本函数只提交计数：客户端计数投影（官方 24.9 的 812 handler）尚未验证，见
// `dimension_gate_entrance.go`，因此这里不发包、不改 opcode。
func (s *Service) consumeCurrentDimensionGateDailyEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if runtime == nil || !s.currentDimensionGateDailyEntryEnabled(runtime.Dungeon) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.CharacterAssets == nil {
		return dnfdungeon.ErrOwnerUnavailable
	}
	limit, source, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		return err
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ConsumeDimensionGateEntry(ctx, dnfdungeon.DimensionGateEntryCommand{
		CharacterID: strconv.Itoa(int(session.selectedCharacterID)),
		Limit:       limit,
		UpdatedAt:   s.gameplayNow(),
	})
	if err != nil {
		return err
	}
	runtime.Character = result.Character
	s.logGameEvent(session, "game-dimension-gate-daily-entry-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"used", result.Used,
		"limit", limit,
		"limit_source", source,
		"client_projection", "pending_native_812_verification",
		"reset", "06:00_UTC+8")
	return nil
}
