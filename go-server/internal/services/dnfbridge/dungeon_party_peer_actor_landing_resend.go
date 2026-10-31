package dnfbridge

import (
	"os"
	"strings"
)

// 本文件是操作者追加报障「**重新组队第二次进图 时装丢失**」的剩余一半。
//
// # 先回答"上一轮的开关到底 fire 了没"：**fire 了，而且是每一次进图都发**
//
// 现场（packet_log.txt，2026-09-28，操作者这一次的新会话；`conn game-000027` = 队长 char1，
// `conn game-000031/000037` = 队员 char6）：
//
//	178728/178730:EVENT kind=game-party-dungeon-peer-projection-generation-reset
//	    source=back_to_village_committed_peer_projection_generation
//	    char_id=1/6 dropped_projected_peers=1 dropped_resend_keys=1
//	    switch=DNFBRIDGE_PARTY_DUNGEON_PEER_PROJECTION_GENERATION mode=on     ← 上一轮的开关确实 fire
//	178729/178731:EVENT kind=game-party-dungeon-run-key-cleared ... source=back_to_village_committed
//
// 第一次进图（party 1，17:17:16.59）与**解散后重新组队**的第二次进图（party 2，17:21:56.06）
// 都发了完整的三条外观载体：
//
//	177373:EVENT kind=game-party-peer-actor-appearance conn_id=game-000027
//	    source=party_dungeon_shared_loading_barrier_party_peer_copresence_self_actor_to_peer
//	    receiver_char_id=1 peer_char_id=6 appearance_rows=15 appearance_link_rows=8
//	    appearance_subtype0_sent=true appearance_subtype0_rows=13 appearance_subtype0_link_rows=8
//	180192:同型（party 2 的第二次进图），t=2026-09-28T17:21:56.059990514Z
//	177379/180198:SEND kind=game-upper conn_id=game-000027 msg_id=2 plain_body_len=425 ...   ← subtype0 载体（test78）
//
// 客户端侧也逐条对上了（`90CN_trace.log`，`pid=40964` = 队长 char1）：
//
//	[01:17:18.232] local-actor-create phase=after object_key=6 result=598CD600 ...   ← 第一次进图建出队友 actor
//	[01:21:57.864] local-actor-create phase=after object_key=6 result=69DA8100 ...   ← 第二次进图也建出来了
//	[01:21:57.917] combat-power equipment snapshot mode=1 key=6 rows=31 ...
//
// ⇒ **"投影被跳过"这条已经不成立了**（上一轮的修复是有效的）。剩下的差别只在"投影落在
// 收包方场景重建的哪一侧"，而这正是本文件要补的那一刀。
//
// # 剩下的真因：**先落位的那一方永远拿不到补发**
//
// 队友 actor 的第一次投影发生在"后落位一方"的 op29 commit 里，此时**先落位一方**的客户端
// 往往还在重建场景；同文件口径的既有取证（dungeon_party_peer_actor_resend.go 文件头）写明：
//
//	04:05:13.166 local-actor-create phase=after object_key=3 result=58A0E080
//	04:05:13.207 town co-presence actor context removed key=3      ← 41ms 后就被客户端删掉
//
// 为此已有 `resendDungeonPartyPeerActorsAfterFinishLoading`（收包方自己的 finish-loading
// 之后补一次）。但它对**先落位**的一方**永远不触发**，因为那份代码有两条硬条件：
//
//	dungeon_party_peer_actor_resend.go:53   runKey, ready := s.currentDungeonPartyRunKey(session)   ← 自己要有键 ✓
//	dungeon_party_peer_actor_resend.go:71   peerKey, peerReady := ...; if !peerReady || peerKey != runKey { continue }
//	                                          ↑ 先落位一方做 op37 时，**对端还没有运行键** ⇒ 一个都不补
//
// 而且名额在循环**之前**就被占掉（`:59 claimDungeonPeerActorResend`），`resent` 为空时也不退还
// ⇒ 这名收包方在本房间**再也不会**被补发。现场全日志的指纹：
//
//	$ grep -c 'kind=game-dungeon-party-peer-actor-resend .*char_id=1 ' packet_log.txt   → 0
//	$ grep -c 'kind=game-dungeon-party-peer-actor-resend .*char_id=6 ' packet_log.txt   → 11
//	（11 条全是"后落位的那一方"，队长一侧一条都没有）
//
// 于是"先落位的一方"（现场 = 队长 char1，也就是要看队员克隆时装的那一方）拿到的是
// **落在场景重建窗口里的那一份**，客户端随后把它丢掉；重新组队以后落位先后与节奏改变，
// 这个窗口就更容易被撞上 —— 与操作者「重新组队第二次进图 时装丢失」吻合。
//
// # 修法（两条，同一个开关）
//
//  1. **名额诚实**：`resendDungeonPartyPeerActorsAfterFinishLoading` 在没有真正补发时
//     （对端还没落位）**退还名额**，而不是把它烧掉；这样"后落位一方落地后"仍有名额可用。
//  2. **后落位一方推一把**：`projectDungeonPartyPeerActors` 两个方向画完之后，对
//     **场景已经稳定的队友**（finish-loading 收尾三面旗都在：`currentFinishLoadingStateSent`
//     + `currentFinishLoadingCompletionSent` + `postFinishLoadingPlayerStateSent`）把
//     **自己**再推一次（op2 mode0/mode1 + subtype0 + op9，与既有补发同一组帧），
//     并记一条可判读事件。
//
// 对端还在重建场景时**不动手**（`decision=deferred`）：那个窗口归对端自己的 finish-loading 补发。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_PEER_ACTOR_LANDING_RESEND`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：退还空名额 + 后落位一方对已稳定队友补推一次；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（不退还名额、不补推、
//	    不记本文件的事件）。
//
// 与同包既有开关一致的取值方式：每次调用读一次 env（不改 options.go）。
const dungeonPartyPeerActorLandingResendEnv = "DNFBRIDGE_PARTY_DUNGEON_PEER_ACTOR_LANDING_RESEND"

func dungeonPartyPeerActorLandingResendEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyPeerActorLandingResendEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

func dungeonPartyPeerActorLandingResendMode() string {
	if dungeonPartyPeerActorLandingResendEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// peerSceneStableForPeerActorLandingResend 报告这名会话的场景重建**已经收尾**
// （finish-loading 的三面旗都在）：只有到这时再推一份队友 actor 才不会被客户端随后的
// 场景重建丢掉。三面旗与 `scene_finish_loading.go:32-51/169-195` 同一组。
func peerSceneStableForPeerActorLandingResend(session *gameSession) bool {
	if session == nil {
		return false
	}
	gates := session.finishLoadingGateSnapshot()
	return gates.State && gates.Completion && gates.PostFinish
}

// resendDungeonPartyPeerActorToAlreadyLandedMembers 由**刚落位**的那一方调用：
// 对场景已经稳定的队友把"自己"再投影一次。返回真正补推成功的队友数。
//
// 调用点：`projectDungeonPartyPeerActors` 两个方向画完之后（同一条既有调用链，
// 不新增锁序：只读各自的 `dungeonPartyRunMu` 与实例注册表，不碰任何会话的 dungeon.mu）。
func (s *Service) resendDungeonPartyPeerActorToAlreadyLandedMembers(session *gameSession, source string) int {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return 0
	}
	if !dungeonPartyPeerActorLandingResendEnabled() {
		return 0
	}
	identity, current := s.boundGameSessionCharacterSnapshot(session)
	if !current {
		return 0
	}
	// 判据只用"本会话有一条**有效**的副本运行键"（`recordDungeonPartyRunKey` 的唯一写入点
	// 就在副本落位处，回城/解散都会被 `dungeon_party_run_key_town_reset.go` 作废），
	// 再叠加"对端与我是同一条键" + "对端 finish-loading 收尾三面旗"。这三条比
	// "本会话有没有 runtime 指针"更贴这件事：帧是发给**对端**的，而对端的场景状态才是
	// 决定这份投影会不会被丢掉的那一项。
	ownKey, ready := s.currentDungeonPartyRunKey(session)
	if !ready {
		return 0
	}
	state := runtimePartyStateSnapshot(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		return 0
	}
	sent := 0
	for _, member := range members {
		if member.UserID == 0 || member.UserID == session.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil || peer == session {
			continue
		}
		peerKey, peerReady := s.currentDungeonPartyRunKey(peer)
		peerGates := peer.finishLoadingGateSnapshot()
		peerSceneStable := peerGates.State && peerGates.Completion && peerGates.PostFinish
		decision, reason := "resent",
			"the_already_landed_member_finished_its_scene_rebuild_so_the_projection_is_pushed_again_after_it"
		switch {
		case !peerReady || peerKey != ownKey:
			decision, reason = "skip", "peer_does_not_share_this_dungeon_run_key"
		case !peerSceneStable:
			decision, reason = "deferred",
				"peer_scene_rebuild_is_still_running_its_own_finish_loading_resend_owns_that_window"
		case !s.claimDungeonPeerActorResend(peer, ownKey, identity):
			decision, reason = "skip",
				"the_peer_already_consumed_its_one_resend_for_this_room_and_run"
		}
		delivered := false
		if decision == "resent" {
			if err := s.sendDungeonPartyPeerActorState(
				peer,
				session,
				source+"_peer_actor_landing_resend",
			); err != nil {
				s.releaseDungeonPeerActorResend(peer, ownKey, identity)
				decision, reason, delivered = "failed", "peer_actor_landing_resend_send_failed", false
				s.logGameEvent(session, "game-party-dungeon-peer-actor-landing-resend-failed",
					"source", source,
					"char_id", peer.selectedCharacterID,
					"peer_char_id", session.selectedCharacterID,
					"error", err)
			} else {
				delivered = true
				sent++
			}
		}
		// 这一行是"下一次实测一眼可判"的那一条：点名**这一场副本的运行键（世代）**、
		// 收包方（先落位、场景已稳定的一方）、被重新投影的 actor（刚落位的一方）、决策与档位。
		// 这一次补推实际发出的外观行数/链接行数/subtype0 由**同 source** 的
		// `game-party-peer-actor-appearance` 行给出（`appearance_event_source` 就是它）。
		s.logGameEvent(session, "game-party-dungeon-peer-actor-landing-resend",
			"source", source,
			"char_id", peer.selectedCharacterID,
			"peer_char_id", session.selectedCharacterID,
			"conn_id", peer.connID,
			"run_key_party_id", ownKey.PartyID,
			"run_key_dungeon_id", ownKey.DungeonID,
			"run_key_maze_index", ownKey.MazeIndex,
			"run_key_room", dungeonRoomCoordinate(ownKey.RoomX, ownKey.RoomY),
			"run_key_map_id", ownKey.MapID,
			"run_key_seed", ownKey.Seed,
			"peer_scene_stable", peerSceneStable,
			"peer_finish_state_sent", peerGates.State,
			"peer_finish_completion_sent", peerGates.Completion,
			"peer_post_finish_sent", peerGates.PostFinish,
			"peer_run_key_ready", peerReady,
			"peer_run_key_party_id", peerKey.PartyID,
			"peer_run_key_map_id", peerKey.MapID,
			"peer_run_key_seed", peerKey.Seed,
			"peer_finish_loading_tail", peerGates.State && peerGates.Completion && peerGates.PostFinish,
			"decision", decision,
			"delivered", delivered,
			"reason", reason,
			"appearance_event_source", source+"_peer_actor_landing_resend",
			"switch", dungeonPartyPeerActorLandingResendEnv,
			"mode", dungeonPartyPeerActorLandingResendMode())
	}
	return sent
}
