package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Cycle admission counts, required entry items and room fatigue share the
// existing character asset transaction. Other dungeon policies remain separate.

type currentDailyCountEntryAssets struct {
	requirements     []dnfdungeon.EntryItemRequirement
	project          dnfdungeon.EntryItemProjector
	inTransaction    func(character *dnfrepo.CharacterRecord) error
	debitRoomFatigue bool
}

// currentDailyCountEntryEnabled 报告该副本的每日次数是否走"与入场资产同事务"的新路径。
// 解析失败必须向上冒（fail closed），不能静默当成"没有规则"。
func (s *Service) currentDailyCountEntryEnabled(dungeon worldmap.Dungeon) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return false, nil
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
	assets.inTransaction = func(character *dnfrepo.CharacterRecord) error {
		dnffatigue.Refresh(character, account, now)
		dnffatigue.Debit(character)
		return nil
	}
	return assets, nil
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
	roomFatiguePending := hasScene
	if roomFatiguePending && runtime.fatigueRooms != nil && runtime.fatigueRooms[scene.Coordinate] {
		roomFatiguePending = false
	}
	assets, err := s.currentDailyCountEntryAssets(session, runtime.Dungeon, roomFatiguePending)
	if err != nil {
		return err
	}
	var (
		character dnfrepo.CharacterRecord
		consumed  []dnfdungeon.EntryItemConsumedStack
		source    string
	)
	source = "dungeon_cycle_entry_assets"
	character, consumed, err = s.consumeCurrentDungeonCycleEntryAssets(session, runtime, assets)
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
		s.logGameEvent(session, "game-dungeon-fatigue-room-debited",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
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
