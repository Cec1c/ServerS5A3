package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 本文件实现"**队长退出副本（回城）时，队伍必须解散，仍留在副本里的队员继续留在本场实例内**"。
//
// # 操作者报障（原文）
//
//	「队长退出副本 队伍没有解散。正常情况下队伍应该解散且队员还留在图内。
//	  现在的情况下队员看队长定住不动，实际上队长已经返回城镇了。」
//
// # 今天的实际路径（改前，逐行可查）
//
// 队长在副本里发起回城（op42 放弃 / op132 系统菜单回城 / 通关 op35）后，服务端只做三件事：
//
//	dungeon_return_town.go:707-716 commitPendingDungeonReturnForSceneRequest
//	  → session.dungeon.runtime = nil                    ← 只摘掉**自己**的副本运行时
//	  → s.releasePartyDungeonInstance(runtime, charID)   ← 只把自己从队伍副本实例摘掉
//	     dungeon_party_instance.go:870-912（member_count-1，剩 0 人才回收实例）
//
// **队伍（RuntimePartyManager）一个字段都不动、一个包都不发**：
//   - `game-dungeon-back-to-village-committed` 之后没有任何 op9 移除帧、没有 op153 空名单、
//     没有 `game-party-membership-removed`；
//   - 留图队员的客户端既收不到"队长离队"，也收不到"队长 actor 从场景里移除"的 op9 kind3，
//     于是把 op2 mode0 建出来的队长 actor 一直留在渲染区（**定住不动**），而队长本人已经在城镇。
//
// 现场逐行证据（packet_log.txt，2026-09-28，队伍 1＝队长角色 3 + 队员角色 6，副本 310 房间 3:0）：
//
//	:96603 EVENT kind=game-dungeon-party-instance-member-left instance_id=1 party_id=1
//	       dungeon_id=310 char_id=3 member_count=1 source=back_to_village_committed
//	       t=2026-09-28T13:39:01.720288894Z
//	  ⇒ 队长（3）离开实例，队友（6）**仍在实例内**（member_count=1，实例未回收）
//	:96613-96665 队友连接 game-000025 在 13:39:01.72→13:39:09.61 之间只收到**自己的**技能/统计包
//	  ⇒ 零条队伍帧、零条 peer 移除
//	:96855 EVENT kind=game-party-membership-removed source=client_leave_party_op13 char_id=3
//	       party_id_before=1 leader_before=3 member_count_before=2 disbanded=false
//	       t=2026-09-28T13:39:48.248324582Z
//	  ⇒ 47 秒后队长在**城镇**里点"退出队伍"时，队伍里仍然是 2 人：这段期间队伍从未解散。
//
// # 权威口径（厂商只读引用，见报告 §A）
//
// `Temp\repo-10675-final-20260926`（git 7bdfc30，规范 + 自测齐全的那一份）在
// `PartyManager.cs:597-638 LeaveForDungeonReturn` + `:667-705 LeaveLocked` 里给的是
// **另一条规则**：队长主动回城时**离开队伍**、由最低已占槽位在原 PartyId 内接任，
// 队伍**不**解散（`Disbanded` 只在 `party.IsEmpty` 时出现），幸存者保留自己的
// `DungeonRun` 与共享 `DungeonInstance`（`DungeonEntryHandler.cs:4143-4150`）。
// 规范原文：`Docs\副本架构业务接入规范.md:719`。
//
// 操作者给出的验收口径是 **解散**（"正常情况下队伍应该解散且队员还留在图内"），
// 本文件按**操作者口径**实现：解散 + 队员留在本场实例内。两者不一致之处已在报告里写明。
//
// # 本开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_LEADER_EXIT`
//	  - `disband`（默认，零值/空/未设置/非法值）= 修好后的行为：
//	    队长回城提交时，若同一队伍副本实例里**还有别的成员在场**，则解散队伍 + 把关照口径的
//	    "peer 已不在"信号发给每个留图队员（队长 actor 的 op9 kind3 移除 + 空名册帧）；
//	  - `legacy` = 逐字节回到改动前：不碰管理器、不发包、也不记本文件的新事件
//	    （今天那条 `game-dungeon-back-to-village-committed` 序列完全不变）。
const dungeonPartyLeaderExitEnv = "DNFBRIDGE_PARTY_DUNGEON_LEADER_EXIT"

const (
	dungeonPartyLeaderExitValueDisband = "disband"
	dungeonPartyLeaderExitValueLegacy  = "legacy"
)

// dungeonPartyLeaderExitMode 是 dungeonPartyLeaderExitEnv 的归一化取值。
type dungeonPartyLeaderExitMode int

const (
	// dungeonPartyLeaderExitDisband 是默认档（零值）= 修好后的行为。
	dungeonPartyLeaderExitDisband dungeonPartyLeaderExitMode = iota
	// dungeonPartyLeaderExitLegacy 是改动前行为，一键回退档。
	dungeonPartyLeaderExitLegacy
)

func (mode dungeonPartyLeaderExitMode) String() string {
	if mode == dungeonPartyLeaderExitLegacy {
		return dungeonPartyLeaderExitValueLegacy
	}
	return dungeonPartyLeaderExitValueDisband
}

// readDungeonPartyLeaderExitMode 解析 DNFBRIDGE_PARTY_DUNGEON_LEADER_EXIT。
// **默认 `disband`（= 修好后的行为）**：空/未设置/非法值都是 disband；
// 只有显式 `legacy` 回到改动前那条"只摘自己、队伍原封不动"的路径。
func readDungeonPartyLeaderExitMode(value string) dungeonPartyLeaderExitMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dungeonPartyLeaderExitValueLegacy:
		return dungeonPartyLeaderExitLegacy
	default:
		return dungeonPartyLeaderExitDisband
	}
}

// dungeonPartyLeaderExitDisbandMode 自读开关（不改 options.go）。
func (s *Service) dungeonPartyLeaderExitDisbandMode() dungeonPartyLeaderExitMode {
	return readDungeonPartyLeaderExitMode(os.Getenv(dungeonPartyLeaderExitEnv))
}

// dungeonPartyInstanceMemberSessions 只读地取"这个队伍副本实例里除 excluding 之外的成员会话"。
// 只读实例注册表（instance.mu），**不碰队友的 session.dungeon.mu**：与
// `partyDungeonInstancePeers` 同一条既有取舍（两会话互等会话锁会死锁）。
func dungeonPartyInstanceMemberSessions(instance *dungeonPartyInstance, excluding uint16) []partyDungeonPeer {
	if instance == nil {
		return nil
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	peers := make([]partyDungeonPeer, 0, len(instance.members))
	for characterID := range instance.members {
		if characterID == 0 || characterID == excluding {
			continue
		}
		session := instance.sessions[characterID]
		if session == nil {
			continue
		}
		peers = append(peers, partyDungeonPeer{
			CharacterID: characterID,
			Session:     session,
			Scene:       instance.scenes[characterID],
		})
	}
	return peers
}

// disbandRuntimePartyOnDungeonLeaderExit 在队长"退出副本/回城"提交之后调用。
//
// 触发条件（全部满足才动手，否则与改动前逐字节相同——只返回 0，不发包、不改状态）：
//  1. 开关档位是默认 `disband`；
//  2. 本会话的权威队伍名单 ≥ 2 人，且名单里的队长**就是**这次离场的角色
//     （队员自己回城不触发，操作者口径只要求队长离场时解散）；
//  3. 同一队伍副本实例里**还有别的成员在场**（`instance.members` 里除离场者之外还有人，
//     且它在实例里登记的会话可用）——这正是"队员还留在图内"的可证状态。
//
// 动作（顺序即线上判读顺序）：
//  1. 把名单里每一个成员都从中央管理器摘掉（最后一个自然返回 `Disbanded=true`）⇒ 队伍解散；
//  2. 清掉每个在线成员的客户端队伍投影，并关掉这支队伍的 P2P 中继；
//  3. 对**每个**在线成员发一条空名册帧（`sendRuntimePartyMemberRemoved`，与既有
//     `runtime_party_member_removed` 逐字节同族）；
//  4. 对**仍留在本场副本实例内**的成员，额外先发一条 op9 kind3「把队长 actor 从场景移除」
//     （`buildCurrentSceneOp9ActorRemovalBodyInContext`，副本上下文 owner=0）——
//     这就是"队员不再看到定住的队长"的那一条下行；缺了它，客户端会一直留着 op2 mode0
//     建出来的队长 actor。
//
// **绝不触碰留图队员的 `session.dungeon.runtime` 与 `runtime.partyInstance`**：队员必须留在
// 本场副本里继续打（操作者口径）。因此这里也**不**调用 `recyclePartyDungeonInstances`
// （那会把队员正在用的实例登记摘掉）；实例会在队员自己出图时按既有收口点回收。
func (s *Service) disbandRuntimePartyOnDungeonLeaderExit(
	session *gameSession,
	instance *dungeonPartyInstance,
	leavingCharacterID uint16,
	source string,
) int {
	if s == nil || session == nil || leavingCharacterID == 0 {
		return 0
	}
	if s.dungeonPartyLeaderExitDisbandMode() == dungeonPartyLeaderExitLegacy {
		// 一键回退档：逐字节回到改动前——不碰管理器、不发包、不记新事件。
		return 0
	}
	// 权威名单按**角色**查中央管理器（会话本地缓存可能落后，见 party_manager_runtime.go）。
	state := s.runtimePartyStateForSession(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		return 0
	}
	leaderID := state.UserID
	if leaderID == 0 || leaderID != leavingCharacterID {
		return 0
	}
	inDungeon := dungeonPartyInstanceMemberSessions(instance, leavingCharacterID)
	if len(inDungeon) == 0 {
		// 没有队友还留在本场实例里（例如全队一起回城、或实例已被回收）：
		// 不改变今天的任何行为。
		return 0
	}

	// 1. 解散：每个成员都摘掉。**先摘非队长、最后摘队长**：管理器里"队长离队且队里还有人"
	// 会走"退役这一代 + 用新 id 重建"的分支（runtime_manager.go:271-306），那会白白多出
	// 一代队伍；先摘掉其他人，队长最后离队时 `party.count()==0`，直接返回 `Disbanded=true`。
	manager := s.runtimePartyManagerForService()
	removed := make([]uint16, 0, len(members))
	disbanded := false
	if manager != nil {
		leaveOrder := make([]uint16, 0, len(members))
		for _, member := range members {
			if member.UserID == 0 || member.UserID == leaderID {
				continue
			}
			leaveOrder = append(leaveOrder, member.UserID)
		}
		leaveOrder = append(leaveOrder, leaderID)
		for _, characterID := range leaveOrder {
			result := manager.LeaveCurrent(characterID)
			if !result.OK {
				continue
			}
			removed = append(removed, characterID)
			if result.Disbanded {
				disbanded = true
			}
		}
	}

	// 2. 清投影 + 关中继（队伍已经不存在了）。
	online := make([]*gameSession, 0, len(members))
	for _, member := range members {
		identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok {
			continue
		}
		s.clearRuntimePartyProjection(member.UserID, identity.generation)
		online = append(online, identity.session)
	}
	s.closeRuntimePartyUDPRelay(state.PartyID)

	// 3./4. 逐个成员发"队伍没了"；留图队员额外先摘掉队长 actor。
	leaderObjectKey := currentSceneActorObjectKey(leaderID)
	peerActorRemovalSent := make([]uint16, 0, len(inDungeon))
	rosterClearedSent := make([]uint16, 0, len(online))
	remainingInDungeon := make([]uint16, 0, len(inDungeon))
	for _, peer := range inDungeon {
		if peer.Session == nil || peer.CharacterID == 0 {
			continue
		}
		remainingInDungeon = append(remainingInDungeon, peer.CharacterID)
		removal := buildCurrentSceneOp9ActorRemovalBodyInContext(
			leaderObjectKey,
			currentSceneObjectContext,
			s.currentSceneActorRoute(),
		)
		if err := s.sendGameUpperRawClass(
			peer.Session,
			uint16(dnfenum.CmdPacketRecoverStamina),
			removal,
			0,
		); err != nil {
			s.logGameEvent(peer.Session, "game-party-dungeon-leader-exit-peer-actor-removal-failed",
				"source", source,
				"party_id", state.PartyID,
				"instance_id", instanceIDOfPartyDungeonInstance(instance),
				"char_id", peer.CharacterID,
				"leaving_char_id", leavingCharacterID,
				"object_key", leaderObjectKey,
				"error", err)
			continue
		}
		peerActorRemovalSent = append(peerActorRemovalSent, peer.CharacterID)
		s.logGameEvent(peer.Session, "game-party-dungeon-leader-exit-peer-actor-removal",
			"source", source,
			"party_id", state.PartyID,
			"instance_id", instanceIDOfPartyDungeonInstance(instance),
			"char_id", peer.CharacterID,
			"leaving_char_id", leavingCharacterID,
			"object_key", leaderObjectKey,
			"owner_channel", currentSceneObjectContext,
			"msg_id", uint16(dnfenum.CmdPacketRecoverStamina),
			"classification", 0,
			"body_len", len(removal),
			"still_in_dungeon", true,
			"reason", "leader_left_the_dungeon_member_keeps_its_instance_so_the_frozen_peer_actor_must_be_removed")
	}
	for _, memberSession := range online {
		if memberSession == nil {
			continue
		}
		peerRemovalAlreadySent := false
		for _, characterID := range peerActorRemovalSent {
			if characterID == selectedCharacterID(memberSession) {
				peerRemovalAlreadySent = true
				break
			}
		}
		if err := s.clearDisbandedDungeonPartyRecords(memberSession, members, leaderID, peerRemovalAlreadySent, source); err != nil {
			s.logGameEvent(memberSession, "game-party-dungeon-retired-record-clear-failed", "source", source, "party_id", state.PartyID, "error", err)
			continue
		}
		if err := s.sendRuntimePartyMemberRemoved(memberSession, leaderID, alignedcmd.PartyState{}); err != nil {
			s.logGameEvent(memberSession, "game-party-dungeon-leader-exit-roster-clear-failed",
				"source", source,
				"party_id", state.PartyID,
				"char_id", memberSession.selectedCharacterID,
				"leaving_char_id", leavingCharacterID,
				"error", err)
			continue
		}
		rosterClearedSent = append(rosterClearedSent, memberSession.selectedCharacterID)
		// The local kind3 roster must consume its previous manager slots
		// before NOTI6 removes the departed leader from the active map.
		if peerRemovalAlreadySent {
			if err := s.sendDungeonDepartedActorRemoval(memberSession, leaderID, source); err != nil {
				s.logGameEvent(memberSession, "game-dungeon-departed-active-actor-remove-failed", "source", source, "departing_char_id", leaderID, "error", err)
			}
		}
	}

	s.logGameEvent(session, "game-party-dungeon-leader-exit-disband",
		"source", source,
		"party_id", state.PartyID,
		"instance_id", instanceIDOfPartyDungeonInstance(instance),
		"leader_char_id", leaderID,
		"leaving_char_id", leavingCharacterID,
		"member_count_before", len(members),
		"removed_char_ids", removed,
		"disbanded", disbanded,
		"members_still_in_dungeon", remainingInDungeon,
		"peer_actor_removal_sent_to", peerActorRemovalSent,
		"roster_cleared_sent_to", rosterClearedSent,
		"leader_object_key", leaderObjectKey,
		"member_instance_retained", true,
		"switch", dungeonPartyLeaderExitEnv,
		"mode", s.dungeonPartyLeaderExitDisbandMode().String(),
		"reason", "party_disbands_when_the_leader_leaves_the_dungeon_and_the_member_stays_in_its_own_instance")
	// 队伍已经不存在 ⇒ 每个成员那条"我在哪一场副本"的运行键没有归属。不清掉的话，
	// 下一次在城镇里重新组队会被同一条键误判成"还在上一场副本里"：新队伍在城镇里收到
	// owner=0 的副本上下文队伍帧（op153 + owner_mode=dungeon_context 的 op9），随后被带进
	// 进图选择器流程。取证与开关见 dungeon_party_run_key_town_reset.go 的文件头。
	// 开关为回退档时这一整段零动作、事件字段也不追加（逐字节回到改动前）。
	if dungeonPartyRunKeyTownResetEnabled() {
		memberIDs := make([]uint16, 0, len(members))
		for _, member := range members {
			if member.UserID != 0 {
				memberIDs = append(memberIDs, member.UserID)
			}
		}
		if cleared := s.forgetRuntimePartyRunKeysOnDisband(memberIDs, source); len(cleared) > 0 {
			s.logGameEvent(session, "game-party-dungeon-leader-exit-run-keys-cleared",
				"source", source,
				"party_id", state.PartyID,
				"leader_char_id", leaderID,
				"cleared_char_ids", cleared,
				"reason", "the_disbanded_party_owned_the_stale_dungeon_run_key",
				"switch", dungeonPartyRunKeyTownResetEnv,
				"mode", dungeonPartyRunKeyTownResetMode())
		}
	}
	return len(peerActorRemovalSent)
}

func instanceIDOfPartyDungeonInstance(instance *dungeonPartyInstance) uint64 {
	if instance == nil {
		return 0
	}
	return instance.id
}
