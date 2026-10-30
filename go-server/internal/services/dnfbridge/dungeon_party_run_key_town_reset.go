package dnfbridge

import (
	"os"
	"strings"
)

// 本文件处理"**解散/出图之后，会话里那条副本运行键没有失效**"导致的重新组队异常。
//
// # 操作者报障（原文）
//
//	「4 队长退队后队员视角正常，但是重新组队后队伍出现异常」
//
// # 现场（packet_log.txt，2026-09-28T14:30:10Z 队长 3 回城解散队伍 1，14:30:38Z 两人在城镇里重新组队 2）
//
//	:109132 EVENT kind=game-party-dungeon-leader-exit-disband conn_id=game-000008
//	        source=completed_town_route party_id=1 instance_id=1 leader_char_id=3 leaving_char_id=3
//	        member_count_before=2 removed_char_ids=[6 3] disbanded=true members_still_in_dungeon=[6] ...
//	:109259 EVENT kind=game-town-set-user-area-committed conn_id=game-000011 channel_id=10 char_id=6
//	        town_id=31 area_id=2 ... map_path=Town/CentralPark/CentralPark_Waiting.map
//	:109352 EVENT kind=game-party-response-peer-received conn_id=game-000011 type=11
//	        source_char_id=6 target_id=3 mode=0 value=0          ← 两人在**城镇**里重新组队（party 2）
//	:109390 EVENT kind=game-current-party-frame-op9-send conn_id=game-000008
//	        source=runtime_party_snapshot_dungeon_roster_dungeon_roster receiver_char_id=3 char_id=3
//	        owner_channel=0 local_channel=10 member_identity_block=true owner_mode=dungeon_context
//	        party_id=2 ...
//	:109409 EVENT kind=game-dungeon-party-frame-roster-send conn_id=game-000008 channel_id=10
//	        source=runtime_party_snapshot_dungeon_roster party_id=2 member_count=2 dungeon_member_count=2
//	        record_count=4 run_key_room=0:0 run_key_dungeon_id=310 run_key_maze_index=0 ...
//	        t=2026-09-28T14:30:38.900222958Z
//
// 即：**新队伍在城镇里被当成"正在副本 310 里"**，每个人都收到 op9 的
// `owner_mode=dungeon_context`（owner=0）队伍帧与 op153 实时表，run key 还是上一场
// 副本的 `0:0 / 310`。紧接着 14:30:41 这条假的"副本上下文"把客户端带进了进图选择器流程
// （`upper_15_party_members_passive_party_area_detach` 的 op23 `area=0xff` 脱离 +
// `owner_channel=0` 的 mode0/mode1 + op3/op5/op27），这就是操作者看到的"队伍出现异常"。
//
// # 状态残留点（file:line）
//
//	recordDungeonPartyRunKey         dungeon_party_copresence.go:76   ← 唯一写入点
//	  调用点只有一个：scene_post_start_map.go:578（**副本落位**时）
//	currentDungeonPartyRunKey        dungeon_party_copresence.go:236  ← 只读，从不校验场景
//	sendDungeonPartyFrameRoster      party_online.go:749              ← 只要求"键 ready"，不看是不是在副本里
//
// `session.dungeonPartyRunValid` 只在"下一次副本落位"时会被重算；解散队伍、回城、
// 出图三条路都不碰它，而 `dungeonPartyRunKeyFrom`（:55）要求当时的队伍 ≥2 人才返回 true
// —— 回到城镇以后再也不会有副本落位，于是这条键**永远**停在解散前那一场。
// 新队伍形成时 `sendManagedRuntimePartySnapshots`（party_manager_runtime.go:593）读到
// 这条过期键 ⇒ 城镇里发出副本上下文队伍帧。
//
// # 修法
//
// 只在**城镇上下文**的那一个消费点收口：`sendDungeonPartyFrameRoster` 读运行键之前，
// 先问一句"这个会话现在真的有活跃副本运行时吗"。没有 ⇒ 键已失效，就地清掉
// （`dungeonPartyRunValid=false` + 清零），并记一条点名 actor/队伍/被清掉的键的事件。
// 有活跃副本运行时（真的在副本里）⇒ 一个字节都不动。
//
// 另外：队长从副本里退出导致**队伍解散**时（dungeon_party_leader_exit.go），队伍已经
// 不存在了，每个成员的运行键都没有归属 ⇒ 同一开关下顺手清掉（并记进解散事件）。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_RUN_KEY_TOWN_RESET`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：城镇上下文里失效的副本运行键会被清掉；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（不清键、不改事件字段、
//	    不发包、不多一行日志）。
//
// 与同包既有开关一致的取值方式：每次调用读一次 env（不改 options.go）。
const dungeonPartyRunKeyTownResetEnv = "DNFBRIDGE_PARTY_DUNGEON_RUN_KEY_TOWN_RESET"

func dungeonPartyRunKeyTownResetEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyRunKeyTownResetEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

// dungeonPartyRunKeyTownResetMode 是判读事件/解散事件里的档位串。
func dungeonPartyRunKeyTownResetMode() string {
	if dungeonPartyRunKeyTownResetEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// forgetDungeonPartyRunKey 无条件清掉一条会话的副本运行键（返回被清掉的那条键，ok=false
// 表示本来就没有有效键）。调用方必须已经确定"这条键不该再有效"。
func forgetDungeonPartyRunKey(session *gameSession) (dungeonPartyRunKey, bool) {
	if session == nil {
		return dungeonPartyRunKey{}, false
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if !session.dungeonPartyRunValid {
		return dungeonPartyRunKey{}, false
	}
	stale := session.dungeonPartyRun
	session.dungeonPartyRun = dungeonPartyRunKey{}
	session.dungeonPartyRunValid = false
	return stale, true
}

// forgetStaleDungeonPartyRunKeyOutsideDungeon 是"城镇上下文"的收口点：只有会话**没有**
// 活跃副本运行时，这条运行键才算失效。真的在副本里 ⇒ 原样保留（返回 false）。
//
// 清理会记一条 `game-party-dungeon-run-key-cleared`，点名 actor、队伍、被清掉的键，
// 让下一次线上复测只看 packet_log.txt 就能判定"新队伍有没有继承上一场的副本上下文"。
func (s *Service) forgetStaleDungeonPartyRunKeyOutsideDungeon(session *gameSession, source string) bool {
	if !dungeonPartyRunKeyTownResetEnabled() || s == nil || session == nil {
		return false
	}
	if s.sessionHasActiveDungeonRuntime(session) {
		return false
	}
	stale, ok := forgetDungeonPartyRunKey(session)
	if !ok {
		return false
	}
	// 键作废 = 本会话的队友投影世代一起结束，否则下一次进图会命中
	// `claimDungeonPeerProjection` 的短路、整场不再投影队友 actor（时装载体随之丢失）。
	// 见 dungeon_party_peer_projection_generation.go。
	s.resetDungeonPeerProjectionGeneration(session, source+"_peer_projection_generation")
	s.logGameEvent(session, "game-party-dungeon-run-key-cleared",
		"source", source,
		"char_id", session.selectedCharacterID,
		"conn_id", session.connID,
		"stale_party_id", stale.PartyID,
		"stale_dungeon_id", stale.DungeonID,
		"stale_maze_index", stale.MazeIndex,
		"stale_room", dungeonRoomCoordinate(stale.RoomX, stale.RoomY),
		"stale_map_id", stale.MapID,
		"reason", "session_has_no_active_dungeon_runtime_so_the_recorded_run_key_belongs_to_a_disbanded_run",
		"switch", dungeonPartyRunKeyTownResetEnv,
		"mode", dungeonPartyRunKeyTownResetMode())
	return true
}

// forgetRuntimePartyRunKeysOnDisband 在"队伍解散"之后把每个会话的副本运行键一起清掉：
// 队伍已经不存在，那条键没有归属，留着就会在**下一次组队**时把新队伍误判成"还在上一场副本里"。
// 返回被清掉键的角色 id（用于解散事件的判读字段）。开关为回退档时零动作。
func (s *Service) forgetRuntimePartyRunKeysOnDisband(memberIDs []uint16, retiredPartyID uint16, source string) []uint16 {
	if s == nil || !dungeonPartyRunKeyTownResetEnabled() || len(memberIDs) == 0 {
		return nil
	}
	cleared := make([]uint16, 0, len(memberIDs))
	for _, characterID := range memberIDs {
		if characterID == 0 {
			continue
		}
		identity, ok := s.onlineGameSessionCharacterSnapshot(characterID)
		if !ok {
			continue
		}
		session := identity.session
		forgotten, counts := s.forgetRetiredDungeonPartyRunKey(identity, retiredPartyID)
		if !forgotten {
			continue
		}
		cleared = append(cleared, characterID)
		if !counts.empty() {
			s.logGameEvent(session, "game-party-dungeon-peer-projection-generation-reset",
				"source", source+"_peer_projection_generation", "char_id", characterID, "conn_id", session.connID,
				"dropped_projected_peers", counts.projected, "dropped_owner_override_rows", counts.overrides,
				"dropped_resend_keys", counts.resends,
				"reason", "the_dungeon_run_was_invalidated_so_every_peer_must_be_projected_again_on_the_next_entry",
				"switch", dungeonPartyPeerProjectionGenerationEnv, "mode", dungeonPartyPeerProjectionGenerationMode())
		}
		s.logGameEvent(session, "game-party-dungeon-run-key-cleared",
			"source", source,
			"char_id", characterID,
			"conn_id", session.connID,
			"reason", "the_runtime_party_was_disbanded_so_the_recorded_run_key_has_no_owner",
			"switch", dungeonPartyRunKeyTownResetEnv,
			"mode", dungeonPartyRunKeyTownResetMode())
	}
	return cleared
}

// Clear the retired key and its peer projection as one operation. A new
// membership or another run must survive delayed cleanup from an old instance.
func (s *Service) forgetRetiredDungeonPartyRunKey(identity boundGameSessionCharacter, retiredPartyID uint16) (bool, dungeonPeerProjectionGenerationCounts) {
	var counts dungeonPeerProjectionGenerationCounts
	manager := s.runtimePartyManagerForService()
	s.mu.Lock()
	defer s.mu.Unlock()
	session := identity.session
	if session == nil || s.gameSessions[identity.character] != session || session.characterGeneration != identity.generation || session.selectedCharacterID != identity.character {
		return false, counts
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if !session.dungeonPartyRunValid || session.dungeonPartyRun.PartyID != retiredPartyID {
		return false, counts
	}
	err := manager.WithSoloMember(identity.character, func() error {
		session.dungeonPartyRun = dungeonPartyRunKey{}
		session.dungeonPartyRunValid = false
		if dungeonPartyPeerProjectionGenerationEnabled() {
			counts = dropDungeonPeerProjectionGenerationLocked(session)
		}
		return nil
	})
	return err == nil, counts
}
