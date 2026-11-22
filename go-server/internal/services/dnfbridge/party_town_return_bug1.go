package dnfbridge

import (
	"context"
	"os"
	"strings"
	"sync/atomic"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// bug1TownReturnKind2RefreshEnv 控制「回城时用 kind2 原地刷新副本作用域记录的成员表」。
//
// kind2 的语义就是"只替换这条记录的成员表"：不新建记录、不移动归属、不删除。
// 回城真正需要做的只有一件事——把"存活者名单"写进客户端当前使用的那张表；
// 而 kind3（删除）会命中客户端正在用的记录，直接触发 0x02017882（"队伍已解散"）。
//
// 默认（未设置 / on）= 发 kind2；legacy / off / 0 / false / no = 回到 kind3 退役老行为。
const bug1TownReturnKind2RefreshEnv = "DNFBRIDGE_PARTY_TOWN_RETURN_KIND2_REFRESH"

func bug1TownReturnKind2RefreshEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(bug1TownReturnKind2RefreshEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

// bug1RetireDungeonPartyAliasesEnv 控制「回城重建城镇组队投影之前，先把副本与城镇作用域
// 的旧 op9 记录用 kind3 退役掉」。
//
// 默认（未设置 / on）= 退役；legacy / off / 0 / false / no = 回到改动前。
const bug1RetireDungeonPartyAliasesEnv = "DNFBRIDGE_PARTY_TOWN_RETURN_RETIRE_ALIASES"

func bug1RetireDungeonPartyAliasesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(bug1RetireDungeonPartyAliasesEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

// bug1TownReturnEndpointReplayEnv 控制「回城重绑时是否重发一次对端端点（op11）」。
//
// 现场（2026-11-17 01:16，4 人→中退 1 人→通关回城）：
//   - 组队成功窗口：op11 发送 6 次；
//   - 回城重绑窗口：op11 发送 0 次（队长与两名队员都是 0）；
//   - 表现：名册（op153+op9）都到了，但客户端场景重建后没有任何 P2P 端点可用，
//     握手帧根本发不出去 ⇒ 队伍框一直"连接中.."，直到有人退队（退队走整份快照，
//     那条路径带 op11）才突然连上——这正是操作者看到的"队长退队后另外两个人能连上"。
//
// 顺序必须是 **op11 先于 op9**：真正触发握手的是 op9；v5 注释里"重放 op11 会停在
// 连接中"的现场，op11 是被放在 op9 之后的。组队路径的顺序也是 op11 → op153 → op9。
//
// 默认（未设置 / on）= 重发；legacy / off / 0 / false / no = 回到改动前（不重发）。
const bug1TownReturnEndpointReplayEnv = "DNFBRIDGE_PARTY_TOWN_RETURN_ENDPOINT_REPLAY"

func bug1TownReturnEndpointReplayEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(bug1TownReturnEndpointReplayEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

func armDungeonReturnAliasRetirePending(session *gameSession) {
	if session == nil {
		return
	}
	atomic.StoreUint32(&session.dungeonReturnAliasRetirePending, 1)
}

func consumeDungeonReturnAliasRetirePending(session *gameSession) bool {
	if session == nil {
		return false
	}
	return atomic.CompareAndSwapUint32(&session.dungeonReturnAliasRetirePending, 1, 0)
}

// allSameChannelPartyMembersOutsideDungeon 判断当前队伍中所有在线且同频道的成员是否都已经脱离副本运行时。
// 只有当同频道队友全部回到城镇（彼此的 town actor 已通过 broadcastTownPlayerEnter 创建完毕）时，
// 才在 [town_local_actor+0x4C8] == 0 的状态下执行一次 retireDungeonPartyAliasRecords + sendRuntimePartyRosterLocal，
// 避免先回城的成员提前绑定 [town_local_actor+0x4C8] 后再被二次 kind3 触发 0x201786B 队伍解散。
func (s *Service) allSameChannelPartyMembersOutsideDungeon(receiver *gameSession, state alignedcmd.PartyState) bool {
	if s == nil || receiver == nil {
		return false
	}
	members := runtimePartyMembers(state)
	if len(members) <= 1 {
		return true
	}
	receiverChannelID := currentTownPresenceChannelID(receiver)
	for _, member := range members {
		if member.UserID == 0 || member.UserID == receiver.selectedCharacterID {
			continue
		}
		memberSession, online := s.onlineGameSession(member.UserID)
		if !online || memberSession == nil {
			continue
		}
		if currentTownPresenceChannelID(memberSession) != receiverChannelID {
			continue
		}
		memberSession.dungeon.mu.Lock()
		inDungeon := memberSession.dungeon.runtime != nil || memberSession.confirmedDungeonReturnStatePending
		memberSession.dungeon.mu.Unlock()
		if inDungeon || !currentPartyTownReturnSceneReady(memberSession) {
			s.logGameEvent(receiver, "game-party-town-return-all-ready-gate",
				"gate", "member_in_dungeon_or_scene_not_ready",
				"receiver_char_id", receiver.selectedCharacterID,
				"member_user", member.UserID,
				"member_in_dungeon", inDungeon,
				"member_scene_ready", currentPartyTownReturnSceneReady(memberSession),
				"party_id", state.PartyID)
			return false
		}
		if s.onlinePlayers != nil {
			peerInfo, found := s.onlinePlayers.PlayerForCharacter(member.UserID)
			if !found || peerInfo.Session != memberSession {
				s.logGameEvent(receiver, "game-party-town-return-all-ready-gate",
					"gate", "member_presence_missing",
					"receiver_char_id", receiver.selectedCharacterID,
					"member_user", member.UserID,
					"presence_found", found,
					"presence_session_matches", found && peerInfo.Session == memberSession,
					"party_id", state.PartyID)
				return false
			}
		}
	}
	return true
}

// sendRuntimePartyTownReturnRebind 在回城同屏 actor 重建完成后，按顺序执行：
//  1. NOTI3(state=0)：把客户端的"副本进行中"清零（客户端只在城镇态下才肯切记录）；
//  2. op11 对端端点（每次回城只发一次）：op24 重建城镇角色会清空客户端 P2P 槽位；
//  3. op153 + op9（owner = 频道号）：切到城镇那条记录，**op9 触发握手**；
//  4. 收副本作用域（owner=0）的旧记录：只删非队员，不删本机可能正在用的那条；
//  5. op153 实时名册。
//
// 为什么顺序是"切在前、删在后"：删（kind3）只有在被删记录**不是**客户端当前使用的那条
// 时才是静默的。中途有人退图时，副本内为更新人数发过 record(self, 0)，客户端当前用的
// 正是那条；先删它就命中 0x02017882（"队伍已解散"）→ 掐断 P2P → 一直"连接中.."。
//
// 为什么必须重发 op11：现场（2026-11-17 01:16）组队窗口 op11 发 6 次、回城窗口发 0 次，
// 名册到了但客户端没有任何端点可用，握手发不出去。而"队长退队后另外两人能连上"正是
// 因为退队走整份快照（带 op11）。顺序必须 op11 先于 op9——v5 注释里"重放 op11 会停在
// 连接中"的现场是把它放在了 op9 之后。
func (s *Service) sendRuntimePartyTownReturnRebind(
	session *gameSession,
	state alignedcmd.PartyState,
	source string,
) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	receiverState := stateForRuntimePartyReceiver(state, session.selectedCharacterID)
	multiMember := len(runtimePartyMembers(receiverState)) >= 2
	// consumeDungeonReturnAliasRetirePending 只消费一次：同一次回城由 queueTownPeerDelivery
	// 触发的二次重绑只刷新名册，不会再重连/重刷一遍。
	firstReturnRebind := multiMember && consumeDungeonReturnAliasRetirePending(session)
	// 1. 先清零"副本进行中"：客户端只在城镇态下才会把当前使用的记录切到频道号那条。
	s.resetRuntimePartyTownReturnUserState(session, receiverState)
	// 2. 重发对端端点（必须排在 op9 之前，op9 才是触发握手的那一帧）。
	if firstReturnRebind && bug1TownReturnEndpointReplayEnabled() {
		// 先确保中继端口在监听（尤其本局发生过中途退图、或中继刚被重建），再发 op11 端点，
		// 否则 op11 里的端口背后没人听 → 客户端长时间"连接中.."。
		s.syncRuntimePartyUDPRelay(receiverState)
		if _, err := s.sendRuntimePartyPeerEndpointInfo(session, receiverState, s.partyFormationOrder()); err != nil {
			return err
		}
	}
	// 3. 刷新本机主记录 record(self,ch)：保持 wasBound==1 && isNowBound==1 平滑更新，
	//    不触发 .gld127 与 P2P->closeAll()。
	if err := s.sendRuntimePartyRosterLocal(session, receiverState, source); err != nil {
		return err
	}
	// 3.5 绑定已回到 record(self,ch)，静默退役退图者的 (D,0) 与 (D,ch)。
	if err := s.retireDepartedPartyAliasRecordsAfterSelfRebind(session, receiverState, source+"_retire_departed_aliases"); err != nil {
		return err
	}
	// 4. 副本作用域（owner=0）的旧记录：**只改成员表，不删记录**。
	//    kind2 对没被使用的记录是空操作，因此可以对每个候选 key 都发一次，
	//    不需要先知道客户端当前用的是哪一条。
	if firstReturnRebind {
		switch {
		case bug1TownReturnKind2RefreshEnabled():
			if err := s.refreshDungeonScopedPartyMemberTables(session, receiverState, source+"_kind2_refresh"); err != nil {
				return err
			}
		case bug1RetireDungeonPartyAliasesEnabled():
			// 回退档：老的 kind3 退役行为。
			if err := s.retireDungeonPartyAliasRecords(session, runtimePartyMembers(receiverState), source+"_retire_aliases"); err != nil {
				return err
			}
		}
	}
	// 5. 实时名册。
	if err := s.sendRuntimePartyRealtimeInfoLocal(session, receiverState); err != nil {
		return err
	}
	return nil
}

// refreshDungeonScopedPartyMemberTables 对副本作用域（owner = 0）的候选记录各发一条
// kind2，把成员表原地改成"当前存活者"。
//
// 为什么是 kind2 而不是 kind3：
//   - kind3 会删除这条记录；如果它正是客户端当前使用的那条，客户端会打印"队伍已解散"、
//     掐断全部 P2P、清空槽位——这就是"回城重组 + 连接中"的直接来源；
//   - kind2 只替换成员表：不建记录、不移动归属、不删除，对没被使用的 key 是空操作。
//
// 候选 key 取三类：本机、每位存活队友、以及同区域已不在队伍里的角色（中途退图者）。
// 退图者那条记录里可能还留着旧人数，靠这一条把它也改成存活者名单。
func (s *Service) refreshDungeonScopedPartyMemberTables(receiver *gameSession, state alignedcmd.PartyState, source string) error {
	if s == nil || receiver == nil || receiver.selectedCharacterID == 0 || state.PartyID <= 0 {
		return nil
	}
	members := runtimePartyMembers(state)
	if len(members) < 2 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	batch := currentPartyFrameCharacterBatch(s, nil)
	projections := make([]currentSceneOp9PartyMemberProjection, 0, len(members))
	for _, member := range members {
		if member.UserID == 0 {
			continue
		}
		identity, found := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !found {
			continue
		}
		memberID, memberName, memberCharacter, hasCharacter := batch.loadCharacter(ctx, identity)
		if memberID != member.UserID {
			continue
		}
		level := byte(1)
		job := byte(0)
		grow := byte(0)
		if hasCharacter {
			if memberCharacter.Level > 0 && memberCharacter.Level < 256 {
				level = byte(memberCharacter.Level)
			}
			job = byte(numericCharacterStat(memberCharacter.Job))
			grow = characterGrowTypeByte(memberCharacter)
		}
		projections = append(projections, currentSceneOp9PartyMemberProjection{
			State: member,
			Name:  memberName,
			Job:   job,
			Level: level,
			Grow:  grow,
		})
	}
	projections = currentSceneOp9PartyMembers(projections)
	if len(projections) < 2 {
		return nil
	}
	leaderSlot := currentSceneOp9PartyLeaderSlot(projections, state.UserID)
	localChannelID := currentClientSelectedChannelID(receiver)
	route := s.currentSceneActorRoute()

	keys := make([]uint16, 0, len(members)+4)
	seen := make(map[uint16]bool, len(members)+4)
	for _, member := range members {
		if member.UserID == 0 {
			continue
		}
		if seen[member.UserID] {
			continue
		}
		seen[member.UserID] = true
		keys = append(keys, member.UserID)
	}
	if s.onlinePlayers != nil {
		for _, peer := range s.onlinePlayers.PeersInSameArea(receiver.selectedCharacterID) {
			if peer.CharacterID == 0 || seen[peer.CharacterID] {
				continue
			}
			seen[peer.CharacterID] = true
			keys = append(keys, peer.CharacterID)
		}
	}
	// 两套 owner 都要覆盖：副本作用域（0）与城镇作用域（频道号）。
	//
	// 现场（2026-11-17 12:46）：队长客户端"我的队伍"绑在**中途退队者**的城镇记录
	// (该角色, 频道号) 上，而这一版之前只刷了 owner=0 ⇒ 刷了等于没刷，血条里被踢的人
	// 一直留着红电脑；后来那位退队者加入新队伍、服务端把那条记录重新声明成新队伍，
	// 队长就跟着"自动解散"了。
	sent := 0
	for _, characterID := range keys {
		objectKey := currentSceneActorObjectKey(characterID)
		if objectKey == 0 {
			continue
		}
		body := buildCurrentSceneOp9RetainedPartyBody(objectKey, currentSceneObjectContext, localChannelID, projections, leaderSlot, route)
		if _, err := s.sendRetiredPartyPacket(receiver, uint16(dnfenum.CmdPacketRecoverStamina), body, &state, nil, false); err != nil {
			return err
		}
		sent++
	}
	s.logGameEvent(receiver, "game-party-town-return-member-tables-refreshed",
		"source", source,
		"char_id", receiver.selectedCharacterID,
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"member_count", len(projections),
		"candidate_keys", len(keys),
		"member_tables_updated", sent,
		"reason", "kind2_only_replaces_the_member_table_and_never_destroys_a_record")
	return nil
}

// resetRuntimePartyTownReturnUserState 用 NOTI3(state=0) 把客户端 [CUser+0x4C]
// 的"副本进行中"标记清零，消除回城后队伍信息里残留的绿色 [进行中]。
func (s *Service) resetRuntimePartyTownReturnUserState(session *gameSession, state alignedcmd.PartyState) {
	if s == nil || session == nil {
		return
	}
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 {
			continue
		}
		objectKey := currentSceneActorObjectKey(member.UserID)
		if objectKey == 0 {
			continue
		}
		body, err := buildCurrentPVPUserStateBodyWithState(objectKey, 0)
		if err != nil {
			continue
		}
		_ = s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyUserState), body, 0)
	}
}

// retireDungeonPartyAliasRecords 在回城后首次重建城镇组队投影（sendRuntimePartyRosterLocal）之前，
// 趁 [town_local_actor+0x4C8] 仍为 0（before==0 && after==0，不会触发 0x201786B 队伍解散），
// 把副本作用域（owner = 0）与城镇作用域（owner = channel）的非本机旧 op9 记录逐条用 kind3 静默清槽。
func (s *Service) retireDungeonPartyAliasRecords(receiver *gameSession, members []alignedcmd.PartyMemberState, source string) error {
	if s == nil || receiver == nil || receiver.selectedCharacterID == 0 {
		return nil
	}
	route := s.currentSceneActorRoute()
	memberSet := make(map[uint16]bool, len(members)+1)
	memberSet[receiver.selectedCharacterID] = true
	for _, member := range members {
		if member.UserID != 0 {
			memberSet[member.UserID] = true
		}
	}

	nonPartyIDs := make([]uint16, 0, 4)
	peerIDs := make([]uint16, 0, len(members))
	ordered := make([]uint16, 0, len(members)+4)
	seen := make(map[uint16]bool, len(members)+4)
	if s.onlinePlayers != nil {
		for _, peer := range s.onlinePlayers.PeersInSameArea(receiver.selectedCharacterID) {
			if peer.CharacterID == 0 || memberSet[peer.CharacterID] || seen[peer.CharacterID] {
				continue
			}
			seen[peer.CharacterID] = true
			nonPartyIDs = append(nonPartyIDs, peer.CharacterID)
			ordered = append(ordered, peer.CharacterID)
		}
		receiverChannelID := currentTownPresenceChannelID(receiver)
		s.onlinePlayers.mu.RLock()
		var sameChannelCandidates []onlinePlayerInfo
		for area, bucket := range s.onlinePlayers.byArea {
			if area.ChannelID != receiverChannelID {
				continue
			}
			for _, info := range bucket {
				if info != nil && info.CharacterID != 0 && !memberSet[info.CharacterID] && !seen[info.CharacterID] {
					sameChannelCandidates = append(sameChannelCandidates, *info)
				}
			}
		}
		s.onlinePlayers.mu.RUnlock()
		for _, cand := range sameChannelCandidates {
			if seen[cand.CharacterID] {
				continue
			}
			seen[cand.CharacterID] = true
			nonPartyIDs = append(nonPartyIDs, cand.CharacterID)
			ordered = append(ordered, cand.CharacterID)
		}
	}
	for _, member := range members {
		if member.UserID == 0 || member.UserID == receiver.selectedCharacterID || seen[member.UserID] {
			continue
		}
		seen[member.UserID] = true
		peerIDs = append(peerIDs, member.UserID)
		ordered = append(ordered, member.UserID)
	}
	if !seen[receiver.selectedCharacterID] {
		seen[receiver.selectedCharacterID] = true
		ordered = append(ordered, receiver.selectedCharacterID)
	}

	retire := func(characterID uint16, owner byte) error {
		objectKey := currentSceneActorObjectKey(characterID)
		if objectKey == 0 {
			return nil
		}
		body := buildCurrentSceneOp9ActorRemovalBodyInContext(
			objectKey,
			owner,
			route,
		)
		return s.sendGameUpperRawClass(
			receiver,
			uint16(dnfenum.CmdPacketRecoverStamina),
			body,
			0,
		)
	}

	// 1. 副本作用域（owner = 0）：只退役已退队的散人（nonPartyIDs）。
	//
	// 本机那条在多人队伍里**不删**：中途有人退图时，副本内为了更新人数发过
	// record(self, 0)，客户端当前使用的可能正是这一条；删它会命中 0x02017882
	// （"队伍已解散"）、UDPManager->closeAll() 掐断全部 P2P、CParty::ClearAll 清槽。
	// 它不在任何人的"非队员"名单里，留着不影响队友，只会作为一条旧记录留在本机。
	state := stateForRuntimePartyReceiver(s.townCopresenceActorPartyState(receiver), receiver.selectedCharacterID)
	inMultiMemberParty := state.PartyID > 0 && len(runtimePartyMembers(state)) >= 2
	for _, id := range nonPartyIDs {
		if err := retire(id, currentSceneObjectContext); err != nil {
			return err
		}
	}
	if !inMultiMemberParty {
		if err := retire(receiver.selectedCharacterID, currentSceneObjectContext); err != nil {
			return err
		}
	}

	// 2. 城镇作用域（owner = channel）：
	// 若当前仍在多人队伍中，绝不退役 record(self, channel) 且绝不使用 handoffPeer 借槽（避免触发
	// 0x0201786B 静默解散与清零 [actor+0x4C8]），仅清理 nonPartyIDs 与 peerIDs 的别名记录；
	// 随后由 sendRuntimePartyRosterLocal 的 kind0 原位更新 record(self, channel) 并通过
	// .prsafe (0x06EC5334..0x06EC536B) 把本机与所有队友的新 town actor 绑回 record(self, channel)。
	if channel := currentClientSelectedChannel(receiver); channel != currentSceneObjectContext {
		for _, id := range nonPartyIDs {
			if err := retire(id, channel); err != nil {
				return err
			}
		}
		if !inMultiMemberParty {
			if err := retire(receiver.selectedCharacterID, channel); err != nil {
				return err
			}
		} else {
			for _, id := range peerIDs {
				if err := retire(id, channel); err != nil {
					return err
				}
			}
		}
	}

	s.logGameEvent(receiver, "game-party-town-return-dungeon-alias-retired",
		"source", source,
		"char_id", receiver.selectedCharacterID,
		"retired_peers", len(ordered)-1,
		"owner_channel", currentSceneObjectContext,
		"switch", bug1RetireDungeonPartyAliasesEnv,
		"self_dungeon_record_retained", inMultiMemberParty,
		"party_member_count", len(runtimePartyMembers(state)),
		"reason", "kind3_non_party_records_only_when_multi_member_keeps_the_self_dungeon_record")
	return nil
}
