package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// sendCurrentInitialTownPlayerState completes the selected town actor before
// the final op24 transition commit. No DOVE packet body and no synthetic C2S
// op37 is used here.
func (s *Service) sendCurrentInitialTownPlayerState(session *gameSession, source string) error {
	if session == nil {
		return nil
	}
	session.townMu.Lock()
	defer session.townMu.Unlock()
	return s.sendCurrentInitialTownPlayerStateLocked(session, source)
}

// sendCurrentInitialTownPlayerStateLocked sends every fallible town actor
// initialization phase before op24. The caller must hold session.townMu. A
// failure leaves the route below currentInitialTownRouteTransitionSent so the
// client remains on its current page and an exact retry can resume safely.
func (s *Service) sendCurrentInitialTownPlayerStateLocked(session *gameSession, source string) error {
	return s.sendCurrentTownPlayerStateLocked(
		session,
		source,
		currentInitialTownRoutePolicy{
			includeReportClientSpec: true,
			includeSecondFullMode1:  true,
		},
	)
}

// sendCurrentTownPlayerStateLocked applies the selected route policy to the
// shared scene-ready finalizer. The caller must hold session.townMu.
func (s *Service) sendCurrentTownPlayerStateLocked(
	session *gameSession,
	source string,
	policy currentInitialTownRoutePolicy,
) error {
	if session == nil {
		return nil
	}
	characterID := session.initialTownRouteCharacterID
	stage := session.initialTownRouteStage
	if characterID == 0 || characterID != session.selectedCharacterID ||
		stage < currentInitialTownRouteActorBound {
		return nil
	}
	if stage == currentInitialTownRoutePlayerStatePrepared ||
		stage >= currentInitialTownRoutePlayerStateSent {
		s.logGameEvent(session, "game-initial-town-player-state-duplicate-skipped",
			"source", source,
			"char_id", characterID,
			"route_stage", stage)
		return nil
	}

	// This context spans all scene snapshots, including synchronous catalog
	// work and inventory bootstrap before the final quest read. Use the
	// existing snapshot budget so those phases do not exhaust a mutation's
	// short deadline and disconnect an otherwise valid selected character.
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	charID, charName, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if charID != characterID || !hasCharacter {
		return fmt.Errorf("initial town player state character mismatch: selected=%d loaded=%d found=%t", characterID, charID, hasCharacter)
	}
	objectKey := currentSceneActorObjectKey(charID)
	sequence := "actor_bound_then_op358_pages_op376_op391_cargo_reset_op13_containers_op251_item_lock_snapshot_op898_crystal_op108_spend_time_op1240_joust_opening_op1241_joust_roster_op1340_full_mode1_pet_only_op14_slot26_op105_op102_optional_op205_expert_job_op19_op21_acceptable_op574_active_op1346_op359_op356_op9_op120_then_typed_op24"
	if policy.includeReportClientSpec {
		sequence = "actor_bound_then_op358_pages_op376_op391_cargo_reset_op13_containers_op251_item_lock_snapshot_op898_crystal_op108_spend_time_op1240_joust_opening_op1241_joust_roster_op1340_full_mode1_pet_only_op14_slot26_op105_op102_optional_op205_expert_job_op19_op21_acceptable_op574_active_op1346_op359_op356_op124_op9_op120_then_typed_op24"
	}
	s.logGameEvent(session, "game-initial-town-player-state-send",
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"route_stage", stage,
		"sequence", sequence,
		"report_client_spec_sent", policy.includeReportClientSpec,
		"action_table", "selected_before_typed_op24_final_transition_commit",
		"transition_policy", "do_not_send_op24_until_full_player_state_succeeds",
		"evidence", "user_required_pretransition_initialization_with_current_exe_typed_bodies")

	if err := s.sendCurrentSceneOverseerPages(ctx, session); err != nil {
		return err
	}
	// Select the active action table after the five overseer pages and before
	// container/player state. The final op24 transition is withheld until every
	// phase in this function has succeeded.
	if err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketPVPMissionHpPercent),
		buildCurrentActionTableStateBody(),
		0,
	); err != nil {
		return err
	}
	// Current NoPack resolves the destination container manager before it
	// installs class0/op13 rows. The actor-binding stage above must therefore
	// own the one-shot bootstrap; sending it next to the select ACK is too early
	// and leaves a visibly empty inventory even when the packet contains rows.
	// Cargo-pad state remains first so the personal-cargo capacity is valid.
	if err := s.sendCurrentCargoPadResetBeforeItemLists(session, source+"_before_full_player_state"); err != nil {
		return err
	}
	if err := s.sendCurrentSelectInventoryBootstrap(session, source+"_after_actor_before_full_player_state"); err != nil {
		return err
	}
	// Reset even a zero stage on every selected actor generation so changing
	// characters cannot inherit the previous character's purchased cap.
	if err := s.sendCurrentCarryGoldStage(session, character); err != nil {
		return err
	}
	// Current NoPack dispatches op898 only through the class0 notification
	// table. Publish the durable crystal selection after all six native
	// inventory managers exist, but before op1340/full mode1/op24 can expose
	// the town UI. The session gate makes the later client-authored op36 path an
	// idempotent fallback instead of a second initialization.
	if err := s.sendCurrentCrystalContractStateOnce(
		session,
		source+"_after_inventory_bootstrap_before_adventure_mode1",
	); err != nil {
		return err
	}
	// 冷登录只核对光环开启状态；后续原生USERINFO携带0x08权限位。
	// 不在这里向默认锁定角色发送未经请求的op863开启效果或自定义标记。
	if err := s.sendCurrentAuraSkinSlotTownUIReadyState(session); err != nil {
		return err
	}
	// The local client-PID registry deduplicates initial protected op108 traffic.
	// The catalog is followed immediately by ordinary op1206. A changed service
	// day separately forces a full catalog replacement to remove the old weekday.
	if err := s.sendCurrentSpendTimeInitialStateOnce(
		session,
		source+"_after_inventory_bootstrap_before_adventure_mode1",
	); err != nil {
		return err
	}
	// 厂商顺序：op108 活动目录 → op1206 消耗时间 → **op1303 副本次数** → op1240 马战。
	if err := s.sendCurrentRaidDailyAvailability(session, source+"_after_spend_time_before_mode1"); err != nil {
		return err
	}
	if err := s.sendCurrentRaidWeeklyAvailability(session, source+"_selected_owner_before_mode1"); err != nil {
		return err
	}
	if err := s.sendCurrentRaidChannelReady(session, source+"_selected_raid_town_before_mode1"); err != nil {
		return err
	}
	// 同窗口的第二条 init 次数报文：厂商 NOTI 0x0326 `DIMENSION_GATE_ENTRANCE_INFO`
	// （次元门/魔界裂缝每日进入次数）。厂商把它注册在 `InitPacketBuilderRegistry.cs:106`，
	// 即 `WeddingInfo` 之后、`StrikerSupportTagCharacter` 之前；我方登录进城序列是一条
	// 合并路由且不下发 WeddingInfo/StrikerSupportTag，因此放在同窗口已按厂商顺序钉过的
	// op1303 之后、op1340/mode0-mode1/定型 op24 城镇提交之前 —— 即城镇 UI 打开前的
	// init 窗口内。详见 dimension_gate_entrance.go 头部依据。
	if err := s.sendCurrentDimensionGateEntranceInfo(session, source+"_after_raid_daily_availability_before_mode1"); err != nil {
		return err
	}
	if err := s.sendCurrentParticularHellInfo(session, character); err != nil {
		return err
	}

	var legacyRepo dnfrepo.LegacyUserInfoRepository
	if repos, ok := s.repositoryGroup(); ok {
		legacyRepo = repos.LegacyUserInfo
	}
	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	adventureLevel := uint32(adventureSummary.ManageLevel)
	// sub_C7DAF0 (op1340) populates the account-wide adventure model consumed
	// by mode1/sub_2008010 before it applies the real management-level option
	// rows, including the four basic-stat bonus. The mode0/mode1 prerequisite
	// above has already created and bound the selected actor, so op1340's
	// current-object check is satisfied before the final transition commit.
	if err := s.sendCurrentAdventureInfoPushFromAccount(session, objectKey, source+"_before_mode1_adventure_model"); err != nil {
		return err
	}
	if err := s.sendCurrentAdventureExpeditionRosterAfterSelection(session); err != nil {
		return err
	}
	// ROUND 4（login_authoritative_before_commit.go）：提交前权威载体的**相对顺序**。
	//
	// 现场判决：提交前最后一件 actor 载体如果是那次 name-tag mode0 重放（只有外观表、
	// 没有耐久、没有实例种子），客户端就用这一份残缺载体定型；进副本与回城两条**正常**
	// 路径都把带 31 条装备 create 行的 mode1 放在最后（scene_post_start_map.go:176-192
	// 在前 / :381-406 在后；town_post_transition_player_state.go:118-127 在前 / :128-137 在后）。
	//
	// 修复档只把这**同一次调用**挪到装备 mode1 之前（不新增帧、不新增行、不改字节）；
	// 回退档保持今天的位置（mode1 之后）。
	authoritativeOrderFixed := loginAuthoritativeBeforeCommitEnabled()
	nameTagReplaySent := false
	nameTagReplayEvidence := "not_attempted_before_mode1"
	if authoritativeOrderFixed {
		sent, evidence, err := s.loginAuthoritativeBeforeCommitNameTagReplay(ctx, session)
		if err != nil {
			return err
		}
		nameTagReplaySent = sent
		nameTagReplayEvidence = evidence
	}
	mode1Body := s.buildCurrentSelectedUserInfoMode1BodyWithAdventureLevelInContext(
		ctx,
		session,
		legacyRepo,
		character,
		hasCharacter,
		charID,
		adventureLevel,
		currentTownActorOwnerContext(session),
	)
	if !policy.includeSecondFullMode1 {
		// Channel reconnect creates the target-owned actor with one complete
		// equipment-bearing mode1 packet. Ordinary cold/return-select routes
		// retain their original second full mode1.
		s.logPacketEvent("game-initial-town-full-mode1-duplicate-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"owner_channel", session.townActorOwnerChannel,
			"body_len", len(mode1Body),
			"reason", "route_policy_uses_one_full_equipment_mode1")
	} else {
		s.logPacketEvent("game-initial-town-full-mode1-send",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"adventure_total_point", adventureSummary.TotalPoint,
			"adventure_manage_level", adventureSummary.ManageLevel,
			"adventure_manage_option", adventureSummary.ManageOption,
			"body_len", len(mode1Body),
			"stat_blob_len", currentMode1StatBlobWireSize,
			"body_source", "current_exe_sub_2008010_existing_town_actor_real_state_and_equipment_create")
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), mode1Body, 0); err != nil {
			return err
		}
		// 记账：登录路径上这一帧带装备 create 行的 mode1 已经发出
		// （`login_mode1_last_before_commit.go`：提交前最后一条 mode1 必须是这一种）。
		s.noteLoginMode1Send(session, mode1Body, "initial_town_full_equipment_mode1_before_player_state")
	}
	session.selectedUserInfoRefreshSent = true
	// Mode1 creates the worn medal object but does not carry its guardian-gem
	// raw socket words. Rehydrate only a medal that actually has a persisted
	// guardian gem through the proved list-3 op14 carrier before the first
	// visible town transition. This is intentionally narrower than a generic
	// equipment refresh, which the current client rejects during initialization.
	if err := s.sendSelectedGuardianGemWornMedalRefresh(
		session,
		source+"_after_full_mode1_before_creature_state",
	); err != nil {
		return err
	}
	if !authoritativeOrderFixed {
		// 回退档 = 今天：name-tag mode0 重放仍在装备 mode1 之后
		//（提交前最后一件 actor 载体因此是那份残缺的外观表）。
		//
		// The scene's first mode0 is intentionally sent before full mode1 creates
		// equipment objects. A live name-tag endpoint therefore needs one mode0
		// replay here, after slot 30 exists, or sub_2008D80 discards the decoration.
		sent, evidence, err := s.loginAuthoritativeBeforeCommitNameTagReplay(ctx, session)
		if err != nil {
			return err
		}
		nameTagReplaySent = sent
		nameTagReplayEvidence = evidence
	}
	mode0AppearanceRows, mode0AppearanceSource := s.loginAuthoritativeMode0AppearanceRows(ctx, session, charID)
	s.logLoginAuthoritativeBeforeCommitOrder(
		session,
		source,
		charID,
		objectKey,
		mode1Body,
		policy.includeSecondFullMode1,
		authoritativeOrderFixed,
		nameTagReplaySent,
		nameTagReplayEvidence,
		mode0AppearanceRows,
		mode0AppearanceSource,
	)
	// Full mode1 has now created the equipped creature object. Publish the
	// absolute creature table and growth state before the typed town transition
	// so the first visible pet panel already has satiety and experience.
	if err := s.sendSelectedCreatureInitialStateAfterMode1(
		session,
		source+"_after_full_mode1_before_typed_op24",
	); err != nil {
		return err
	}
	// The expert-job manager is populated by local class0/op205 after mode1 has
	// created the current actor. The matching current-client compatibility unit
	// mirrors that validated type into the actor field consumed by the system
	// menu and self-click controls without opening the personal-info panel.
	if err := s.sendCurrentExpertJobInfoForCharacter(session, character, false); err != nil {
		return err
	}

	// Current NoPack's mode3 reader (sub_2008600) takes the selected-character
	// UI refresh path. Historical live first-login traces and the current live
	// packet log show that sending it before typed op24 opens the personal-info
	// panel. Mode1 already carries the same real 92-byte state and equipment
	// create rows, so keep this one UI-affecting refresh out of the pre-op24
	// bootstrap. A future mode3 send remains request/runtime-seed driven by
	// sendSelectedSceneUserInfoMode3RefreshOnce instead of being fabricated here.
	s.logPacketEvent("game-initial-town-mode3-pretransition-deferred",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"adventure_total_point", adventureSummary.TotalPoint,
		"adventure_manage_level", adventureSummary.ManageLevel,
		"stat_blob_len", 92,
		"reader", "current_exe_sub_2008600",
		"reason", "pre_typed_op24_mode3_opens_personal_info_panel_mode1_already_applies_real_state")
	// All five real op13 containers were installed immediately after the select
	// ACK. Generic op14 remains forbidden during initialization because current
	// EXE sub_1D73120 hardcodes the item-install notification flag to one. The
	// only exceptions are the guarded list-3/slot-32 medal rehydrated above and
	// the existing list-3/slot-26 creature row: their mode1-created objects each
	// require that specific callback to restore durable dynamic state.
	// op19 owns the skill trees and quick-slot layout. At this point the real
	// selected actor, its equipment model, and the full stat model all exist,
	// while the typed op24 transition is still withheld. This is the accepted
	// 2026-07-27 initialization boundary.
	if err := s.sendCurrentSceneSkillInfo(session, ctx, character, source+"_after_mode1_without_mode3_before_typed_op24"); err != nil {
		return err
	}
	// F1 "All" is rebuilt by the current op21 acceptable/definition list,
	// while op574 owns the live active triggers. Both handlers require the
	// selected actor to exist; mode1 + op19 is the accepted pre-op24 boundary.
	if err := s.sendCurrentInitialTownQuestSnapshotsLocked(session, source+"_after_mode1_and_op19_before_typed_op24"); err != nil {
		return err
	}
	// op1340 has already populated the model before mode1 consumes it.  Keep
	// op1346 after the full actor state: it remains the actor-bound
	// notification for the account display name and overhead/UI views.
	if err := s.sendCurrentAdventureActorRefreshFromAccount(session, objectKey, source+"_after_adventure_info_actor_bound"); err != nil {
		return err
	}
	// The normal initial-town route can commit without the optional client
	// finish-loading followup. Restore durable friends and invitations after
	// the selected actor exists, before that first visible town commit.
	if err := s.sendCurrentAccountFriendLists(session, source+"_after_adventure_actor_before_scene_commit"); err != nil {
		return err
	}
	s.refreshCurrentAccountFriendPresence(session, source+"_after_adventure_actor_before_scene_commit")
	if err := s.sendSelectedCurrentEpicProductionInfo(
		session,
		source+"_after_actor_bound_before_scene_commit",
	); err != nil {
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
		source+"_after_actor_and_quest_snapshots_before_scene_commit",
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
	// Keep the cancelled loading-cover boundary as a zero-duration seam.
	s.waitCurrentInitialTownSceneCommitLocked(session, characterID)
	if policy.includeReportClientSpec {
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketReportClientSpec), nil, 0); err != nil {
			return err
		}
	} else {
		s.logGameEvent(session, "game-channel-reconnect-report-client-spec-skipped",
			"source", source,
			"char_id", charID,
			"object_key", objectKey,
			"reason", "current_exe_cross_channel_page_state_crashes_in_op124_when_actor_binding_is_not_page_owned")
	}

	partyState := runtimePartyStateSnapshot(session)
	op9Body := buildCurrentSceneOp9ActorRemovalBodyInContext(objectKey, session.townActorOwnerChannel)
	op9Kind := currentSceneOp9ActorRemoveKind
	bodySource := "current_exe_sub_1D64CA0_kind3_remove_party_manager_record"
	if partyState.PartyID > 0 {
		// Active-party sessions receive the complete member projection from the
		// runtime party broadcaster. Retain a kind-0 registration here only for
		// that explicitly active state; an ordinary partyless login must not
		// create the client's global one-person party marker.
		op9Body = s.buildCurrentSceneOp9ActorDisplayBodyInContext(
			objectKey,
			character,
			hasCharacter,
			charName,
			session.townActorOwnerChannel,
		)
		op9Kind = currentSceneOp9ActorDisplayKind
		bodySource = "current_exe_sub_1D64CA0_kind0_active_party_registration"
	}
	s.logPacketEvent("game-initial-town-op9-party-state-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"owner_channel", session.townActorOwnerChannel,
		"party_id", partyState.PartyID,
		"record_kind", op9Kind,
		"body_len", len(op9Body),
		"body_source", bodySource)
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRecoverStamina), op9Body, 0); err != nil {
		return err
	}
	placementBody := buildCurrentSceneActorPlacementBody()
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRequestBlacklist), placementBody, 0); err != nil {
		return err
	}

	if session.initialTownRouteCharacterID == charID {
		switch {
		case session.initialTownRouteStage == currentInitialTownRouteActorBound:
			session.initialTownRouteStage = currentInitialTownRoutePlayerStatePrepared
		case session.initialTownRouteStage >= currentInitialTownRouteTransitionSent &&
			session.initialTownRouteStage < currentInitialTownRoutePlayerStateSent:
			session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
			session.townSceneReadyCharacterID = charID
		}
	}
	stage = session.initialTownRouteStage
	s.logGameEvent(session, "game-initial-town-player-state-finished",
		"source", source,
		"char_id", charID,
		"object_key", objectKey,
		"route_stage", stage,
		"next_owner", "typed_op24_final_transition_commit")
	return nil
}
