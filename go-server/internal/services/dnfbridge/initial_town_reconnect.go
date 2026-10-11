package dnfbridge

import (
	"context"
	"fmt"
)

// sendCurrentChannelReconnectTownEntry rebuilds the selected actor after the
// target-channel transport reconnect. An account-bound connection that
// completed CHANNELINFO must use the same u8 channel in every town mode0/mode1
// row; legacy/high-id fallbacks retain context zero. The connection bootstrap
// already owns the one allowed class0/op1 and endpoint success.
func (s *Service) sendCurrentChannelReconnectTownEntry(session *gameSession) error {
	if s == nil || session == nil || !session.channelReconnect || session.selectedCharacterID == 0 {
		return nil
	}
	if session.residentChannel.ID <= 0 || session.residentChannel.ID != session.channel.ID {
		return fmt.Errorf(
			"channel reconnect has no committed target identity: connected=%d resident=%d",
			session.channel.ID,
			session.residentChannel.ID,
		)
	}
	ownerChannel := currentConnectionTownActorOwnerContext(session)
	session.townActorOwnerChannel = ownerChannel
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID, characterName, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if characterID == 0 || characterID != session.selectedCharacterID || !hasCharacter {
		return fmt.Errorf(
			"channel reconnect selected character mismatch: selected=%d loaded=%d found=%t",
			session.selectedCharacterID,
			characterID,
			hasCharacter,
		)
	}
	character, townID, areaID, row, mapPath, err := s.currentChannelReconnectTownTransition(
		ctx,
		session,
		characterID,
		character,
	)
	if err != nil {
		return fmt.Errorf("load persisted channel reconnect location: %w", err)
	}
	character, err = s.commitCurrentReconnectLocation(ctx, characterID, character)
	if err != nil {
		return fmt.Errorf("commit channel reconnect location: %w", err)
	}
	transitionBody, err := buildCurrentSceneTransitionBody(
		townID,
		areaID,
		[]currentSceneTransitionRow{row},
	)
	if err != nil {
		return err
	}
	objectBody := s.buildCurrentSceneObjectListBodyForSessionInContext(
		ctx,
		session,
		characterID,
		characterName,
		character,
		hasCharacter,
		ownerChannel,
	)
	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	mode1Body := s.buildCurrentActorBindingMode1BodyForSelectedInContext(
		ctx,
		session,
		character,
		hasCharacter,
		characterID,
		uint32(adventureSummary.ManageLevel),
		ownerChannel,
	)

	session.townMu.Lock()
	resetCurrentTownPostTransitionPlayerState(session)
	session.initialTownRouteCharacterID = characterID
	session.initialTownRouteStage = currentInitialTownRouteArmed
	session.initialTownActorSceneSnapshotSent = false
	session.initialTownLocationNotificationsSent = false
	session.initialTownQuestSnapshotsSent = false
	session.initialTownSkillInfoPrepared = false
	session.initialTownSkillInfoSent = false
	session.initialTownSkillInfo = currentSceneSkillInfoProjection{}
	session.initialTownLegacySceneReadyAccepted = false
	session.initialTownAdventureOverheadRefreshSent = false
	session.initialTownCombatPowerAffixesSent = false
	session.townActorOwnerChannel = ownerChannel

	endpointPolicy := "proactive_class1_op1_context0_no_route_replay"
	if session.currentChannelResidentNoticeSent {
		endpointPolicy = "channelinfo_then_single_requested_class1_op1_no_route_replay"
	}
	s.logGameEvent(session, "game-channel-reconnect-town-entry-send",
		"char_id", characterID,
		"target_channel", session.channel.ID,
		"target_owner_channel", ownerChannel,
		"owner_server", 0,
		"owner_channel", ownerChannel,
		"town_id", townID,
		"area_id", areaID,
		"position_x", row.Value1,
		"position_y", row.Value2,
		"direction", row.Value3,
		"area_state", row.Value4,
		"map_path", mapPath,
		"sequence", "op376_mode0_light_mode1_op800_full_town_init_op124_op9_op120_op22_op23_op24_op21_op574",
		"resident_notice_sent", session.currentChannelResidentNoticeSent,
		"endpoint_policy", endpointPolicy)
	if err := s.sendCurrentChannelReconnectTownActorRoutePacketsLocked(
		session,
		characterID,
		townID,
		areaID,
		row,
		objectBody,
		mode1Body,
		transitionBody,
	); err != nil {
		session.townMu.Unlock()
		return err
	}
	routeStage := session.initialTownRouteStage
	questSnapshotsSeeded := session.initialTownQuestSnapshotsSent
	session.townMu.Unlock()

	// The pre-op24 pair seeds the task manual while the actor is being rebuilt.
	// Refresh it once more after the actor-ready boundary so NPC markers reflect
	// repository/PVF state without accepting or completing a quest.
	if routeStage >= currentInitialTownRouteTransitionSent && questSnapshotsSeeded {
		if err := s.sendCurrentAcceptableQuestListForSession(
			session,
			"channel_reconnect_post_op24_actor_ready_refresh",
		); err != nil {
			return err
		}
	}
	session.channelReconnect = false
	session.sceneBootstrapTailDeferred = false
	session.sceneBootstrapTailSent = true
	session.townActorOwnerChannel = ownerChannel
	// 路由收尾：**发布城镇存在感**（厂商参考实现同一条口径）。
	//
	// 参考实现（D:\DNF90-test\server\runtime\logs\packet_log.txt，行号为原文行号）里，
	// 这条路由的收尾有三条固定成对的事件，顺序也是固定的：
	//
	//	:2574 game-channel-reconnect-town-entry-send ...
	//	:2760 game-town-presence-published source=channel_reconnect_town_actor_route_complete
	//	      char_id=1 town_id=38 area_id=0 instance_key=0 position_x=1677 position_y=222
	//	      direction=5 area_state=3 peer_count=0
	//	:2771 game-channel-reconnect-town-entry-finished ...
	//
	// 三次出现（:2760 char1 / :3146 char2 / :16452 char1）**每一次之后数秒内都跟着一次
	// 成功的邀请**（:3309 / :16595 `game-peer-request-accepted`）。我们这条路由此前只重建
	// actor、从不登记区域存在（`EnterArea` 的四个生产调用点里没有它），于是走频道重连进城
	// 的角色在 `OnlinePlayerManager` 里**根本不存在** ⇒ `PeerInSameArea` 恒为 false ⇒
	// 邀请在 `party_online.go:128-136` 被静默吞掉（客户端"点了没反应"），也就是操作者报的
	// 「组不上队」。这里按同一条口径在收尾补齐，与 op36 那条路（town_move.go:372）落进
	// 同一个分桶键，两条路互不冲突（同键重复登记是幂等的换桶 + 一次同屏投影）。
	//
	// 回退：`DNFBRIDGE_CHANNEL_RECONNECT_TOWN_PRESENCE_PUBLISH=off`。
	if currentChannelReconnectTownPresencePublishEnabled() {
		s.publishTownPresenceInArea(
			session,
			townID,
			areaID,
			row.Value1,
			row.Value2,
			row.Value3,
			row.Value4,
			channelReconnectTownPresenceSource,
		)
	}
	s.logGameEvent(session, "game-channel-reconnect-town-entry-finished",
		"char_id", characterID,
		"target_channel", session.channel.ID,
		"route_stage", routeStage,
		"resident_notice_sent", session.currentChannelResidentNoticeSent,
		"town_actor_owner_channel", session.townActorOwnerChannel,
		"report_client_spec_sent", true,
		"final_packet", "class0_op574_post_op24_actor_ready_refresh")
	return nil
}
