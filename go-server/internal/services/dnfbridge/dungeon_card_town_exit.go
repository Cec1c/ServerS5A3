package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 调用者持有dungeon.mu；待办只属于当前角色、当前已完成普通副本。
// 团本、教学、塔和锦标赛继续由各自的结果与退出流程处理。
func currentOrdinaryCompletedTownExitOwner(session *gameSession, runtime *runtimeDungeonState) bool {
	return session != nil && runtime != nil && runtime.Session != nil &&
		session.dungeon.runtime == runtime && dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) &&
		runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunCompleted &&
		runtime.raidEntry == nil && runtime.Tournament == nil &&
		!isPVFTutorialDungeon(runtime) && !currentDungeonUsesDedicatedTowerResult(runtime)
}

// resumeCurrentDungeonCardTownReturn 在真实牌面就绪后续办明确的回城意图。
// 调用者必须已释放dungeon.mu；使用原退出处理器自动翻免费牌、幂等落账和重建城镇。
// 不主动制造69/70，不购买付费牌，也不把上局或已切换角色的待办带到新局。
func (s *Service) resumeCurrentDungeonCardTownReturn(session *gameSession, runtime *runtimeDungeonState) error {
	if session == nil || runtime == nil {
		return nil
	}
	session.dungeon.mu.Lock()
	requestMsgID := runtime.settlementTownReturnRequestMsgID
	ready := currentOrdinaryCompletedTownExitOwner(session, runtime) &&
		(requestMsgID == uint16(dnfenum.CmdPacketGiveupGame) || requestMsgID == uint16(dnfenum.CmdPacketBack2Village)) &&
		completedDungeonSettlementResultReady(runtime, session.selectedCharacterID) &&
		runtime.settlementCardLayoutSent && runtime.settlementPhase >= currentDungeonSettlementPhaseCardsRevealed
	session.dungeon.mu.Unlock()
	if !ready {
		return nil
	}
	s.logGameEvent(session, "翻牌就绪后续办回城请求",
		"char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID, "request_msg_id", requestMsgID)
	return s.handleCurrentDungeonTownExitRequest(session, nil, requestMsgID)
}
