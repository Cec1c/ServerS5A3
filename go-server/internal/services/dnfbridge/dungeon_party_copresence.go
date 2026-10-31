package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 2026-09-24 双开复测（packet_log.20260924-225314.txt，22:49:40 进图）后剩下的两条症状：
//
//	② 队长视角：看得到队员进入地图，但队员不会动；
//	③ 队员视角：队长的加载条消失，进副本后只有自己一个人。
//
// 证据链（修前）：
//   - 两个会话各持一份 `gameSession.dungeon.runtime`（本工程是 **per-session**），
//     组队进图只保证"解析出的 dungeon/maze/room/map/seed 相同"
//     （`game-party-dungeon-entry-member-prepared` + 两条 `game-dungeon-runtime-prepared`），
//     没有任何"这是同一场"的共享对象。参考实现用队伍级实例
//     （`ServerS4A21-master\...\Dungeon\DungeonIdentity.cs` 的
//     `PartyDungeonInstanceId` + `DungeonInstanceRegistry`，按 PartyId 注册），
//     那份共享实例尚未合并到本工程——本文件先把"同一场"做成**显式运行键**，
//     用它给队友 actor 投影把关，给后续共享实例留出同一个判据。
//   - 进图后服务端**从不把队友 actor 投影给对面**：唯一可用的同屏投影
//     `sendTownRemoteActorState` 只按城镇上下文（owner=收包者频道）发，而且
//     `party_town_return.go` 明确要求 `hasDungeonRuntime == false`。
//     现场：进图窗口（packet_log.20260924-225314.txt 行 1335-1565）里没有任何
//     owner=0 的队友 mode0/mode1/op9；操作者本机 trace（pid=21380，角色 3）在整个副本期间
//     没有一条 key=5（队友）的包。⇒ 队员客户端根本没有队长 actor（"只有自己"），
//     队长客户端只剩进图前那份城镇 actor（"看得到但不动"）。
//   - 位移本身走客户端 P2P：副本内两个连接都没有 op22/op36 位置包；op11 已经把中转端点发对
//     （实测 `uid=3 → 192.0.2.12:31001`、`uid=5 → 192.0.2.12:31000`，
//     `DNFBRIDGE_PARTY_UDP_RELAY_ENABLED=true`，dnf90.out 无 relay 警告）。
//     所以缺的是"对面 actor 存在"，不是端点。
//
// 修法（阶段一，只补同屏投影）：两人运行键相等时，各自落位完成后把对方的 actor 用
// **城镇里已经验证过的形状**投影过去：`mode0`（全局场景 actor；owner 字节取
// **收包者频道**，客户端 `mode0-owner-compare` 判定 scene_channel == owner →
// current-scene-create）→ `mode1`（装备绑定）→ `op9 kind0`（队伍槽位表，同一 owner）。
// 不重放 op11：仓库既有结论是刚建好的 UDP 对端再握手会把客户端打回 connecting。

// dungeonPartyRunKey 是"同一场副本"的判据。字段全部来自运行中的副本运行时与场景，
// 不使用任何会被后续过图改写的会话状态。
type dungeonPartyRunKey struct {
	PartyID        uint16
	RaidKey        uint32
	RaidInstanceID string
	RaidRunID      string
	DungeonID      int64
	MazeIndex      int
	RoomX          int64
	RoomY          int64
	MapID          int64
	Seed           uint32
}

func dungeonPartyRunKeyFrom(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) (dungeonPartyRunKey, bool) {
	if session == nil || runtime == nil || session.selectedCharacterID == 0 {
		return dungeonPartyRunKey{}, false
	}
	state := runtimePartyStateSnapshot(session)
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) < 2 {
		return dungeonPartyRunKey{}, false
	}
	key := dungeonPartyRunKey{
		PartyID:   uint16(state.PartyID),
		DungeonID: runtime.Dungeon.ID,
		MazeIndex: runtime.MazeIndex,
		RoomX:     scene.Coordinate.X,
		RoomY:     scene.Coordinate.Y,
		MapID:     scene.Map.Map.ID,
		Seed:      runtime.Seed,
	}
	if entry := runtime.raidEntry; entry != nil {
		key.RaidKey = entry.RaidKey
		key.RaidInstanceID = entry.InstanceID
		key.RaidRunID = entry.RunID
	}
	return key, true
}

// recordDungeonPartyRunKey 在当前会话落位完成时记录"我在哪一场"。换场时清空
// 已投影集合，让新的一场还能再画一次队友。
func (s *Service) recordDungeonPartyRunKey(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) {
	if session == nil {
		return
	}
	key, ok := dungeonPartyRunKeyFrom(session, runtime, scene)
	session.dungeonPartyRunMu.Lock()
	if !ok {
		session.dungeonPartyRun = dungeonPartyRunKey{}
		session.dungeonPartyRunValid = false
		session.dungeonPartyRunMu.Unlock()
		return
	}
	previousKeyValid := session.dungeonPartyRunValid
	runChanged := previousKeyValid && session.dungeonPartyRun != key
	// 世代结束的两种形状（见 dungeon_party_peer_projection_generation.go）：
	//   ① 上一条键还有效但换了场（换房间/换实例）；
	//   ② 上一条键已经被"回城失效/解散清键"作废（`dungeonPartyRunValid=false`）——
	//      这一条是操作者「第二次进图时装又不显示」的成因：少了它，投影集合会跨世代残留，
	//      `claimDungeonPeerProjection` 对同一名队友恒返回 false，整场不再投影。
	generationReset := runChanged ||
		(!previousKeyValid && dungeonPartyPeerProjectionGenerationEnabled())
	var dropped dungeonPeerProjectionGenerationCounts
	if runChanged {
		if dungeonPartyPeerProjectionGenerationEnabled() {
			dropped = dropDungeonPeerProjectionGenerationLocked(session)
		} else {
			session.dungeonPeerProjected = nil
			session.dungeonOwnerOverrideLogged = nil
		}
	} else if generationReset {
		dropped = dropDungeonPeerProjectionGenerationLocked(session)
	}
	session.dungeonPartyRun = key
	session.dungeonPartyRunValid = true
	if session.dungeonPeerProjected == nil {
		session.dungeonPeerProjected = make(map[uint16]boundGameSessionCharacter, 4)
	}
	session.dungeonPartyRunMu.Unlock()
	if generationReset && !dropped.empty() {
		s.logGameEvent(session, "game-party-dungeon-peer-projection-generation-reset",
			"source", "record_dungeon_party_run_key",
			"char_id", session.selectedCharacterID,
			"conn_id", session.connID,
			"new_party_id", key.PartyID,
			"new_raid_key", key.RaidKey,
			"new_raid_instance_id", key.RaidInstanceID,
			"new_raid_run_id", key.RaidRunID,
			"new_dungeon_id", key.DungeonID,
			"new_seed", key.Seed,
			"previous_run_key_valid", previousKeyValid,
			"run_changed", runChanged,
			"dropped_projected_peers", dropped.projected,
			"dropped_owner_override_rows", dropped.overrides,
			"dropped_resend_keys", dropped.resends,
			"reason", "a_new_dungeon_run_key_starts_a_new_peer_projection_generation",
			"switch", dungeonPartyPeerProjectionGenerationEnv,
			"mode", dungeonPartyPeerProjectionGenerationMode())
	}
}

// recordCurrentDungeonPartyRunKey materializes the current run key after a
// party is accepted inside an already-entered dungeon. A solo entry did not
// have a party run key, so projection would otherwise fail its same-run gate.
func (s *Service) recordCurrentDungeonPartyRunKey(session *gameSession) bool {
	if s == nil || session == nil {
		return false
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil {
		session.dungeon.mu.Unlock()
		return false
	}
	scene, ok := runtime.Session.Scene()
	var raidEntry *currentRaidDungeonEntry
	if runtime.raidEntry != nil {
		copyEntry := *runtime.raidEntry
		raidEntry = &copyEntry
	}
	snapshot := runtimeDungeonState{Dungeon: runtime.Dungeon, MazeIndex: runtime.MazeIndex, Seed: runtime.Seed, raidEntry: raidEntry}
	session.dungeon.mu.Unlock()
	if !ok {
		return false
	}
	s.recordDungeonPartyRunKey(session, &snapshot, scene)
	return true
}

// dungeonPeerOwnerPlan 是一次"副本内发给**其他**队员"的 owner 计划。两个字段可以不同
// （`mixed` 档就是不同），因为客户端对 op2 与 op9 吃的是两条不同的判断。
type dungeonPeerOwnerPlan struct {
	// Mode 是 DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE 的归一化档位（zero/channel/mixed），
	// 只用于日志判读，不参与组包。
	Mode string
	// ActorOwner 是 op2 mode0/mode1 的 owner（body[4]）。
	ActorOwner byte
	// PartyFrameOwner 是 op9 kind0 记录的 owner（body[7]）。
	PartyFrameOwner byte
}

// overridesPeerOwner 报告这一档是否真的改写了任何一个 owner 字节（zero 档 = false）。
func (plan dungeonPeerOwnerPlan) overridesPeerOwner() bool {
	return plan.ActorOwner != currentSceneObjectContext || plan.PartyFrameOwner != currentSceneObjectContext
}

// ownerFields 是"档位 + 两个实际 owner 值"的日志字段：发送处统一带上它，
// 线上不必对照另一条日志就能判出 zero/channel/mixed 哪一档生效、实际发了哪两个字节。
// `mode=` 的字段名与取证报告 §8/父代理约定的判读串一致。
func (plan dungeonPeerOwnerPlan) ownerFields() []any {
	return []any{
		"mode", plan.Mode,
		"op2_owner_channel", plan.ActorOwner,
		"op9_owner_channel", plan.PartyFrameOwner,
	}
}

// partyDungeonPeerOwnerPlan 给出"副本内发给**其他**队员"的 op2 mode0/mode1 与 op9 该写的
// owner 字节。默认（`DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE=zero` 或未设置）就是现行为。
//
// # 字段名 → 值来源（权威写入点，全部是"参数直接落字节"，不靠猜偏移）
//
//   - op2 mode0：`buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext`
//     （character_scene_object.go:83）的 `ownerChannel` 参数 ⇒ **body[4]**。body 布局：
//     [0]=mode(0), [1:3]=uint16(1), [3]=`currentSceneObjectRoute`, [4]=owner。
//     客户端 trace 的 `opcode=2 mode=0 owner=N` 读的就是 body[0] 与 body[4]。
//   - op2 mode1：`buildCurrentUserInfoMode1WithEquipmentInContext`
//     （userinfo_current_modes.go:35）的 `ownerChannel` 参数 ⇒ **body[4]**，布局同 mode0
//     （[0]=mode(1), [1:3]=1, [3]=route, [4]=owner）。
//   - op9：`buildCurrentSceneOp9ActorPartyDisplayBodyInContext`（scene_op9.go:139）的
//     `ownerChannel` 参数 ⇒ **body[7]**。布局：[0:2]=uint16(1), [2:4]=9999,
//     [4:6]=objectKey, [6]=0, [7]=owner, [8]=0, [9]=kind。客户端 trace 的
//     `opcode=9 owner=N kind=K key=<objectKey>` 读的就是 [7]/[9]/[4:6]，逐字节对上。
//
// # 为什么现在是 0
//
// 城镇同屏投影（`sendTownRemoteActorState`）这三个字节取的是
// `currentTownRemoteActorOwnerContext(target)` = `session.townActorOwnerChannel` =
// `channel.ID`（现场 = 121）；副本投影（`sendDungeonPartyPeerActorState`）把同一个参数
// 硬编码成了 `currentSceneObjectContext`（character_protocol_constants.go:11 = 0），
// op9 走 `currentPartyFrameOwnerDungeonContext` → 同样写 0（party_online.go:1311）。
// 所以客户端 trace 里副本内队友 `owner=0`、城镇 `owner=121`，全日志 319 条
// `town co presence local actor with owner context` 里 owner=0 的行数为 0。
//
// # 三档各自改什么
//
//   - `zero`（默认）：两个 owner 都是 0 = 现行为，逐字节不变。
//   - `channel`：两个 owner 都取**收包者当前所选频道号**（`currentClientSelectedChannel`，
//     与城镇那条路同源；本会话 = 121）= 00:22 之前那一版的线格式。
//   - `mixed`：**op2 取频道号、op9 取 0**。这是前两格的交叉组合，此前从未试过：
//     op2 走 `current-scene-create` 分支（客户端 trace 的 `mode0-owner-compare` 判据），
//     同时 op9 保住成员行的跨频道内联身份块（`member_identity_block=true`）。
//
// 收包者频道未提交（0）时两个 owner 一律回退 0——与现行为一致，绝不写出"半个覆盖"。
// 本地角色自己那条包不经过这里（见 party_online.go 的 peer 包装）。
//
// ⚠️ op9 的 owner 与"成员行是否带内联身份块"**不是两个独立变量**：客户端
// `sub_1D64CA0` 拿"记录 owner 字节 vs 客户端实际所选频道"自行判分支，owner 取频道号时
// 成员行必然不带身份块。所以 `channel` 档在 op9 上是联动改动，`mixed` 档才是
// "只动 op2"的干净单变量。
func (s *Service) partyDungeonPeerOwnerPlan(receiver *gameSession) dungeonPeerOwnerPlan {
	mode := partyDungeonOwnerSceneContext
	if s != nil {
		mode = s.options.partyDungeonOwner
	}
	plan := dungeonPeerOwnerPlan{
		Mode:            mode.String(),
		ActorOwner:      currentSceneObjectContext,
		PartyFrameOwner: currentSceneObjectContext,
	}
	channelID := currentClientSelectedChannel(receiver)
	if channelID == currentSceneObjectContext {
		// 客户端频道还没提交：任何档位都只能回退现行为。
		return plan
	}
	switch mode {
	case partyDungeonOwnerClientChannel:
		plan.ActorOwner = channelID
		plan.PartyFrameOwner = channelID
	case partyDungeonOwnerMixed:
		plan.ActorOwner = channelID
	}
	return plan
}

// logDungeonPartyOwnerOverride 在开关真正改写了 owner 字节时记一条事件，按
// (连接, 队友 key) 去重，避免每次补发都刷屏。队友 key = 客户端 trace 里的 actor
// object_key = 队友角色 id（`currentSceneActorObjectKey`）。
//
// 事件里同时带**档位**与**两个实际 owner 值**（`op2_owner_channel`/`op9_owner_channel`），
// 线上不必对照发送日志就能判出 zero/channel/mixed 哪一档生效、实际发出的是哪两个字节。
func (s *Service) logDungeonPartyOwnerOverride(receiver *gameSession, peerCharID uint16, plan dungeonPeerOwnerPlan, source string) {
	if s == nil || receiver == nil || peerCharID == 0 || !plan.overridesPeerOwner() {
		return
	}
	peerKey := currentSceneActorObjectKey(peerCharID)
	receiver.dungeonPartyRunMu.Lock()
	if receiver.dungeonOwnerOverrideLogged == nil {
		receiver.dungeonOwnerOverrideLogged = make(map[uint16]bool, 4)
	}
	if receiver.dungeonOwnerOverrideLogged[peerKey] {
		receiver.dungeonPartyRunMu.Unlock()
		return
	}
	receiver.dungeonOwnerOverrideLogged[peerKey] = true
	receiver.dungeonPartyRunMu.Unlock()
	// owner= 是"这一档实际改掉的第一个非零值"（zero 档不会走到这里）：
	// mixed 档 = op2 的频道号（op9 仍是 0），channel 档两个值相同。
	owner := plan.ActorOwner
	if owner == currentSceneObjectContext {
		owner = plan.PartyFrameOwner
	}
	fields := []any{
		"source", source,
		"peer_key", peerKey,
		"peer_char_id", peerCharID,
		"receiver_char_id", receiver.selectedCharacterID,
		"owner", owner,
		"local_channel", currentClientSelectedChannel(receiver),
		"env", partyDungeonOwnerModeEnv,
	}
	fields = append(fields, plan.ownerFields()...)
	s.logGameEvent(receiver, "game-party-dungeon-owner-override", fields...)
}

func (s *Service) currentDungeonPartyRunKey(session *gameSession) (dungeonPartyRunKey, bool) {
	if session == nil {
		return dungeonPartyRunKey{}, false
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if !session.dungeonPartyRunValid {
		return dungeonPartyRunKey{}, false
	}
	return session.dungeonPartyRun, true
}

// claimDungeonPeerProjection tracks the peer binding, not only its character ID.
// 发送失败时用 releaseDungeonPeerProjection 回滚，保证下一轮还能补发。
func (s *Service) claimDungeonPeerProjection(session *gameSession, peer boundGameSessionCharacter) bool {
	if session == nil || !s.boundGameSessionCharacterCurrent(peer) {
		return false
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if session.dungeonPeerProjected == nil {
		session.dungeonPeerProjected = make(map[uint16]boundGameSessionCharacter, 4)
	}
	if session.dungeonPeerProjected[peer.character] == peer {
		return false
	}
	session.dungeonPeerProjected[peer.character] = peer
	return true
}

func (s *Service) releaseDungeonPeerProjection(session *gameSession, peer boundGameSessionCharacter) {
	if session == nil || peer.character == 0 {
		return
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if session.dungeonPeerProjected[peer.character] == peer {
		delete(session.dungeonPeerProjected, peer.character)
	}
}

// projectDungeonPartyPeerActors 双向各投影一次：把队友画给本人，也把本人画给队友。
// 进图落位有先后：先落位的一方此时队友还没有运行键，什么也不做；后落位的一方
// 会在这一次调用里把两个方向都补齐（先做的不标记，避免漏发）。
func (s *Service) projectDungeonPartyPeerActors(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	self, current := s.boundGameSessionCharacterSnapshot(session)
	if !current {
		return nil
	}
	state := runtimePartyStateSnapshot(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		return nil
	}
	ownKey, ready := s.currentDungeonPartyRunKey(session)
	if !ready {
		return nil
	}
	for _, member := range members {
		if member.UserID == 0 || member.UserID == session.selectedCharacterID {
			continue
		}
		peerIdentity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		peer := peerIdentity.session
		if !online || peer == nil || peer == session {
			continue
		}
		peerKey, peerReady := s.currentDungeonPartyRunKey(peer)
		if !peerReady || peerKey != ownKey {
			continue
		}
		if s.claimDungeonPeerProjection(session, peerIdentity) {
			if err := s.sendDungeonPartyPeerActorState(
				session,
				peer,
				source+"_peer_actor_to_self",
			); err != nil {
				s.releaseDungeonPeerProjection(session, peerIdentity)
				return err
			}
		}
		if s.claimDungeonPeerProjection(peer, self) {
			if err := s.sendDungeonPartyPeerActorState(
				peer,
				session,
				source+"_self_actor_to_peer",
			); err != nil {
				s.releaseDungeonPeerProjection(peer, self)
				return err
			}
		}
	}
	// 队友 actor 建出来之后补一次**已上报坐标**（op35 快照），让新 actor 落在队友真实
	// 位置上而不是默认点。默认 off；形状与城镇 town_copresence_move_0x0016 完全一致。
	s.broadcastDungeonPartyPeerPosition(session, source+"_dungeon_actor_initial_position")
	// 本会话是**后落位**的那一方：把"自己"再推给**场景已经稳定**的队友一次。
	// 先落位一方自己的 finish-loading 补发对它永远不触发（那一刻对端还没有运行键），
	// 于是它手里那份投影落在场景重建窗口里会被客户端丢掉 —— 现场队长一侧的时装丢失
	// 就是这一条。见 dungeon_party_peer_actor_landing_resend.go。
	s.resendDungeonPartyPeerActorToAlreadyLandedMembers(session, source)
	return nil
}

// sendDungeonPartyPeerActorState 把一个队友的 actor 投影进收包者当前的副本场景。
//
// owner 字节用**副本场景上下文**（`currentSceneObjectContext`，即本地 actor 用的那个 0）：
//   - 客户端 `mode0-owner-compare` 对 owner 0 走 `current-scene-context0-remap`（= current-scene-create），
//     与 owner=收包者频道时是**同一条本地分支**。操作者客户端 trace 实证（pid=43200，23:59:47）：
//     owner=121 的队友 mode0 同样进 `local-actor-create object_key=5` 并建出对象，
//     所以换 owner 不改变 actor 的创建路径；
//   - **op9 记录**的分支判据是"记录 owner 字节 vs 客户端实际所选频道"（`sub_1D64CA0`）。
//     取副本上下文 0（≠ 登录频道 121）→ 每个成员行带内联身份块（job/level/grow/name），
//     与本地 actor 那条 op9 记录（`owner_channel=0`）完全同形；取收包者频道时两者相等 →
//     成员行**不带**跨频道身份块。
//   - ⚠️ 这两条读法在"副本内队友不可见"这件事上**互相矛盾，尚未定论**（2026-09-25）：
//     这里原先的理由是"owner 取频道 ⇒ member_identity_block=false ⇒ 客户端不认队友 actor"
//     （00:45 版复测），但客户端钩子取证（`90CN_副本队友actor取证报告.md` §5）给出的是
//     反过来的相关性：城镇同屏（**能看见**）的包 owner=121，副本内（看不见）owner=0；
//     全日志 319 条 `town co-presence local actor with owner context` 里 **owner=0 的行数为 0**
//     ⇒ owner 是"owner-context 登记"这条钩子路径的唯一开关。
//     两个读法只能靠 A/B 定：**默认值仍是 0（现行为），
//     `DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE=channel` 才把发给其他队员的 owner 换成频道号**，
//     见 partyDungeonPeerOwner。A/B 的判据与预期日志（客户端侧
//     `mode0-owner-compare ... branch=current-scene-create` + `town co presence local actor
//     with owner context`）写在 `changlog.md` 第七批/取证报告 §8。
//
// 不重放 op11：仓库既有结论是刚建好的 UDP 对端再握手会把客户端打回 connecting。
//
// ⚠️ 副本内**不发送任何"落位/放置"包**（2026-09-25 定形，证据如下）：
//   - 客户端的 actor 生成原语就是 USERINFO 0x0002 的 subtype0（外观/建对象）+ subtype1
//     （完整属性/装备/技能）。同族实现（S4A21 `TownHandler.BroadcastAreaRosterAsync`，
//     注释标注"真机实测（逆向+抓包结论）"）写明："只发 0x17/0x18 客户端既不生成他人角色对象、
//     也不主动拉外观"，所以城镇同屏 = subtype0 + subtype1 + 0x0017(USER_AREA) + 0x0018(AREA_USERS)。
//     城镇那条我们能看见队友，就是因为它对上了这套原语。
//   - 但同族实现在**副本里没有任何"远端队友 actor 落位"实现**：`Handlers/Dungeon/*` 里
//     0x0002 只用于本人（进本 subtype1 / 回城 subtype0）与同频道好友状态广播，所有
//     0x0017/0x0018 都出现在"离开副本回城"的城镇投影里；副本参与者之间只中继服务端权威事件
//     （击杀/清图/结算，`InstanceRegistry` 参与者中继）。
//   - 我们试过的两条落位包都无效：op22（0x16 自身位置通知）复测让**收包方自己的模型消失**；
//     op23（0x17 USER_AREA，02:12 版上线）复测仍然看不见队友 ⇒ 无证据支持，已移除。
//   - 重新加回的门槛（需要客户端证据，不要再试包）：证明当前 EXE 在副本场景里会把"非本地
//     actor"登记进房间 actor 表——加只读钩子 dump `object_manager` 已登记 key 列表，在收到
//     队友帧前后各取一次；只有登记成功才谈落位。
//
// 因此本函数只做**对象创建**（与本地 actor 同形的 mode0/mode1/op9），不声明任何位置语义。
func (s *Service) sendDungeonPartyPeerActorState(
	receiver, actor *gameSession,
	source string,
) error {
	if receiver == nil || actor == nil || actor.selectedCharacterID == 0 {
		return nil
	}
	receiverIdentity, receiverCurrent := s.boundGameSessionCharacterSnapshot(receiver)
	actorIdentity, actorCurrent := s.boundGameSessionCharacterSnapshot(actor)
	if !receiverCurrent || !actorCurrent {
		return nil
	}
	membershipCurrent := s.currentPartyProjectionMembershipGuard(actorIdentity, nil)
	receiverRun, receiverHasRun := s.currentDungeonPartyRunKey(receiver)
	actorRun, actorHasRun := s.currentDungeonPartyRunKey(actor)
	// owner 计划：默认 zero = 两个 owner 都是副本场景上下文 0（现行为）；
	// channel = 两个都是收包者频道号；mixed = op2 取频道号、op9 取 0。
	// 权威写入点与三档理由见 partyDungeonPeerOwnerPlan。
	ownerPlan := s.partyDungeonPeerOwnerPlan(receiver)
	if currentClientSelectedChannel(receiver) == currentSceneObjectContext {
		// 会话频道未提交，或者 > 255（u8 owner 字段装不下，客户端频道无法用字节表达）。
		// 两条都**明确失败**：绝不截断成 `byte(508)`=252 那种"另一个频道号"。
		return fmt.Errorf(
			"dungeon peer actor client channel is not expressible as an owner byte: connected=%d character=%d",
			receiver.channel.ID,
			receiver.selectedCharacterID,
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	// 队友档技能页标记（DNFBRIDGE_PEER_SKILL_PAGE 默认档 = peer）：
	// 这条 ctx 产出的 mode1 USERINFO 是**发给队友**（receiver）的 actor body，
	// 所以它的技能页槽位要写被投影 actor 的真实技能等级 —— 否则收包方客户端把
	// 这个 actor 的所有 BUFF 按 0 级算（现场症状：勇气祝福特效可见、面板不动、buff 栏 0 级）。
	// 判定与档位表见 peer_skill_page.go 文件头；发给本人的 body 不带这个标记，一个字节都不变。
	ctx = withDungeonPartyPeerSkillPageTarget(ctx, receiverIdentity.character)
	// Mode0 and mode1 describe one actor snapshot. A second repository read
	// failing after mode0 must not become a successful empty equipment list.
	var err error
	ctx, err = s.currentRemoteActorEquipmentSnapshot(ctx, actor, actorIdentity.character)
	if err != nil {
		return fmt.Errorf("snapshot dungeon peer equipment: %w", err)
	}
	charID, charName, character, hasCharacter := s.characterForEnter(ctx, actor, actorIdentity.character)
	if charID != actorIdentity.character || !hasCharacter {
		return fmt.Errorf(
			"dungeon peer actor repository record unavailable: requested=%d loaded=%d found=%t",
			actorIdentity.character,
			charID,
			hasCharacter,
		)
	}
	mode0Body, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(
		ctx,
		actor,
		charID,
		charName,
		character,
		hasCharacter,
		ownerPlan.ActorOwner,
	)
	if err != nil {
		return fmt.Errorf("build dungeon peer actor mode0: %w", err)
	}
	// This frame arrives after dungeon USER_STATE and may create or reuse an
	// actor. Native mode0 copies its state into actor+0x4c, so a town default
	// would undo state=1, including when the appearance frame is repeated.
	mode0Body, err = projectCurrentDungeonPeerActorState(mode0Body, currentSceneActorObjectKey(charID))
	if err != nil {
		return fmt.Errorf("project dungeon peer actor state: %w", err)
	}
	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(ctx, actor, character, hasCharacter)
	mode1Body := s.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		ctx,
		actor,
		character,
		hasCharacter,
		charID,
		uint32(adventureSummary.ManageLevel),
		ownerPlan.ActorOwner,
		true,
	)
	s.logDungeonPartyOwnerOverride(receiver, charID, ownerPlan, source)
	peerActorFields := []any{
		"conn_id", receiver.connID,
		"source", source,
		"receiver_char_id", receiver.selectedCharacterID,
		"actor_char_id", charID,
		"owner_channel", ownerPlan.ActorOwner,
		"owner_mode", ownerPlan.Mode,
		"object_key", currentSceneActorObjectKey(charID),
		"mode0_body_len", len(mode0Body),
		"mode1_body_len", len(mode1Body),
		"body_source", "current_exe_mode0_create_then_mode1_bind_then_party_op9_in_dungeon_context",
	}
	// 发送处日志同时给出档位与**两个实际 owner 值**（op2 的 body[4] 与 op9 的 body[7]），
	// 线上一眼判档位；`owner_channel`/`owner_mode` 是 op2 的实际值与档位。
	peerActorFields = append(peerActorFields, ownerPlan.ownerFields()...)
	peerActorFields = append(peerActorFields, currentPeerActorWireDiagnostic(mode0Body, mode1Body)...)
	s.logGameEvent(receiver, "game-dungeon-party-peer-actor-send", peerActorFields...)
	// 判读事件：这条投影到底带没带队友的时装（克隆装扮链接字段）。操作者的
	// 「队长看不到队员时装、队员看得到队长时装」两个方向都靠这一条事件判定，见
	// party_peer_appearance.go 的文件头。
	//
	// subtype0Body 是**第二条外观载体**（被投影角色自己的 USERINFO subtype0 外观列表，
	// 厂商 TownHandler.cs:502-515 的同屏口径）：mode0 那条载体的 delta 已经是 0，
	// 缺的就是这一条。见 party_peer_appearance_subtype0.go 的文件头。
	subtype0Body := s.peerActorUserInfoSubtype0BodyForProjection(
		ctx, receiver, actor, character, hasCharacter, charID, charName, mode0Body,
	)
	s.logCurrentPeerActorAppearanceEvent(receiver, actor, source, mode0Body, mode1Body, subtype0Body)
	delivered := false
	if err := s.withGameWire(receiver, func() error {
		if !s.boundGameSessionCharacterCurrent(receiverIdentity) || !s.boundGameSessionCharacterCurrent(actorIdentity) || !membershipCurrent() {
			return nil
		}
		currentReceiverRun, currentReceiverHasRun := s.currentDungeonPartyRunKey(receiver)
		currentActorRun, currentActorHasRun := s.currentDungeonPartyRunKey(actor)
		if receiverHasRun != currentReceiverHasRun || actorHasRun != currentActorHasRun || receiverRun != currentReceiverRun || actorRun != currentActorRun {
			return nil
		}
		// Keep creation and binding adjacent on the receiver's stream. A leave
		// removal must not split them and then be undone by the old binding.
		for _, body := range [][]byte{mode0Body, mode1Body} {
			if err := s.sendGameUpperRawClassCodecLocked(receiver, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0, true); err != nil {
				return err
			}
		}
		delivered = true
		return nil
	}); err != nil {
		return fmt.Errorf("send dungeon peer actor snapshot: %w", err)
	}
	if !delivered {
		return nil
	}
	if len(subtype0Body) != 0 {
		s.sendPeerUserInfoSubtype0Appearance(receiver, subtype0Body, source+"_peer_userinfo_subtype0", charID)
	}
	// Live mode1 loads can clear the peer display after mode0 while keeping
	// clone links. Reapply the proved appearance after equipment/identity.
	if len(subtype0Body) != 0 && currentSceneObjectAppearanceBodyHasCloneLink(mode0Body) {
		if err := s.sendGameUpperRawClass(receiver, uint16(dnfenum.CmdPacketSetUDPIPPort), mode0Body, 0); err != nil {
			return err
		}
		s.logGameEvent(receiver, "game-party-clone-appearance-after-equipment", "peer_char_id", charID, "source", source)
	}
	if _, err := s.sendCurrentPartyActorFrameProjectionForDungeonPeer(
		receiver,
		actor,
		runtimePartyStateSnapshot(actor),
		source+"_op9",
	); err != nil {
		return fmt.Errorf("send dungeon peer actor op9: %w", err)
	}
	s.logGameEvent(receiver, "game-dungeon-party-peer-actor-delivered",
		"source", source, "receiver_char_id", receiver.selectedCharacterID,
		"actor_char_id", charID, "actor_generation", actor.characterGeneration,
		"receiver_generation", receiver.characterGeneration)
	// 到此为止：客户端拿到"队友 actor 的外观 + 属性 + 队伍记录"，对象会被建出来
	// （trace 实证：`local-actor-create object_key=5`，随后 op9 走完 before/after）。
	// **不再往下发落位包**：op120（`buildCurrentSceneActorPlacementBody` 的 body 只有
	// actor_slot+seed、不带 actor key）与 op23 两条都已在 07-05 / 02:12 两版复测过——
	// 队友仍然不可见，同族实现也没有对应的副本内落位原语（详见函数头注释）。
	// 缺少的客户端证据：副本场景里"非本地 actor 被登记进房间 actor 表"。
	return nil
}
