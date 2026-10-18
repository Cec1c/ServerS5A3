package dnfbridge

import (
	"encoding/binary"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

const currentTownActorSceneSnapshotMsgID uint16 = 0x0320

// buildCurrentTownActorSceneSnapshotBody matches the current EXE's scene
// snapshot reader (sub_1D6C6F0):
//
//	u8 actor_count, actor_count * { u16 existing_actor_key, u8 child_count }
//
// The reader first resolves every actor key through sub_2699A30, then builds
// its scene-side bindings. A selected character has no server-owned children
// at town entry, so child_count is zero. The key is session data, never a
// captured DOVE key.
func buildCurrentTownActorSceneSnapshotBody(actorKey uint16) []byte {
	body := make([]byte, 4)
	body[0] = 1
	binary.LittleEndian.PutUint16(body[1:3], actorKey)
	return body
}

// sendCurrentInitialTownActorSceneSnapshotLocked completes the mode0-created
// selected actor's scene binding before full player state/location packets.
// The caller holds session.townMu.
func (s *Service) sendCurrentInitialTownActorSceneSnapshotLocked(
	session *gameSession,
	characterID uint16,
) error {
	if session == nil || characterID == 0 ||
		session.initialTownRouteCharacterID != characterID ||
		session.initialTownRouteStage < currentInitialTownRouteActorBound ||
		session.initialTownActorSceneSnapshotSent {
		return nil
	}
	actorKey := currentSceneActorObjectKey(characterID)
	body := buildCurrentTownActorSceneSnapshotBody(actorKey)
	if err := s.sendCurrentSceneFixedClass0Packet(
		session,
		currentTownActorSceneSnapshotMsgID,
		body,
		"initial_town_actor_scene_snapshot_current_exe_sub_1D6C6F0",
	); err != nil {
		return err
	}
	session.initialTownActorSceneSnapshotSent = true
	s.logGameEvent(session, "game-initial-town-actor-scene-snapshot-sent",
		"char_id", characterID,
		"actor_object_key", actorKey,
		"child_count", 0,
		"sequence", "mode0_mode1_actor_created_then_noti0x320_scene_snapshot_then_full_player_state")
	return nil
}

// sendCurrentInitialTownActorRoutePacketsLocked owns completed character-login
// ordering. Unlike a dungeon return, it finishes the full actor state before
// op24 becomes the final transition commit. The caller must hold session.townMu.
func (s *Service) sendCurrentInitialTownActorRoutePacketsLocked(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
	row currentSceneTransitionRow,
	objectBody []byte,
	mode1Body []byte,
	transitionBody []byte,
) error {
	return s.sendCurrentInitialTownActorRoutePacketsWithOptionsLocked(
		session,
		characterID,
		townID,
		areaID,
		row,
		objectBody,
		mode1Body,
		transitionBody,
		currentInitialTownRoutePolicy{
			includeReportClientSpec: true,
			includeSecondFullMode1:  true,
			skipPrerequisiteMode1:   false,
		},
	)
}

func (s *Service) sendCurrentChannelReconnectTownActorRoutePacketsLocked(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
	row currentSceneTransitionRow,
	objectBody []byte,
	mode1Body []byte,
	transitionBody []byte,
) error {
	return s.sendCurrentInitialTownActorRoutePacketsWithOptionsLocked(
		session,
		characterID,
		townID,
		areaID,
		row,
		objectBody,
		mode1Body,
		transitionBody,
		currentInitialTownRoutePolicy{
			includeReportClientSpec: true,
			includeSecondFullMode1:  true,
			skipPrerequisiteMode1:   false,
		},
	)
}

func (s *Service) sendCurrentInitialTownActorRoutePacketsWithOptionsLocked(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
	row currentSceneTransitionRow,
	objectBody []byte,
	mode1Body []byte,
	transitionBody []byte,
	policy currentInitialTownRoutePolicy,
) error {
	prerequisiteMode1Body := mode1Body
	if policy.skipPrerequisiteMode1 {
		prerequisiteMode1Body = nil
	}
	// The client pushes its own option template (op197/op198/op447) later in
	// this same entry stream, so the account replay has to land first: the
	// template the client sends after it has applied 0x00AD is the one that is
	// safe to persist last-write-wins. Guarded by `game_option_notice_sent` so
	// a reconnect route cannot replay it twice.
	if !session.gameOptionNoticeSent {
		session.gameOptionNoticeSent = true
		if err := s.sendAccountGameOptionNotices(session, "initial_town_before_actor_prerequisites"); err != nil {
			s.logWarn("dnfbridge account game option replay deferred",
				"conn_id", session.connID,
				"char_id", characterID,
				"error", err)
		}
	} else if err := s.sendSelectedCharacterHotkeyNotice(session, "initial_town_before_actor_prerequisites"); err != nil {
		s.logWarn("dnfbridge selected character hotkey replay deferred",
			"conn_id", session.connID, "char_id", characterID, "error", err)
	}
	// Character options must replay for every new selection on this connection.
	// Keep the accepted account-option gate unchanged (P55).
	if !session.characterOptionNoticeSent {
		if err := s.sendCharacterOptionNotice(session, "initial_town_before_actor_prerequisites"); err != nil {
			s.logWarn("dnfbridge character option replay deferred",
				"conn_id", session.connID,
				"char_id", characterID,
				"error", err)
		} else {
			session.characterOptionNoticeSent = true
		}
	}
	if err := s.sendCurrentTownActorPrerequisitesLocked(session, characterID, objectBody, prerequisiteMode1Body); err != nil {
		return err
	}
	if err := s.sendCurrentInitialTownActorSceneSnapshotLocked(session, characterID); err != nil {
		return fmt.Errorf("initialize selected town actor scene snapshot: %w", err)
	}
	if session.initialTownRouteStage < currentInitialTownRoutePlayerStatePrepared {
		if err := s.sendCurrentTownPlayerStateLocked(
			session,
			"initial_town_before_final_transition",
			policy,
		); err != nil {
			return fmt.Errorf("initialize selected town actor before transition: %w", err)
		}
	}
	if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, characterID, townID, areaID, row); err != nil {
		return fmt.Errorf("initialize selected town location notifications: %w", err)
	}
	if err := s.sendCurrentTownTransitionLocked(session, characterID, townID, areaID, transitionBody); err != nil {
		return err
	}
	// The endpoint handshake is connection-scoped and completes before the
	// selected-town route. Never follow op24 with another class0/op1: it would
	// arm a fresh client watchdog after the one allowed class1/op1 success.
	s.logGameEvent(session, "game-initial-town-route-endpoint-preserved",
		"char_id", characterID,
		"town_id", townID,
		"area_id", areaID,
		"route_stage", session.initialTownRouteStage,
		"resident_notice_sent", session.currentChannelResidentNoticeSent,
		"town_actor_owner_channel", session.townActorOwnerChannel,
		"endpoint_policy", "channelinfo_then_single_requested_class1_op1")
	return nil
}

// waitCurrentInitialTownSceneCommitLocked preserves the former visual-cover
// boundary as a zero-duration test seam. The caller continues directly into
// the route-specific scene finalizers.
func (s *Service) waitCurrentInitialTownSceneCommitLocked(
	session *gameSession,
	characterID uint16,
) {
	if s == nil || session == nil || s.initialTownEntryWait == nil {
		return
	}
	s.logGameEvent(session, "game-initial-town-client-loading-cover-disabled",
		"char_id", characterID,
		"duration", currentInitialTownEntryDelay.String(),
		"owner", "none",
		"server_packet_stream_blocked", false)
	s.initialTownEntryWait(currentInitialTownEntryDelay)
}

// sendCurrentTownActorRoutePacketsLocked preserves the accepted completed
// dungeon-return ordering. The caller must hold session.townMu.
func (s *Service) sendCurrentTownActorRoutePacketsLocked(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
	row currentSceneTransitionRow,
	objectBody []byte,
	mode1Body []byte,
	transitionBody []byte,
) error {
	if err := s.sendCurrentTownActorPrerequisitesLocked(session, characterID, objectBody, mode1Body); err != nil {
		return err
	}
	if session.initialTownRouteStage == currentInitialTownRouteActorBound {
		// Completed dungeon return already has a live selected actor from the
		// dungeon state chain. Its accepted town route is action-table, mode0
		// object, equipment-bearing mode1 actor state, then typed op24. Mark the town
		// player-state gate as satisfied before op24 so the later real client
		// scene callback does not replay the character-list full mode3/op19
		// initialization in visible town, which opens the personal-info panel.
		session.initialTownRouteStage = currentInitialTownRoutePlayerStatePrepared
		session.selectedUserInfoRefreshSent = true
		s.logGameEvent(session, "game-dungeon-town-route-player-state-replay-suppressed",
			"char_id", characterID,
			"town_id", townID,
			"area_id", areaID,
			"route_stage", session.initialTownRouteStage,
			"reason", "completed_dungeon_return_uses_equipment_bearing_mode1_and_must_not_replay_initial_town_full_mode3_after_op24")
	}
	// The current client rebuilds NPC task markers from op21/op574 only after
	// the selected actor exists. Completed dungeon return bypasses the full
	// cold-login initialization, so publish the repository/PVF snapshots at
	// the same safe pre-op24 boundary instead of leaving the task manual stale.
	if err := s.sendCurrentInitialTownQuestSnapshotsLocked(
		session,
		"completed_dungeon_return_after_mode1_before_typed_op24",
	); err != nil {
		return err
	}
	// Preserve the controlled town location prefix already used by cold town
	// and normal op36 before op24 consumes the area actor roster. This emits
	// existing op22/op23 and optional Seria context, never a synthetic scene callback.
	if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, characterID, townID, areaID, row); err != nil {
		return err
	}
	return s.sendCurrentTownTransitionLocked(session, characterID, townID, areaID, transitionBody)
}

func (s *Service) sendCurrentTownActorPrerequisitesLocked(
	session *gameSession,
	characterID uint16,
	objectBody []byte,
	mode1Body []byte,
) error {
	if session == nil || characterID == 0 ||
		session.initialTownRouteCharacterID != characterID ||
		session.initialTownRouteStage < currentInitialTownRouteArmed {
		return nil
	}
	if session.initialTownRouteStage < currentInitialTownRouteActionTableSent {
		if err := s.sendGameUpperRawClass(
			session,
			uint16(dnfenum.CmdPacketPVPMissionHpPercent),
			buildCurrentActionTableStateBody(),
			0,
		); err != nil {
			return err
		}
		session.initialTownRouteStage = currentInitialTownRouteActionTableSent
	}
	if session.initialTownRouteStage < currentInitialTownRouteObjectSent {
		objectPacket := csharpSelectInitPacket{
			class: 0,
			msgID: uint16(dnfenum.CmdPacketSetUDPIPPort),
			kind:  csharpCurrentSceneObjectListKind,
		}
		if err := s.sendCSharpSelectInitPacket(session, objectPacket, objectBody); err != nil {
			return err
		}
		session.initialTownRouteStage = currentInitialTownRouteObjectSent
	}
	if session.initialTownRouteStage < currentInitialTownRouteActorBound {
		if len(mode1Body) > 0 {
			if err := s.sendGameUpperRawClass(
				session,
				uint16(dnfenum.CmdPacketSetUDPIPPort),
				mode1Body,
				0,
			); err != nil {
				return err
			}
			// 记账：这一帧 mode1 已经发出，行数从**已发出的包体**里读回
			// （`login_mode1_last_before_commit.go`）。
			s.noteLoginMode1Send(session, mode1Body, "initial_town_actor_prerequisites_mode1_before_player_state")
		} else {
			s.logPacketEvent("game-initial-town-prerequisite-mode1-skipped",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"char_id", characterID,
				"object_key", currentSceneActorObjectKey(characterID),
				"reason", "cold_select_uses_single_complete_mode1_after_mode0")
		}
		session.initialTownRouteStage = currentInitialTownRouteActorBound
	}
	return nil
}

func (s *Service) sendCurrentTownTransitionLocked(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
	transitionBody []byte,
) error {
	if session == nil || characterID == 0 ||
		session.initialTownRouteCharacterID != characterID ||
		session.initialTownRouteStage < currentInitialTownRouteActorBound {
		return nil
	}
	if session.initialTownRouteStage < currentInitialTownRouteTransitionSent {
		// FIX ①（`login_mode1_last_before_commit.go`）：typed op24 之前，保证**最后一条**
		// class0/op2 mode=1 是装备承载那一帧。顺序本来就对时这里零字节；只有出现
		// "equipment-less mode1 落在最后"这一种形状时，才会把本会话已经发过的装备 mode1
		// 原样重发一帧。失败只记日志，绝不打断场景提交。
		// 注意：本函数由调用方持 `session.townMu` 调用，因此归属判据在这里现算、
		// 不可以在被调函数里再加锁（自死锁）。
		enforceOwned := session.initialTownRouteCharacterID != 0 &&
			session.initialTownRouteCharacterID == session.selectedCharacterID
		if err := s.enforceLoginMode1LastBeforeCommit(session, "initial_town_typed_op24_final_transition_commit", enforceOwned); err != nil {
			s.logWarn("dnfbridge login mode1 last-before-commit enforcement failed",
				"conn_id", session.connID,
				"char_id", characterID,
				"error", err)
		}
		if err := s.sendGameUpperRawClass(session, currentSceneTransitionMsgID, transitionBody, 0); err != nil {
			return err
		}
		setCurrentTownPositionSceneLocked(session, characterID, townID, areaID)
		session.initialTownRouteStage = currentInitialTownRouteTransitionSent
	}
	if session.initialTownRouteStage == currentInitialTownRouteTransitionSent {
		// The first selected-town op24 is followed by legacy type1345. It does
		// not own the ordinary movement/return post-op24 actor reconstruction:
		// replaying that generation at type1345 reproducibly crashes the current
		// client.
		// The skill projection was already installed at the four preserved
		// 2026-07-27 sources' full-mode1/creature pre-op24 boundary.
		session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
		session.townSceneReadyCharacterID = characterID
	}
	return nil
}
