package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ── 每日次数入场的同批资产（异次元裂缝 / 艾肯）────────────────────────────────────
//
// 这两类入场的**每日次数**必须与同一轮入场的其他资产在同一个事务里提交：
//
//	1. DGN `[required item]` 里 `ConsumeOnEntry == 1` 的门票（仅在既有锦标赛开关打开时，
//	   与改动前 prepare 阶段的消耗条件逐字一致，数量也逐字一致）；
//	2. 入场房间疲劳（与 `updateCurrentCharacterFatigue(..., true)` 同口径：Refresh + Debit）。
//
// 改动前的顺序是「先扣疲劳、prepare 阶段先扣票，再提交次数」：次数写失败或被同角色另一个
// 会话抢走最后一次额度时，前面提交的疲劳/门票已经落库，入场却被拒绝 —— 这是本批要消除的
// 损失分支。现在三者共用角色资产事务（domain 侧既有的那一个单元）提交：任一失败全部回滚，
// 不使用补偿写回、不做事后返还。
//
// 范围只到这两类入场：316 魔界裂缝（次数与票本来就在一个事务里）、4906-4910 每日副本、
// 锦标赛 / 史诗入场等路径的既有事务边界与顺序都不动。

type currentDailyCountEntryAssets struct {
	requirements     []dnfdungeon.EntryItemRequirement
	project          dnfdungeon.EntryItemProjector
	inTransaction    func(character *dnfrepo.CharacterRecord) error
	debitRoomFatigue bool
	fatigueCost      int64
	fatigueStartOnly bool
}

// currentDailyCountEntryEnabled 报告该副本的每日次数是否走"与入场资产同事务"的新路径。
// 解析失败必须向上冒（fail closed），不能静默当成"没有规则"。
func (s *Service) currentDailyCountEntryEnabled(dungeon worldmap.Dungeon) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return false, nil
	}
	if s.currentDimensionGateDailyEntryEnabled(dungeon) {
		return true, nil
	}
	_, declared, err := s.currentDungeonCycleDailyCountForDungeon(dungeon.ID)
	if err != nil {
		return false, err
	}
	return declared, nil
}

// currentDailyCountEntryAssets 组装与每日次数同批提交的资产。门票只取 `ConsumeOnEntry` 条目：
// 与 `consumeDungeonEntryItemsWithTournament` 同源同条件，因此开关关闭时一行都不扣，与改动前一致。
func (s *Service) currentDailyCountEntryAssets(session *gameSession, dungeon worldmap.Dungeon, roomFatiguePending bool) (currentDailyCountEntryAssets, error) {
	assets := currentDailyCountEntryAssets{}
	if dungeonTournamentEnabled() {
		for _, requirement := range currentTournamentEntryItems(dungeon) {
			if requirement.ConsumeOnEntry {
				assets.requirements = append(assets.requirements, requirement)
			}
		}
	}
	assets.project = func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
		entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		return stack, nil
	}
	if !roomFatiguePending || currentDungeonUsesNoFatigue(dungeon) {
		return assets, nil
	}
	fatigueCost, fatigueStartOnly, err := currentDungeonStartFatigueRule(dungeon)
	if err != nil {
		return currentDailyCountEntryAssets{}, err
	}
	account := dnfrepo.AccountRecord{}
	if repos, ok := s.repositoryGroup(); ok && repos.Account != nil {
		loaded, _, err := repos.Account.Load(context.Background(), s.accountIDForSession(session))
		if err != nil {
			return currentDailyCountEntryAssets{}, err
		}
		account = loaded
	}
	now := s.gameplayNow()
	assets.debitRoomFatigue = true
	assets.fatigueCost = fatigueCost
	assets.fatigueStartOnly = fatigueStartOnly
	assets.inTransaction = func(character *dnfrepo.CharacterRecord) error {
		dnffatigue.Refresh(character, account, now)
		if fatigueStartOnly {
			return debitCurrentDungeonFatigueAmount(character, fatigueCost)
		}
		dnffatigue.Debit(character)
		return nil
	}
	return assets, nil
}

func currentDailyCountFatiguePending(runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, hasScene bool) bool {
	if runtime == nil || !hasScene || runtime.fatigueStartDebited {
		return false
	}
	return runtime.fatigueRooms == nil || !runtime.fatigueRooms[scene.Coordinate]
}

// consumeCurrentDailyCountEntry 是这两类入场的唯一提交点：次数 + 门票 + 入场房间疲劳。
// 成功后才标记疲劳房间并发出被扣票槽位刷新；失败时 domain 事务已回滚，桥侧不留任何写入。
func (s *Service) consumeCurrentDailyCountEntry(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, hasScene bool) error {
	if runtime == nil {
		return nil
	}
	enabled, err := s.currentDailyCountEntryEnabled(runtime.Dungeon)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	roomFatiguePending := currentDailyCountFatiguePending(runtime, scene, hasScene)
	assets, err := s.currentDailyCountEntryAssets(session, runtime.Dungeon, roomFatiguePending)
	if err != nil {
		return err
	}
	var (
		character dnfrepo.CharacterRecord
		consumed  []dnfdungeon.EntryItemConsumedStack
		source    string
	)
	if s.currentDimensionGateDailyEntryEnabled(runtime.Dungeon) {
		source = "dimension_gate_daily_count"
		character, consumed, err = s.consumeCurrentDimensionGateDailyEntry(session, runtime, assets)
	} else {
		source = "dungeon_cycle_entry_assets"
		character, consumed, err = s.consumeCurrentDungeonCycleEntryAssets(session, runtime, assets)
	}
	if err != nil {
		return err
	}
	runtime.Character = character
	s.refreshCurrentDungeonCycleInfo(session, runtime, "entry_commit")
	if assets.debitRoomFatigue && hasScene {
		if runtime.fatigueRooms == nil {
			runtime.fatigueRooms = make(map[worldmap.RoomCoordinate]bool)
		}
		runtime.fatigueRooms[scene.Coordinate] = true
		if assets.fatigueStartOnly {
			runtime.fatigueStartDebited = true
		}
		s.logGameEvent(session, "game-dungeon-fatigue-room-debited",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"cost", assets.fatigueCost,
			"start_only", assets.fatigueStartOnly,
			"remaining", character.Stats["fatigue"],
			"limit", character.Stats["fatigue_limit"],
			"commit", "with_daily_count")
	}
	if len(consumed) == 0 {
		return nil
	}
	refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(consumed))
	for _, item := range consumed {
		for _, slot := range item.Slots {
			refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot.Slot})
		}
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, source, refreshes); err != nil {
		s.logGameEvent(session, "game-daily-count-entry-item-refresh-deferred", "char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID, "error", err)
	}
	return nil
}
