package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	currentDungeonStoryPauseRequestSize  = 2
	currentDungeonStoryPauseResponseSize = 4
	currentDungeonStoryPauseMsgID        = uint16(170)
)

// handleCurrentDungeonStoryPause closes the current-EXE op191 -> class0/op170
// story-state exchange. The current client also uses the state-1/type-1 edge
// after the opening tutorial cinematic as its first proved room-ready
// boundary. Tutorial op3 must not be sent at the earlier op120 boundary: the
// current EXE can still have a nil active-room object manager there.
func (s *Service) handleCurrentDungeonStoryPause(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	if len(body) != currentDungeonStoryPauseRequestSize {
		s.logGameEvent(session, "game-dungeon-story-pause-blocked",
			"body_len", len(body),
			"expected_body_len", currentDungeonStoryPauseRequestSize,
			"reason", "current_exe_op191_plaintext_boundary_mismatch")
		return nil
	}
	stateFlag := body[0]
	requestType := body[1]
	if stateFlag > 1 || requestType > 2 {
		s.logGameEvent(session, "game-dungeon-story-pause-blocked",
			"body_len", len(body),
			"state_flag", stateFlag,
			"request_type", requestType,
			"reason", "current_exe_op191_writer_domain_mismatch")
		return nil
	}
	if session.selectedCharacterID == 0 {
		s.logGameEvent(session, "game-dungeon-story-pause-blocked",
			"body_len", len(body),
			"state_flag", stateFlag,
			"request_type", requestType,
			"reason", "selected_character_missing")
		return nil
	}

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		s.logGameEvent(session, "game-dungeon-story-pause-blocked",
			"char_id", session.selectedCharacterID,
			"body_len", len(body),
			"state_flag", stateFlag,
			"request_type", requestType,
			"reason", "active_dungeon_runtime_owner_mismatch")
		return nil
	}

	actorObjectKey := currentSceneActorObjectKey(session.selectedCharacterID)
	response := make([]byte, currentDungeonStoryPauseResponseSize)
	binary.LittleEndian.PutUint16(response[0:2], actorObjectKey)
	response[2] = stateFlag
	response[3] = requestType
	if err := s.sendGameUpperRawClass(
		session,
		currentDungeonStoryPauseMsgID,
		response,
		0,
	); err != nil {
		return err
	}
	s.logGameEvent(session, "game-dungeon-story-pause-op170-sent",
		"request_msg_id", uint16(dnfenum.CmdPacketDungeonEventStoryPause),
		"response_msg_id", currentDungeonStoryPauseMsgID,
		"classification", 0,
		"char_id", session.selectedCharacterID,
		"actor_object_key", actorObjectKey,
		"state_flag", stateFlag,
		"request_type", requestType,
		"body_len", len(response),
		"body_source", "current_exe_sub_1D3A650_u16_actor_u8_state_u8_type")
	if stateFlag != 1 || requestType != 1 || !isPVFTutorialDungeon(runtime) ||
		runtime.tutorialInitialUserStateSent {
		return nil
	}
	// 2026-09-24 远程部署实测：教程副本的 class0/op3 state 1 在**所有已证明的边界**上
	// 都会让当前 EXE 以空指针解引用崩溃（0xC0000005 @ E44ED6，
	// active-room object manager 为 nil）：
	//   · post-op120 放置之后                 (changelog 2026-07-25 第一次修复)
	//   · op143 progress-30 ACK 之后          (2026-07-25 第二次修复，同日被实测否定)
	//   · 客户端 finish-loading 之后          (2026-09-24 §6.9 实测)
	//   · op191 state-1/type-1 之后，紧跟 op170 同批写出 (2026-09-24 §6.10 实测)
	//   · op191 state-1/type-1 之后，推迟到下一个客户端包再发 (2026-09-24 §6.11 实测)
	// 最后一种已经排除了"客户端 dispatch 队列未排空"这个时序因素，仍然崩，
	// 说明该客户端（8A7AF614）在本教程副本里的 active-room object manager
	// 始终没有建立，op3 state 1 无法安全送达。
	// 因此按 changelog 2026-07-25 的最终结论：教程副本不合成 op3。
	// 教程的解锁改走既有的"教程中放弃/返回城镇"边界
	// （persistCurrentActiveTutorialExit，op42 / op132 共用），它会持久化
	// tutorial_completed=1 并把角色送回出生城镇。
	// 普通（非教程）owned dungeon 的 op3 流程完全不受影响。
	finishGates := session.finishLoadingGateSnapshot()
	if !session.postStartMapPlayerStateSent || !finishGates.State {
		s.logGameEvent(session, "game-dungeon-tutorial-user-state-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"state_flag", stateFlag,
			"request_type", requestType,
			"post_start_map_state_sent", session.postStartMapPlayerStateSent,
			"finish_loading_state_sent", finishGates.State,
			"reason", "tutorial_story_resume_arrived_before_scene_ready")
		return nil
	}
	if !s.options.tutorialDeferUserState {
		// 应急回退（DNFBRIDGE_TUTORIAL_DEFER_USER_STATE=0）：这一边界过去也是完全抑制，
		// 依据是"op3 state 1 在所有边界都崩"的旧结论；该结论已被 2026-09-24 的实测
		// 推翻（教程可以正常通关），所以默认改为与上游一致的发送。
		runtime.tutorialInitialUserStateSent = true
		s.logGameEvent(session, "game-dungeon-tutorial-user-state-fully-suppressed",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"room", runtime.Session.Snapshot().Run.Current,
			"actor_object_key", actorObjectKey,
			"request_msg_id", uint16(dnfenum.CmdPacketDungeonEventStoryPause),
			"request_state_flag", stateFlag,
			"request_type", requestType,
			"reason", "suppression_forced_by_environment_DNFBRIDGE_TUTORIAL_DEFER_USER_STATE_0",
			"fallback", "leave_tutorial_via_op42_or_op132_persists_completion_and_birth_town")
		return nil
	}
	// op191 state-1/type-1 是教程里**剧情恢复**的已证房间就绪边界：上游 0.1.8 血统
	// 在这里直接发 op3 user state（不是抑制），与 scene_post_start_map 的
	// finish-loading 延迟一起构成完整的教程 user-state 路径。2026-09-24 实测确认
	// 延迟发送那一半可行，因此这一半也按上游对齐。
	userStateBody, err := s.currentDungeonUserStateBody(
		session,
		runtime,
		actorObjectKey,
		"tutorial_story_pause_state1_type1",
	)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketNotifyUserState),
		userStateBody,
		0,
	); err != nil {
		return err
	}
	runtime.tutorialInitialUserStateSent = true
	s.logGameEvent(session, "game-dungeon-tutorial-user-state-op3-sent",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"room", runtime.Session.Snapshot().Run.Current,
		"actor_object_key", actorObjectKey,
		"request_msg_id", uint16(dnfenum.CmdPacketDungeonEventStoryPause),
		"request_state_flag", stateFlag,
		"request_type", requestType,
		"response_msg_id", uint16(dnfenum.CmdPacketNotifyUserState),
		"classification", 0,
		"body_len", len(userStateBody),
		"user_state", currentDungeonPlayerUserState,
		"body_source", "reference_aligned_op191_state1_type1_story_resume_room_ready")
	return nil
}
