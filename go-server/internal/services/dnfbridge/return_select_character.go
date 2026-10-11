package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// townCopresenceEvictOnCharacterSelectEnv 是「切角色（op7）时把**上一个角色**从城镇在线表
// 摘掉、并向同区剩下的玩家补发 NOTI 0x0006」的开关。
//
// 默认（未设置 / 空 / 未知值）= on = 修复档；`off` / `0` / `false` / `no` / `legacy` = 逐字节
// 回到改动前（切角色只清 selectedCharacterID，不摘存在感、不发任何移除帧）。回退值的口径与
// 同包的 currentTownPresencePublishDisabled 完全一致（默认开）。
//
// 现场（2026-09-28 批次 4 第 4 项）：切角色之后还能看到自己**上一个角色**站在城镇里约 1 秒。
// 根因：城镇存在感按**角色 id** 分桶（online_player_manager.go 的 byArea/byChar），而
// resetGameSessionForCharacterSelect 只把 session.selectedCharacterID 清成 0，从不摘表也从不
// 广播 despawn ⇒ 上一个角色仍是"活着的同区玩家"，新角色 EnterArea 把他当同屏队友投影回**同一条
// 连接**（op2 mode0/mode1 + op9 + op23），op24 名册也多一行（len 36 / rows 2，正常 len 28 / rows 1）。
// 客户端用自己的 verified=0 守卫在 1,020–1,063 ms 后丢弃，所以运营看到的是"过 1s 刷新掉"。
// 厂商 A21 在 op7 / op4 切换时都会 await _townHandler.NotifyLeaveAsync(session) 广播 0x0006
// USER_LEAVE（CharacterSessionLifecycleCoordinator.cs:526 / :676 ⇒ TownHandler.cs:465-468）：
// 这是我们相对参考实现漏掉的一步，不是新发明。
// 端到端取证（服务器日志 + 客户端 trace + 厂商对照）见 `_dist\stale-town-peer-diagnosis.md`。
const townCopresenceEvictOnCharacterSelectEnv = "DNFBRIDGE_TOWN_COPRESENCE_EVICT_ON_CHARACTER_SELECT"

// currentTownCopresenceEvictOnCharacterSelectEnabled 每次调用都读环境变量（与同包其它城镇开关
// 一样：启动后改 env 不影响已启动进程，但测试可以用 t.Setenv 直接回退）。
func currentTownCopresenceEvictOnCharacterSelectEnabled() bool {
	return !currentTownPresencePublishDisabled(townCopresenceEvictOnCharacterSelectEnv)
}

// handleUpperReturnSelectCharacter closes the selected-character lifecycle and
// acknowledges op7. The current client switches to the character-select module
// from the ACK handler, then issues op8 to request a fresh repository roster.
func (s *Service) handleUpperReturnSelectCharacter(session *gameSession, body []byte) error {
	if s == nil || session == nil {
		return nil
	}
	if len(body) != 0 {
		s.logGameEvent(session, "game-upper-return-select-character-blocked",
			"body_len", len(body),
			"expected_body_len", 0,
			"reason", "current_exe_op7_body_must_be_empty")
		return nil
	}

	previousCharacterID, abandonedDungeon := s.resetGameSessionForCharacterSelect(session)
	s.logGameEvent(session, "game-upper-return-select-character-send",
		"previous_char_id", previousCharacterID,
		"dungeon_runtime_abandoned", abandonedDungeon,
		"sequence", "class1_op7_success_then_wait_for_client_op8")
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketReturnSelectCharacter), nil)
}

func (s *Service) resetGameSessionForCharacterSelect(session *gameSession) (uint16, bool) {
	if s == nil || session == nil {
		return 0, false
	}
	s.closeCurrentExpertJobStoreSession(session, true)
	s.retireCurrentLukeSessionRun(session)
	s.detachRuntimePartySession(session, "return_select_character")
	s.stopCurrentSpendTimeClock(session, "return_select_character")
	s.stopCurrentPetGrowthClock(session, "return_select_character")
	s.stopCurrentDailyDeleteClock(session)

	abandonedDungeon := false
	session.dungeon.mu.Lock()
	if runtime := session.dungeon.runtime; runtime != nil {
		if runtime.Session != nil && runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
			if err := runtime.Session.Abandon(); err != nil {
				s.logGameEvent(session, "game-upper-return-select-character-dungeon-abandon-failed", "error", err)
			} else {
				abandonedDungeon = true
			}
		}
		s.cancelCurrentDungeonCardAutoFlipLocked(session, runtime, "return_select_character")
		session.dungeon.runtime = nil
		// 换角色前必须把成员名额从队伍实例里摘掉（selectedCharacterID 紧接着会被清零）。
		s.releasePartyDungeonInstance(runtime, session.selectedCharacterID, "return_select_character")
	}

	s.lockCurrentCharacterBindingMutation(session, 0)
	previousCharacterID := session.selectedCharacterID
	if previousCharacterID != 0 && s.gameSessions[previousCharacterID] == session {
		delete(s.gameSessions, previousCharacterID)
	}
	if previousCharacterID != 0 {
		advanceGameSessionCharacterGeneration(session)
	}
	// 切角色等价于"上一个角色离开城镇"：必须在 selectedCharacterID 清零的**同一个临界区**里
	// 把他从城镇在线表摘掉，否则下一个角色 EnterArea 会用这张还没摘干净的表，把上一个角色当作
	// 同区队友投影回本客户端（op24 名册同理）——运营看到的残影就是这么来的。
	// 用 LeaveAreaSession（而不是 LeaveArea）：保留"同角色短命辅助连接不得驱逐常住城镇连接"的
	// 既有保护（online_player_manager.go:178-200），记录里的 Session 不是本会话时自然摘不到。
	// 注意：广播与记日志都会取 s.mu（logPacketEvent），所以只在这里摘表，发包放到下面 unlock 之后。
	var evictedPeers []onlinePlayerInfo
	presenceEvicted := false
	if currentTownCopresenceEvictOnCharacterSelectEnabled() &&
		previousCharacterID != 0 && s.onlinePlayers != nil {
		_, evictedPeers, presenceEvicted = s.onlinePlayers.LeaveAreaSession(previousCharacterID, session)
	}
	session.selectedCharacterID = 0
	s.mu.Unlock()
	session.dungeon.mu.Unlock()

	if presenceEvicted {
		if len(evictedPeers) > 0 {
			s.broadcastTownPlayerLeave(previousCharacterID, evictedPeers)
		}
		s.logGameEvent(session, "game-upper-return-select-character-presence-evicted",
			"previous_char_id", previousCharacterID,
			"remaining_peers", len(evictedPeers))
	}

	resetDungeonEntrySceneGates(session)
	markReturnSelectTownReentry(session, previousCharacterID)
	clearCurrentDungeonSelectContext(session)
	clearCurrentTownSelectorOrigin(session)
	session.selectPreviewObjectStateSent = false
	session.selectPreviewActorOwnerChannel = 0
	session.rosterRequested = false
	session.channelReconnect = false
	session.townActorOwnerChannel = currentConnectionTownActorOwnerContext(session)
	return previousCharacterID, abandonedDungeon
}
