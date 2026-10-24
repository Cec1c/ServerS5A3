package dnfbridge

import (
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

// runtimePartyManagerForService is intentionally lazy because many protocol
// tests construct Service directly. Production initialization still creates it
// with Service, so all live party mutations share one central manager.
func (s *Service) runtimePartyManagerForService() *party.RuntimePartyManager {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimePartyManager == nil {
		s.runtimePartyManager = party.NewRuntimePartyManager()
	}
	return s.runtimePartyManager
}

func (s *Service) runtimePartyMemberForIdentity(identity boundGameSessionCharacter) (party.RuntimePartyMember, bool) {
	if identity.session == nil || identity.character == 0 || identity.generation == 0 || !s.boundGameSessionCharacterCurrent(identity) {
		return party.RuntimePartyMember{}, false
	}
	state := runtimePartyStateSnapshot(identity.session)
	member := runtimePartyMember(identity.session, state)
	return party.RuntimePartyMember{
		UserID:            identity.character,
		SessionGeneration: identity.generation,
		State:             member,
	}, true
}

func (s *Service) publishRuntimePartySnapshot(snapshot party.RuntimePartySnapshot) alignedcmd.PartyState {
	if s == nil || snapshot.ID == 0 {
		return alignedcmd.PartyState{}
	}
	for _, member := range snapshot.Members {
		identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok {
			// 该角色当前没有在线会话：离线成员不画名单是正常语义，保持静默。
			continue
		}
		if identity.generation != member.SessionGeneration {
			// 该角色换了一条会话（辅助/替换 socket、通道重连）：权威名单里的代际还是
			// "入队时那条会话"的。这一档过去是**静默 continue** —— 名单既没写进任何
			// 会话，日志里也一个字都没有，现场 2026-09-26 HK 的"组队成功但队长选图时
			// party_id=0"因此无法从日志分辨。这里只补一条具名事件（不发包、不改行为）：
			// 下一次复测单看这一行就能确认"入队那条会话 ≠ 现在的会话"。
			s.logGameEvent(identity.session, "game-party-snapshot-member-session-generation-stale",
				"conn_id", identity.session.connID,
				"char_id", member.UserID,
				"party_id", snapshot.ID,
				"leader_char_id", snapshot.Leader,
				"member_session_generation", identity.generation,
				"party_recorded_session_generation", member.SessionGeneration,
				"reason", "runtime_party_records_a_different_session_generation_for_this_member")
			continue
		}
		storeRuntimePartyState(identity.session, snapshot.StateFor(member.UserID))
		// 记下队长会话的权威队伍号，供 handleOnlineChangePartyHost 认重发。
		// 普通队员保持零值，避免一条本来就无权的 op121 被误认成委任重发。
		// 成功的 transfer 会给退位队长单独写上"新队长 + 新队号"。
		if snapshot.Leader == member.UserID {
			identity.session.lastPublishedPartyLeader = snapshot.Leader
			identity.session.lastPublishedPartyID = int(snapshot.ID)
		}
	}
	return snapshot.StateFor(snapshot.Leader)
}

// publishRuntimePartyMembershipSnapshot keeps a one-member manager lobby
// available for the next invite, but deliberately removes its client-facing
// party projection after a leave/kick/disconnect mutation.  The current EXE
// treats a nonzero cached party id as dungeon leader authority even when its
// roster only has one row.  Replaying such a singleton after a successful
// leave is what left the player unable to enter alone.
func (s *Service) publishRuntimePartyMembershipSnapshot(snapshot party.RuntimePartySnapshot) alignedcmd.PartyState {
	state := s.publishRuntimePartySnapshot(snapshot)
	if len(snapshot.Members) > 1 {
		return state
	}
	// 队伍缩到 1 人/解散：这个队伍再也不会有人进图，立即回收它的队伍级副本实例
	// （阶段二的回收时机之一；参考实现里对应 TryBeginEnding/TryMarkEnded）。
	s.recyclePartyDungeonInstances(int(snapshot.ID), "party_disbanded")
	for _, member := range snapshot.Members {
		s.clearRuntimePartyProjection(member.UserID, member.SessionGeneration)
	}
	return alignedcmd.PartyState{}
}

func (s *Service) clearRuntimePartyProjection(userID uint16, generation uint64) {
	identity, ok := s.onlineGameSessionCharacterSnapshot(userID)
	if !ok || identity.generation != generation {
		return
	}
	storeRuntimePartyState(identity.session, alignedcmd.PartyState{})
}

// runtimePartyStateForSession 解析"这个会话所属角色**当前权威的**队伍投影"。
//
// # 为什么不能只读会话本地缓存（现场缺陷，2026-09-26 HK）
//
// `session.party.state` 只是一份投影：`publishRuntimePartySnapshot` 只把权威名单写进
// **角色目录**（`s.gameSessions[charID]`）里的那一条会话。同一角色的第二条连接
// （当前客户端会为同角色开短命的辅助 transport —— 见 online_player_manager.go 的
// `LeaveAreaSession` 注释；以及通道重连时的替换 socket）走
// `bindGameSessionCharacter` 的早退分支：它**刻意不夺走**角色目录，只在绑定那一刻
// 继承一份拷贝，此后再也收不到任何 publish。
//
// 于是现场这条链是自洽的：
//
//	① 组队成立（`game-party-join-authoritative-snapshot-sent party_id=1 member_count=2`）
//	   —— 权威名单写进了角色目录那条会话；
//	② 队长选图那次 op16 落在**另一条**同角色会话上；
//	③ 后者读到的是"组队之前"的空缓存 ⇒ `party_id=0 member_count=0`，
//	   进图静默退化成单人：barrier / 队友端点 / 房间槽位表 / op800 全部 skipped。
//
// 因此副本进图这条链一律以中央 RuntimePartyManager 为准。查询**按角色**（generation 传 0）：
// 权威名单里的代际是"入队时那条会话"的代际，辅助/替换 socket 的代际天然不同，
// 用代际过滤会把正要看的那条队伍过滤掉。只有中央完全没有这个角色时，才回退到会话
// 本地缓存（旧客户端投影、单会话夹具与测试路径），行为与改动前逐字节相同。
//
// 解析结果会写回会话缓存（只写权威值），这样同一条调用链上其余仍然读
// `runtimePartyStateSnapshot(session)` 的旧代码看到的是同一份数据；发生修复时留一条
// `game-party-session-projection-repaired`，便于线上一次复测就看清是哪条会话缺投影。
func (s *Service) runtimePartyStateForSession(session *gameSession) alignedcmd.PartyState {
	cached := runtimePartyStateSnapshot(session)
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return cached
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return cached
	}
	snapshot, found := manager.SnapshotByUser(session.selectedCharacterID, 0)
	if !found {
		s.logRuntimePartyMembershipMiss(session, cached, manager)
		return cached
	}
	authoritative := snapshot.StateFor(session.selectedCharacterID)
	if authoritative.PartyID <= 0 {
		return cached
	}
	if cached.PartyID != authoritative.PartyID ||
		len(runtimePartyMembers(cached)) != len(runtimePartyMembers(authoritative)) {
		storeRuntimePartyState(session, authoritative)
		s.logGameEvent(session, "game-party-session-projection-repaired",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"character_generation", session.characterGeneration,
			"party_id", authoritative.PartyID,
			"leader_char_id", authoritative.UserID,
			"member_count", len(runtimePartyMembers(authoritative)),
			"cached_party_id", cached.PartyID,
			"cached_member_count", len(runtimePartyMembers(cached)),
			"reason", "authoritative_runtime_party_was_not_projected_onto_this_session")
	}
	return authoritative
}

// logRuntimePartyMembershipMiss 在"按角色查权威队伍没查到、于是退回会话本地缓存"这一档
// 留下一次可判读的现场快照。默认**只在管理器里确实存在队伍时才记**（服务器上没有任何队伍
// 时这是完全正常的状态，不能刷日志）。
//
// 现场判读（2026-09-26 HK：队长选图 `${party_id}=0` 而查不到任何东西）：
//   - `manager_party_count=0` ⇒ 这个进程的管理器此刻**一支队伍都没有**（角色被摘过、
//     或队伍从未在本实例建立）；
//   - `character_member_row_in_manager=true` 而查询未命中 ⇒ 管理器里**有**这个角色的成员
//     记录，是键/代际口径的问题（那就是真 bug，不是状态问题）；
//   - `manager_parties=1:leader=4:members=4,1` 这种串给出管理器实际持有的
//     队伍号/队长/成员，一眼能看出"队伍在，只是不在这个角色名下"。
func (s *Service) logRuntimePartyMembershipMiss(
	session *gameSession,
	cached alignedcmd.PartyState,
	manager *party.RuntimePartyManager,
) {
	if s == nil || session == nil || manager == nil {
		return
	}
	snapshots := manager.Snapshots()
	if len(snapshots) == 0 {
		return
	}
	characterID := session.selectedCharacterID
	characterMemberRow := false
	recordedGeneration := uint64(0)
	parts := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		memberIDs := make([]string, 0, len(snapshot.Members))
		for _, member := range snapshot.Members {
			memberIDs = append(memberIDs, strconv.FormatUint(uint64(member.UserID), 10))
			if member.UserID == characterID {
				characterMemberRow = true
				recordedGeneration = member.SessionGeneration
			}
		}
		parts = append(parts, fmt.Sprintf("%d:leader=%d:members=%s",
			snapshot.ID, snapshot.Leader, strings.Join(memberIDs, ",")))
	}
	s.logGameEvent(session, "game-party-authoritative-membership-missing",
		"conn_id", session.connID,
		"char_id", characterID,
		"character_generation", session.characterGeneration,
		"lookup_id_space", "character_id",
		"cached_party_id", cached.PartyID,
		"cached_member_count", len(runtimePartyMembers(cached)),
		"manager_party_count", len(snapshots),
		"manager_parties", strings.Join(parts, "|"),
		"character_member_row_in_manager", characterMemberRow,
		"recorded_member_generation", recordedGeneration,
		"live_same_character_conns", s.liveSameCharacterConnIDs(characterID, session),
		"reason", "runtime_party_manager_has_no_membership_for_this_character")
}

func (s *Service) createOrJoinManagedRuntimeParty(joiner, anchor *gameSession) (alignedcmd.PartyState, party.RuntimePartyResult, bool) {
	if s.currentFiendWarAutomaticPartyOnly(joiner) || s.currentFiendWarAutomaticPartyOnly(anchor) {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "fiendwar_automatic_encounter_party_only"}, false
	}
	joinerIdentity, joinerOK := s.boundGameSessionCharacterSnapshot(joiner)
	anchorIdentity, anchorOK := s.boundGameSessionCharacterSnapshot(anchor)
	if !joinerOK || !anchorOK || joinerIdentity.character == anchorIdentity.character {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "invalid_session"}, false
	}
	// 频道隔离（彻底隔离口径）：跨频道不得建立/加入队伍。
	// 放在这个唯一入口上，是因为 op705（直接入队/接受后落队）与 op90（队伍目录快速加入）
	// 都汇到这里；拒绝原因会沿 result.Reason 传到各自的日志与失败包里。
	if s.partyChannelMismatch(joiner, anchor) {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: partyMemberChannelMismatchReason}, false
	}
	manager := s.runtimePartyManagerForService()
	joinerMember, joinerMemberOK := s.runtimePartyMemberForIdentity(joinerIdentity)
	anchorMember, anchorMemberOK := s.runtimePartyMemberForIdentity(anchorIdentity)
	if manager == nil || !joinerMemberOK || !anchorMemberOK {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "stale_session"}, false
	}
	// SET_PARTY_INFO (op12) is a local client-window operation. It can create
	// a one-member projection on the invitee before that player accepts an
	// incoming invite, but it must never steal leadership from the inviter.
	// Resolve/import the packet target first: accepting or directly entering a
	// target always joins the target's party, creating that target's lobby when
	// it does not yet own one. A joiner's old central party is then detached by
	// RuntimePartyManager.Join as one atomic transition.
	anchorParty, anchorFound := s.bootstrapManagedRuntimePartyForSession(anchor)
	var result party.RuntimePartyResult
	if anchorFound {
		// Import a pre-central joiner's legacy projection only after the target
		// is known to own a party. This retains a real old-party survivor
		// transition during deployment, while a partyless target still wins over
		// an invitee's transient op12 singleton.
		if _, joinerManaged := manager.SnapshotByUser(joinerIdentity.character, joinerIdentity.generation); !joinerManaged &&
			runtimePartyStateSnapshot(joiner).PartyID > 0 {
			if imported, importedOK := s.bootstrapManagedRuntimePartyForSession(joiner); importedOK && imported.ID == anchorParty.ID {
				return s.publishRuntimePartySnapshot(anchorParty), party.RuntimePartyResult{OK: true, Party: anchorParty, TargetUser: joinerIdentity.character}, true
			}
		}
		result = manager.Join(anchorParty.ID, joinerMember)
	} else {
		// EnsureLeader is idempotent for concurrent invite/entry packets. It
		// never detaches a just-created target lobby as Create would.
		created := manager.EnsureLeader(anchorMember, runtimePartyStateSnapshot(anchor))
		if !created.OK {
			return alignedcmd.PartyState{}, created, false
		}
		result = manager.Join(created.Party.ID, joinerMember)
		if result.PriorLeave == nil && created.PriorLeave != nil {
			result.PriorLeave = created.PriorLeave
		}
	}
	if !result.OK {
		return alignedcmd.PartyState{}, result, false
	}
	if err := s.reconcileManagedPriorPartyLeave(result.PriorLeave, joinerIdentity.character); err != nil {
		result.Reason = "prior_party_projection_failed: " + err.Error()
		return alignedcmd.PartyState{}, result, false
	}
	state := s.publishRuntimePartySnapshot(result.Party)
	if state.PartyID <= 0 {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "party_projection_failed"}, false
	}
	return state, result, true
}

// reconcileManagedPriorPartyLeave projects the old half of a cross-party
// join. Membership has already moved under RuntimePartyManager's lock; this
// function only emits the corresponding client cache, relay and roster
// transition for remaining old-party members. Without it, a successful join
// could leave those survivors looking at a stale team until their next map
// change.
func (s *Service) reconcileManagedPriorPartyLeave(prior *party.RuntimePartyResult, movedUser uint16) error {
	if prior == nil || !prior.OK || prior.Previous == nil || prior.Previous.ID == 0 {
		return nil
	}
	previous := *prior.Previous
	s.logRuntimePartyMembershipRemoved("prior_party_leave_on_join_or_create", movedUser, 0, previous, *prior)
	if prior.Disbanded || prior.Party.ID == 0 {
		s.closeRuntimePartyUDPRelay(int(previous.ID))
		return nil
	}
	nextState := s.publishRuntimePartyMembershipSnapshot(prior.Party)
	if prior.Retired != nil {
		s.closeRuntimePartyUDPRelay(int(previous.ID))
		for _, member := range previous.Members {
			if member.UserID == movedUser {
				continue
			}
			session, online := s.onlineGameSession(member.UserID)
			if !online {
				continue
			}
			if err := s.sendRuntimePartySnapshot(session, alignedcmd.PartyState{}); err != nil {
				return err
			}
		}
		s.syncRuntimePartyUDPRelay(nextState)
		return s.sendManagedRuntimePartySnapshots(nextState)
	}
	if nextState.PartyID <= 0 {
		s.closeRuntimePartyUDPRelay(int(previous.ID))
	} else {
		s.syncRuntimePartyUDPRelay(nextState)
	}
	return s.broadcastRuntimePartyMemberRemoved(previous.StateFor(previous.Leader), nextState, movedUser, nil)
}

// bootstrapManagedRuntimePartyForSession imports the old per-session
// projection exactly once when an already-open client window predates the
// central manager (including channel reconnect tests). From that point on the
// projection is only a client cache; every membership mutation goes through
// RuntimePartyManager. Production joins normally never take this path because
// op12 now creates the central party before its first roster packet is sent.
func (s *Service) bootstrapManagedRuntimePartyForSession(session *gameSession) (party.RuntimePartySnapshot, bool) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return party.RuntimePartySnapshot{}, false
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return party.RuntimePartySnapshot{}, false
	}
	if snapshot, found := manager.SnapshotByUser(identity.character, identity.generation); found {
		return snapshot, true
	}
	state := runtimePartyStateSnapshot(session)
	if state.PartyID <= 0 {
		return party.RuntimePartySnapshot{}, false
	}
	leaderID := state.UserID
	if leaderID == 0 {
		leaderID = identity.character
	}
	leaderIdentity, leaderOnline := s.onlineGameSessionCharacterSnapshot(leaderID)
	if !leaderOnline {
		leaderIdentity = identity
		leaderID = identity.character
	}
	leaderMember, leaderOK := s.runtimePartyMemberForIdentity(leaderIdentity)
	if !leaderOK {
		return party.RuntimePartySnapshot{}, false
	}
	state.UserID = leaderID
	state.IsLeader = true
	created := manager.EnsureLeader(leaderMember, state)
	if !created.OK {
		return party.RuntimePartySnapshot{}, false
	}
	snapshot := created.Party
	for _, cachedMember := range runtimePartyMembers(state) {
		if cachedMember.UserID == 0 || cachedMember.UserID == leaderID {
			continue
		}
		memberIdentity, online := s.onlineGameSessionCharacterSnapshot(cachedMember.UserID)
		if !online {
			continue
		}
		member, memberOK := s.runtimePartyMemberForIdentity(memberIdentity)
		if !memberOK {
			continue
		}
		member.State = cachedMember
		joined := manager.Join(snapshot.ID, member)
		if joined.OK {
			snapshot = joined.Party
		}
	}
	s.publishRuntimePartySnapshot(snapshot)
	return snapshot, true
}

// runtimePartyMembershipRemovedEvent 是"某个角色被摘出运行期队伍"的**唯一审计点**。
//
// 为什么需要它（现场 2026-09-26 HK）：队长选图时 `party_id=0`，而现场按
// `game-party-(leave|removed|...)` grep 到的 leave/移除事件是 0 —— 但摘除本身可能是
// 静默的：`detachRuntimePartySession`（socket 关闭 / 重选角 / 回选角界面）与
// `handleOnlineLeaveParty` 的**成功路径**都不打任何 `game-party-*` 事件，只有收包方
// 那条 op9 移除帧（`game-current-party-frame-op9-remove-send`）。这一条把"谁摘的、
// 摘的是谁、摘之前队伍是什么、这个角色当时还有没有别的活 socket"一次写清。
func (s *Service) logRuntimePartyMembershipRemoved(
	source string,
	characterID uint16,
	generation uint64,
	before party.RuntimePartySnapshot,
	result party.RuntimePartyResult,
) {
	if s == nil || characterID == 0 {
		return
	}
	session, _ := s.onlineGameSession(characterID)
	fields := []any{
		"source", source,
		"char_id", characterID,
		"character_generation", generation,
		"party_id_before", before.ID,
		"leader_before", before.Leader,
		"member_count_before", len(before.Members),
		"party_id_after", result.Party.ID,
		"leader_after", result.Party.Leader,
		"member_count_after", len(result.Party.Members),
		"disbanded", result.Disbanded,
		"leader_moved", result.LeaderMoved,
		"result_reason", result.Reason,
		"live_same_character_conns", s.liveSameCharacterConnIDs(characterID, session),
	}
	if session != nil {
		fields = append(fields,
			"conn_id", session.connID,
			"session_generation", session.characterGeneration)
	}
	s.logPacketEvent("game-party-membership-removed", fields...)
}

func (s *Service) leaveManagedRuntimeParty(identity boundGameSessionCharacter, source string) (alignedcmd.PartyState, party.RuntimePartyResult, bool) {
	if !s.boundGameSessionCharacterCurrent(identity) {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "stale_session"}, false
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "party_manager_unavailable"}, false
	}
	if _, found := manager.SnapshotByUser(identity.character, identity.generation); !found {
		s.bootstrapManagedRuntimePartyForSession(identity.session)
	}
	before, _ := manager.SnapshotByUser(identity.character, 0)
	result := manager.Leave(identity.character, identity.generation)
	if !result.OK {
		return alignedcmd.PartyState{}, result, false
	}
	s.logRuntimePartyMembershipRemoved(source, identity.character, identity.generation, before, result)
	s.clearRuntimePartyProjection(identity.character, identity.generation)
	if result.Disbanded || result.Party.ID == 0 {
		return alignedcmd.PartyState{}, result, true
	}
	return s.publishRuntimePartyMembershipSnapshot(result.Party), result, true
}

func (s *Service) ensureManagedRuntimePartyForSession(session *gameSession) (alignedcmd.PartyState, error) {
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return alignedcmd.PartyState{}, nil
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return alignedcmd.PartyState{}, fmt.Errorf("runtime party manager unavailable")
	}
	state := runtimePartyStateSnapshot(session)
	if state.PartyID <= 0 {
		return state, nil
	}
	if _, memberOK := s.runtimePartyMemberForIdentity(identity); !memberOK {
		return alignedcmd.PartyState{}, nil
	}
	if snapshot, exists := manager.SnapshotByUser(identity.character, identity.generation); exists {
		result := manager.UpdateSettings(identity.character, identity.generation, state)
		if result.OK {
			return s.publishRuntimePartySnapshot(result.Party), nil
		}
		s.publishRuntimePartySnapshot(snapshot)
		return snapshot.StateFor(identity.character), nil
	}
	snapshot, created := s.bootstrapManagedRuntimePartyForSession(session)
	if !created {
		return alignedcmd.PartyState{}, fmt.Errorf("create runtime party")
	}
	return s.publishRuntimePartySnapshot(snapshot), nil
}

func (s *Service) hasManagedRuntimeParty(session *gameSession) bool {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return false
	}
	if snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(identity.character, identity.generation); found {
		return snapshot.ID != 0
	}
	if runtimePartyStateSnapshot(session).PartyID <= 0 {
		return false
	}
	_, found := s.bootstrapManagedRuntimePartyForSession(session)
	return found
}

// replaceManagedRuntimeParty expresses a higher-level grouping (currently a
// raid attack-party assignment) through the same central party graph. It
// intentionally does not reuse the caller's synthetic PartyID: the manager
// allocates a fresh generation just like ordinary create/leader transfer, then
// callers project the returned snapshot to the current EXE.
func (s *Service) replaceManagedRuntimeParty(state alignedcmd.PartyState) (alignedcmd.PartyState, bool) {
	members := runtimePartyMembers(state)
	if len(members) == 0 {
		return alignedcmd.PartyState{}, false
	}
	leaderID := state.UserID
	if leaderID == 0 {
		leaderID = members[0].UserID
	}
	leaderIdentity, online := s.onlineGameSessionCharacterSnapshot(leaderID)
	if !online {
		return alignedcmd.PartyState{}, false
	}
	leader, leaderOK := s.runtimePartyMemberForIdentity(leaderIdentity)
	if !leaderOK {
		return alignedcmd.PartyState{}, false
	}
	for _, cached := range members {
		if cached.UserID == leaderID {
			leader.State = cached
			break
		}
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return alignedcmd.PartyState{}, false
	}
	created := manager.Create(leader, state)
	if !created.OK {
		return alignedcmd.PartyState{}, false
	}
	snapshot := created.Party
	for _, cached := range members {
		if cached.UserID == leaderID {
			continue
		}
		identity, online := s.onlineGameSessionCharacterSnapshot(cached.UserID)
		if !online {
			return alignedcmd.PartyState{}, false
		}
		member, memberOK := s.runtimePartyMemberForIdentity(identity)
		if !memberOK {
			return alignedcmd.PartyState{}, false
		}
		member.State = cached
		joined := manager.Join(snapshot.ID, member)
		if !joined.OK {
			return alignedcmd.PartyState{}, false
		}
		snapshot = joined.Party
	}
	return s.publishRuntimePartySnapshot(snapshot), true
}

func (s *Service) publishAllManagedRuntimeParties() {
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return
	}
	for _, snapshot := range manager.Snapshots() {
		s.publishRuntimePartySnapshot(snapshot)
	}
}

func (s *Service) sendManagedRuntimePartySnapshots(state alignedcmd.PartyState) error {
	if state.PartyID <= 0 || state.PartyID >= int(^uint16(0)) {
		return nil
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByID(uint16(state.PartyID))
	if !found {
		return fmt.Errorf("runtime party %d is no longer current", state.PartyID)
	}
	for _, member := range snapshot.Members {
		identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || identity.generation != member.SessionGeneration {
			continue
		}
		if err := s.sendRuntimePartySnapshot(identity.session, snapshot.StateFor(member.UserID)); err != nil {
			return err
		}
	}
	// 上面这一轮（op153/op11/op9）里 op9 只描述"收包人自己"；队友那条记录在城镇靠
	// `broadcastCurrentPartyActorProjection` 的区域在线表补发，而**副本里没有城镇区域表**
	// （进图即 leaveTownPresenceForDungeon）⇒ 委任队长/成员变动后副本内的队伍框仍是旧数据。
	// 这里对同一场副本里的成员再补一轮完整队伍帧（op153 + 每人一条 op9）。
	s.sendDungeonPartyFrameRoster(state, "runtime_party_snapshot_dungeon_roster")
	return nil
}

// sendChangeHostRuntimePartySnapshots publishes the committed host projection
// without replaying the formation endpoint handshake or an old-generation
// clear. The client already owns the peer transport; CHANGE_HOST changes which
// fixed slot carries the crown. One authoritative realtime table plus its
// scene roster contains every required HP, slot and leader value.
func (s *Service) sendChangeHostRuntimePartySnapshots(state alignedcmd.PartyState) error {
	if state.PartyID <= 0 || state.PartyID >= int(^uint16(0)) {
		return nil
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByID(uint16(state.PartyID))
	if !found {
		return fmt.Errorf("runtime party %d is no longer current", state.PartyID)
	}
	for _, member := range snapshot.Members {
		identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || identity.generation != member.SessionGeneration {
			continue
		}
		if err := s.sendRuntimePartyRosterRefresh(
			identity.session,
			snapshot.StateFor(member.UserID),
			"managed_runtime_party_change_host_roster",
		); err != nil {
			return err
		}
	}
	// Dungeon members have no town-area table. Keep the existing complete
	// dungeon projection (op153 followed by every member's op9 record).
	s.sendDungeonPartyFrameRoster(state, "managed_runtime_party_change_host_dungeon_roster")
	return nil
}
