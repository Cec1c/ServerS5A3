package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const (
	currentSceneOverseerPageCount      = 5
	currentDungeonPlayerUserState byte = 1
)

// buildCurrentDungeonUserStateBody（单条 4 字节体）、成员取源链与 all 档组包都在
// dungeon_party_user_state_members.go。

func (s *Service) sendCurrentSceneOverseerPages(ctx context.Context, session *gameSession) error {
	for page := 0; page < currentSceneOverseerPageCount; page++ {
		fallback := buildCurrentRequestOverseerBody(uint32(page))
		body := s.buildCurrentRequestOverseerBodyForSession(ctx, session, fallback)
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRequestOverseer), body, 0); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) sendCurrentSelectPreviewObjectState(
	ctx context.Context,
	session *gameSession,
	charID uint16,
	charName string,
	character dnfrepo.CharacterRecord,
	hasCharacter bool,
	source string,
) error {
	if session == nil || session.selectPreviewObjectStateSent {
		return nil
	}
	if charID == 0 {
		return fmt.Errorf("select preview object state requires selected character")
	}
	objectKey := currentSceneActorObjectKey(charID)
	// This preview precedes the dungeon transition. The native mode0 reader
	// (22C4F86/6E839F0) compares its owner with the selected client channel;
	// owner zero on channel 10 creates an auxiliary actor absent from 296D730.
	// Op124 then dereferences the missing local actor in 20A0D0C. Keep mode0,
	// binding mode1 and the later preview removal in this same owner context.
	ownerChannel := currentClientSelectedChannel(session)
	s.logPacketEvent("game-upper-select-preview-object-state-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"sequence", "op358_pages_action_table_msg2_mode0_actor_binding_mode1_op359_op356_op124",
		"lifetime", "removed_by_op9_kind3_after_validated_op16_before_final_actor_rebind",
		"mode1", "actor_binding_only_before_op359_full_equipment_after_op29")

	if err := s.sendCurrentSceneOverseerPages(ctx, session); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketPVPMissionHpPercent), buildCurrentActionTableStateBody(), 0); err != nil {
		return err
	}
	objectBody := s.buildCurrentSceneObjectListBodyForSessionInContext(ctx, session, charID, charName, character, hasCharacter, ownerChannel)
	objectPacket := csharpSelectInitPacket{
		class: 0,
		msgID: uint16(dnfenum.CmdPacketSetUDPIPPort),
		kind:  csharpCurrentSceneObjectListKind,
	}
	if err := s.sendCSharpSelectInitPacket(session, objectPacket, objectBody); err != nil {
		return err
	}
	// Current NoPack's op359 path enters sub_1D8C0C0 and dereferences the
	// native selected-character descriptor. Mode0 creates the preview actor but
	// does not install that descriptor; binding-only mode1 performs the native
	// sub_2693E90 assignment without publishing equipment prematurely.
	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	mode1Body := s.buildCurrentActorBindingMode1BodyForSelectedInContext(
		ctx,
		session,
		character,
		hasCharacter,
		charID,
		uint32(adventureSummary.ManageLevel),
		ownerChannel,
	)
	s.logPacketEvent("game-upper-select-preview-actor-binding-mode1-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"owner_channel", ownerChannel,
		"body_len", len(mode1Body),
		"equipment_create_count", 0,
		"body_source", "current_exe_sub_2008010_sub_2002fc0_sub_2693e90_before_sub_1d8c0c0")
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), mode1Body, 0); err != nil {
		return err
	}
	insertBody := s.buildCurrentInsertOverseerBodyForSession(ctx, session, buildCurrentInsertOverseerBody())
	insertPacket := csharpSelectInitPacket{
		class: 0,
		msgID: uint16(dnfenum.CmdPacketInsertOverseer),
		kind:  csharpLongHengSceneBootstrapKind,
	}
	if err := s.sendCSharpSelectInitPacket(session, insertPacket, insertBody); err != nil {
		return err
	}
	clearQuestListBody, err := s.buildCurrentClearQuestListTransportBodyForSession(
		ctx,
		session,
		source+"_select_preview_before_scene_commit",
	)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperFixed16Transport(
		session,
		currentClearQuestListMsgID,
		clearQuestListBody,
		0,
		1,
		true,
		currentClearQuestListTransportCodec,
	); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketReportClientSpec), nil, 0); err != nil {
		return err
	}

	session.selectPreviewObjectStateSent = true
	session.selectPreviewActorOwnerChannel = ownerChannel
	s.logPacketEvent("game-upper-select-preview-object-state-finished",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"packet_count", currentSceneOverseerPageCount+6,
		"personal_panel", "not_sent",
		"removal", "await_validated_op16")
	return nil
}

func (s *Service) sendCurrentPreDungeonContextPlayerState(session *gameSession, source string) error {
	if session == nil {
		return nil
	}
	if session.preDungeonContextPlayerStateSent {
		s.logGameEvent(session, "game-upper-pre-dungeon-context-player-state-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "already_sent")
		return nil
	}
	if session.selectedCharacterID == 0 {
		return fmt.Errorf("pre-dungeon-context player state requires selected character")
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	charID, charName, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if charID == 0 {
		charID = session.selectedCharacterID
	}
	objectKey := currentSceneActorObjectKey(charID)
	ownerChannel := byte(currentSceneObjectContext)
	s.logGameEvent(session, "game-upper-pre-dungeon-context-player-state-send",
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"owner_channel", ownerChannel,
		"sequence", "msg2_mode0_then_actor_binding_only_mode1_before_op27",
		"mode3", "not_sent",
		"evidence", "pickup_proven_current_client_dungeon_context_uses_owner_zero_for_mode0_and_mode1")

	objectBody := s.buildCurrentSceneObjectListBodyForSessionInContext(
		ctx,
		session,
		charID,
		charName,
		character,
		hasCharacter,
		ownerChannel,
	)
	objectPacket := csharpSelectInitPacket{
		class: 0,
		msgID: uint16(dnfenum.CmdPacketSetUDPIPPort),
		kind:  csharpCurrentSceneObjectListKind,
	}
	if err := s.sendCSharpSelectInitPacket(session, objectPacket, objectBody); err != nil {
		return err
	}

	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	adventureLevel := uint32(adventureSummary.ManageLevel)
	mode1Body := s.buildCurrentActorBindingMode1BodyForSelectedInContext(
		ctx,
		session,
		character,
		hasCharacter,
		charID,
		adventureLevel,
		ownerChannel,
	)
	s.logGameEvent(session, "game-upper-pre-dungeon-context-actor-binding-mode1-send",
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"owner_channel", ownerChannel,
		"adventure_total_point", adventureSummary.TotalPoint,
		"adventure_manage_level", adventureSummary.ManageLevel,
		"body_len", len(mode1Body),
		"equipment_create_count", 0,
		"equipment_update_count", 0,
		"stat_blob_len", currentMode1StatBlobWireSize,
		"body_source", "current_exe_sub_2008010_real_92b_state_sub_2002fc0_actor_create_and_sub_2693e90_descriptor_bind")
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), mode1Body, 0); err != nil {
		return err
	}

	session.preDungeonContextPlayerStateSent = true
	s.logGameEvent(session, "game-upper-pre-dungeon-context-player-state-finished",
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"packet_count", 2,
		"next_stage", "op27_page_enter_actor_vtable_0x254_existing_runtime_recompute_gate")
	return nil
}

func (s *Service) sendCurrentPostStartMapPlayerPlacement(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	source string,
) error {
	if session == nil {
		return nil
	}
	if session.postStartMapPlayerStateSent {
		s.logPacketEvent("game-upper-post-start-map-player-placement-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "already_sent")
		return nil
	}
	if session.selectedCharacterID == 0 {
		return fmt.Errorf("post-start-map player state requires selected character")
	}

	// This scene bootstrap loads several authoritative repository snapshots;
	// it is not a single mutation. In the live tower entry, writing op13 alone
	// outlasted the mutation deadline and expired the later mode1/quest reads.
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	charID, charName, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if charID == 0 {
		charID = session.selectedCharacterID
	}
	// The select ACK stores the selected character id as the protected local
	// owner key. Reusing the fixed preview key here creates a second, remote
	// wrapper, so mode1 would update the other-player actor instead of the
	// selected scene actor.
	objectKey := currentSceneActorObjectKey(charID)
	ownerChannel := byte(currentSceneObjectContext)
	// PVF tutorial scenes can still reach sub_1D88A10 before their room object
	// manager is usable. Ordinary dungeon scenes activate their already-bound mode1
	// actor only after op120 has committed the room placement. Successful
	// pickup traces use that order; sending op3 before op120 leaves the client
	// showing drops without enabling its native op43 pickup writer.
	//
	// 2026-07-25 的实测重试已经否定了"把教程 op3 延后到 finish-loading /
	// op143 progress-30 再发"这一方案：
	//   "The live retry disproved op143 progress 30 as a current-client
	//    scene-ready boundary. The client transmitted that request before its
	//    dispatch queue processed op120, so the server's deferred class0/op3
	//    state-1 packet was still queued directly behind op120 and reproduced
	//    the same 0xC0000005 at E44ED6 through a nil active-room object manager."
	// 结论是 PVF 教程入口一律不合成 class0/op3 state 1（既不放在 op120 之后，
	// 也不放在 op143 之后）。2026-09-24 远程部署复测再次命中同一处崩溃：
	// map/Cataclysm/NewTutorial/Priest_F/57801.map 在 finish-loading 边界收到
	// 延迟 op3 后同样 0xC0000005 @ E44ED6。因此这里不再设置 deferred 键，
	// scene_finish_loading.go 的延迟发送分支自然跳过。
	// 普通 owned dungeon 仍保留既有的 post-op120 state transition。
	ownedDungeonScene := currentDungeonRoomOwnsScene(runtime, scene)
	suppressInitialUserState := ownedDungeonScene && isPVFTutorialDungeonScene(runtime, scene)
	sendInitialUserState := ownedDungeonScene && !suppressInitialUserState
	session.deferredDungeonUserStateObjectKey = 0
	if suppressInitialUserState {
		if s.options.tutorialDeferUserState {
			// 默认行为（2026-09-24 实测确认）：教程 state 1 交给客户端
			// finish-loading 边界再发。此前的"完全抑制"会让教程卡在移动教学
			// 过不了图（拯救赛丽亚也就无从发生）。
			session.deferredDungeonUserStateObjectKey = objectKey
			s.logPacketEvent("game-upper-post-start-map-user-state-deferred",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"source", source,
				"char_id", charID,
				"object_key", objectKey,
				"deferred_until", "client_finish_loading_request",
				"reason", "reference_deferral_confirmed_by_live_tutorial_clear_20260924")
		} else {
			// 应急回退（DNFBRIDGE_TUTORIAL_DEFER_USER_STATE=0）：完全抑制。
			// 可避免 post-op120 等较早边界上的 0xC0000005 @ E44ED6，代价是教程
			// 无法推进，只能靠"返回城镇"边界标记完成。
			s.logPacketEvent("game-upper-post-start-map-user-state-fully-suppressed",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"source", source,
				"char_id", charID,
				"object_key", objectKey,
				"reason", "pvf_tutorial_state1_earlier_boundaries_crash_at_E44ED6_suppression_forced_by_environment")
		}
	}
	mode0Stage := "sent_once_before_op27"
	if !session.preDungeonContextPlayerStateSent {
		mode0Stage = "fallback_after_op29"
	}
	s.logPacketEvent("game-upper-post-start-map-player-state-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"owner_channel", ownerChannel,
		"sequence", "op358_pages_action_table_op13_containers_full_mode1_op359_op356_op124_op9_op120_then_ordinary_op3_or_tutorial_suppress",
		"initial_user_state_eligible", ownedDungeonScene,
		"initial_user_state_suppressed", suppressInitialUserState,
		"mode0", mode0Stage,
		"reason", "op29_room_commit_precedes_full_equipment_and_stat_refresh")

	if err := s.sendCurrentSceneOverseerPages(ctx, session); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketPVPMissionHpPercent), buildCurrentActionTableStateBody(), 0); err != nil {
		return err
	}
	if !session.preDungeonContextPlayerStateSent {
		// Direct callers that did not run the dungeon-context actor-binding stage still
		// need a descriptor before the full mode1 state is applied.
		objectBody := s.buildCurrentSceneObjectListBodyForSessionInContext(
			ctx,
			session,
			charID,
			charName,
			character,
			hasCharacter,
			ownerChannel,
		)
		s.logPacketEvent("game-upper-post-start-map-mode0-built",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"body_len", len(objectBody),
			"reason", "fallback_when_pre_op27_descriptor_stage_was_not_sent")
		objectPacket := csharpSelectInitPacket{
			class: 0,
			msgID: uint16(dnfenum.CmdPacketSetUDPIPPort),
			kind:  csharpCurrentSceneObjectListKind,
		}
		if err := s.sendCSharpSelectInitPacket(session, objectPacket, objectBody); err != nil {
			return err
		}
	} else {
		s.logGameEvent(session, "game-upper-post-start-map-mode0-skipped",
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"reason", "descriptor_and_actor_bound_before_op27_page_enter")
	}
	// Install the complete container snapshot only after the selected actor and
	// its container managers exist. This changes inventory ownership only; the
	// dungeon scene/camera packet sequence remains untouched.
	if err := s.sendCurrentSelectInventoryBootstrap(session, source+"_after_actor_before_player_object"); err != nil {
		return err
	}
	// The container bootstrap owns its own bounded snapshot context and socket
	// writes. Its elapsed time must not consume the following actor/quest reads.
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	adventureLevel := uint32(adventureSummary.ManageLevel)
	mode1Body := s.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		ctx,
		session,
		character,
		hasCharacter,
		charID,
		adventureLevel,
		ownerChannel,
		true,
	)
	s.logPacketEvent("game-upper-post-start-map-local-mode1-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"owner_channel", ownerChannel,
		"adventure_total_point", adventureSummary.TotalPoint,
		"adventure_manage_level", adventureSummary.ManageLevel,
		"adventure_manage_option", adventureSummary.ManageOption,
		"body_len", len(mode1Body),
		"stat_blob_len", currentMode1StatBlobWireSize,
		"body_source", "current_exe_sub_2008010_local_owner_real_state_and_equipment_create")
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), mode1Body, 0); err != nil {
		return err
	}
	// Full mode1 recreates the selected actor's equipped creature. Its dynamic
	// enchant card is applied only by the existing same-template slot26 op14
	// path, so restore that durable state after creation in this lifecycle too.
	if repositories, ok := s.repositoryGroup(); ok && repositories.Equipment != nil {
		if err := s.sendSelectedEquippedCreatureItemRefresh(session, source+"_after_dungeon_mode1"); err != nil {
			return err
		}
	}
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, source+"_after_dungeon_mode1"); err != nil {
		return err
	}
	session.selectedUserInfoRefreshSent = true

	insertBody := s.buildCurrentInsertOverseerBodyForSession(ctx, session, buildCurrentInsertOverseerBody())
	insertPacket := csharpSelectInitPacket{
		class: 0,
		msgID: uint16(dnfenum.CmdPacketInsertOverseer),
		kind:  csharpLongHengSceneBootstrapKind,
	}
	if err := s.sendCSharpSelectInitPacket(session, insertPacket, insertBody); err != nil {
		return err
	}
	clearQuestListBody, err := s.buildCurrentClearQuestListTransportBodyForSession(
		ctx,
		session,
		source+"_post_start_map_before_scene_commit",
	)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperFixed16Transport(
		session,
		currentClearQuestListMsgID,
		clearQuestListBody,
		0,
		1,
		true,
		currentClearQuestListTransportCodec,
	); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketReportClientSpec), nil, 0); err != nil {
		return err
	}

	partyState := runtimePartyStateSnapshot(session)
	_, partyMemberIndexed := runtimePartyMemberIndex(charID, partyState)
	if partyMemberIndexed {
		if err := s.sendRuntimePartyRealtimeInfoLocal(session, partyState); err != nil {
			return err
		}
		// 本开关按**相位**收口"进图那一次提交里同一个 actor 被声明两条 kind0 槽位表"：
		// 本次提交已经发过厂商形状的房间槽位表时，这条 `…_post_start_map` 名册帧让位。
		// 默认档（current）恒不让位 ⇒ 逐字节与今天相同。详见
		// dungeon_party_room_slot_table.go 的 dungeonPartyEntrySlotTableEnv。
		// 实时信息（msg 153）无论哪一档都照发：它不是触发点（抑制 11+153 不减退队），
		// 而且队伍框的血条/槽位列要它。
		if !s.suppressPostStartMapLegacyPartyFrame(session, source+"_post_start_map") {
			frameSent, err := s.sendCurrentPartyFrameProjection(session, partyState, source+"_post_start_map")
			if err != nil {
				return err
			}
			if !frameSent {
				return fmt.Errorf("post-start-map party frame requires bound character %d", charID)
			}
		}
	} else {
		op9Body := s.buildCurrentSceneOp9ActorDisplayBodyInContext(
			objectKey,
			character,
			hasCharacter,
			charName,
			ownerChannel,
		)
		s.logPacketEvent("game-upper-post-start-map-op9-actor-display-send",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"owner_channel", ownerChannel,
			"body_len", len(op9Body),
			"body_source", "current_exe_sub_1D64CA0_after_mode2_room_player_object_and_stats")
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRecoverStamina), op9Body, 0); err != nil {
			return err
		}
	}

	placementBody := buildCurrentSceneActorPlacementBody()
	s.logPacketEvent("game-upper-post-start-map-player-placement-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"msg_id", uint16(dnfenum.CmdPacketRequestBlacklist),
		"classification", 0,
		"body_len", len(placementBody),
		"actor_slot", placementBody[0],
		"placement_seed", placementBody[1],
		"body_source", "current_exe_sub_1D41DA0_actor_slot_scene_commit")
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRequestBlacklist), placementBody, 0); err != nil {
		return err
	}

	// The working current-client route commits the room object manager with
	// op120 before op3 marks the selected local wrapper active.
	if sendInitialUserState {
		userStateBody, err := s.currentDungeonUserStateBody(session, runtime, objectKey, source)
		if err != nil {
			return err
		}
		s.logPacketEvent("game-upper-post-start-map-user-state-send",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"msg_id", uint16(dnfenum.CmdPacketNotifyUserState),
			"classification", 0,
			"body_len", len(userStateBody),
			"user_state", currentDungeonPlayerUserState,
			"body_source", "working_current_client_trace_op3_after_op120_room_object_manager_commit")
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyUserState), userStateBody, 0); err != nil {
			return err
		}
	} else if suppressInitialUserState {
		s.logPacketEvent("game-upper-post-start-map-user-state-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"map_path", scene.Map.Map.Path,
			"suppressed", "entirely",
			"reason", "pvf_tutorial_state1_crashes_at_E44ED6_at_both_post_op120_and_finish_loading_boundaries")
	} else if !sendInitialUserState {
		s.logPacketEvent("game-upper-post-start-map-user-state-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"map_path", scene.Map.Map.Path,
			"reason", "scene_is_not_owned_by_current_dungeon_runtime")
	}

	session.postStartMapPlayerStateSent = true
	session.sceneBootstrapTailDeferred = false
	session.sceneBootstrapTailSent = true
	minimumPacketCount := currentSceneOverseerPageCount + 13
	if !session.preDungeonContextPlayerStateSent {
		minimumPacketCount++
	}
	userStateResult := "skipped_unowned_dungeon_scene"
	if sendInitialUserState {
		minimumPacketCount++
		userStateResult = "normal_state_1_sent_after_op120_room_object_manager_commit_for_owned_dungeon"
	} else if suppressInitialUserState {
		userStateResult = "deferred_to_client_finish_loading_for_pvf_tutorial"
	}
	s.logPacketEvent("game-upper-post-start-map-player-placement-finished",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"minimum_packet_count", minimumPacketCount,
		"item_lists", "sent_before_full_equipment_mode1_after_pre_op27_actor_binding",
		"user_state", userStateResult,
		"mode0", mode0Stage,
		"mode1", "full_equipment_refresh_after_op29_for_existing_local_actor",
		"mode3", "not_sent_personal_information_panel_route",
		"equipment", "mode1_create_then_post_finish_op14_update",
		"skills", "post_finish_op19_after_local_actor_state",
		"op9", "sent_after_scene_object_finalizer_before_scene_slot_placement",
		"op3", userStateResult,
		"hud", "deferred")

	// 本人已落位完成：记录本场副本运行键，并跟已经落位完成的队友互画 actor。
	// 进图落位有先后，两个方向都在"后落位"的那次补齐（先做的一方因为对面还没有运行键
	// 而什么都不做，也不会被标记），失败只记日志、不能让进图失败。
	s.recordDungeonPartyRunKey(session, runtime, scene)
	s.recordCurrentRaidParticipation(session, runtime)
	if err := s.projectDungeonPartyPeerActors(session, source+"_party_peer_copresence"); err != nil {
		s.logGameEvent(session, "game-dungeon-party-peer-actor-failed",
			"conn_id", session.connID,
			"source", source+"_party_peer_copresence",
			"char_id", session.selectedCharacterID,
			"party_id", runtimePartyStateSnapshot(session).PartyID,
			"error", err)
	}
	return nil
}
