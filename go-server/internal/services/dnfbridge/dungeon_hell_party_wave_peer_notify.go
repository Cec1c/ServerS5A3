package dnfbridge

import (
	"fmt"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是组队深渊房间缺陷「**组队挑战深渊 队员的深渊柱子异常：怪物出现了、柱子没碎、
// 也没有掉落碎片**」的修复。
//
// ── 上一轮（已上线）修了什么、为什么症状还在 ──────────────────────────────────
//
// 上一轮新增了波次怪退役的队友投影（`notifyCurrentHellPartyWaveRetirementToPartyPeersLocked`），
// **它确实生效了**：现场（`conn_id=game-000031`，2026-09-28T17:17:46–49Z）有 5 条
//
//	EVENT kind=game-dungeon-hell-party-wave-peer-notify … object_key=438/442/439/441/440
//	      from_char_id=1 to_char_id=6 response_opcode=38 body_len=7 drop_rows_stripped=true
//
// 也就是说"少了死亡投影 = 白色残影"这一半已经修好。但操作者报的两个症状都还在，
// 因为那一版投影**只发了一项**、而且**故意把掉落行剥掉了**：
//
//	① 柱子（map-owned special passive object 30528，房图 60083 的 `[special passive object]`
//	   + 行内 `[hellparty]`）**从来没有被投影过**：厂商那条分支
//	   （`DungeonCombatHandler.cs:78-152`）对 map-owned 对象不回 op38，本仓同口径
//	   （`dungeon_passive_object_drop.go:569-651`：`death_ack_sent=false`），而客户端
//	   也**从不**上报它（全日志 `game-dungeon-map-owned-passive-object-destroyed` = 0）。
//	   ⇒ 队长那一侧的客户端是**自己**把柱子打碎的（本地权威），队员那一侧的客户端
//	   没有任何东西告诉它柱子没了 ⇒ 操作者看到的"怪物出现了、柱子没碎"
//	   （怪物是服务端在进房时公告的扩展 actor，两边都有）。
//
//	② 掉落行被剥掉：击杀方那一条 op38 是 1150 字节（`hell_party_reward_drops=9
//	   abyss_epic_fragment_drops=9` = 18 条掉落行），队友那一条只有 7 字节
//	   （`drop_rows_stripped=true`）。而**地面掉落行正是客户端知道"地上有东西"的唯一途径**
//	   ⇒ 操作者看到的"也没有掉落碎片"。
//
//	   ⚠ 同一份日志里既有普通怪的队伍死亡广播**是带掉落行的**：
//	   `game-dungeon-party-instance-death-broadcast … object_key=405 from_char_id=1
//	   to_char_id=6 response_opcode=38 body_len=134`（击杀方那一份也是 134）。
//	   也就是说"剥掉落"只发生在上一轮新加的这条深渊波次投影上，与既有的队伍通路**不一致**。
//	   地面对象由 objectKey/sceneSlot 全局唯一标识，**同一个对象发给两名成员是共享**，
//	   不是复制（复制指的是同一件物品发两次奖励，那发生在掉落账本，不在这条投影上）。
//
// ── 本轮修法 ─────────────────────────────────────────────────────────────────
//
//	① `DNFBRIDGE_HELL_PARTY_WAVE_PEER_DROPS`（默认 `shared`）：队友拿到的 op38 与击杀方
//	   **逐字节同一份 body**（含全部掉落行）⇒ 两名成员看到同一批地面掉落。
//	   回退字面量 `stripped` = 逐字节复现上一轮那版（7 字节、无掉落行）。
//	② `DNFBRIDGE_HELL_PARTY_PILLAR_PEER_DESTROY`（默认 `on`）：深渊房间**第一只**波次怪
//	   退役时，把柱子的破坏投影给同一队伍副本实例内的其它会话（op38、不带掉落行、
//	   每房间每成员只发一次）。柱子的身份 = 房图 `[special passive object]` 里唯一带
//	   行内 `[hellparty]` 的那一行的**对象 code**（真档 30528）—— 这正是厂商
//	   `DieMonsterRequest.cs:53-54` 里客户端用的同一身份（`localIndex` = 对象 code，
//	   见 `dungeon_passive_object_drop.go` 文件头 §3 的取证）；本仓
//	   `handleCurrentDungeonMapOwnedPassiveObjectDeathLocked` 也是拿这个 code 比对注册表。
//	   回退字面量 `legacy` = 不投影（= 今天的线上行为）。
//
// ——"客户端会不会接受这一条 op38"**未证**（厂商对 map-owned 对象不回 op38，本仓也没有
// 现场样本）。这是本文件唯一的假设，写在这里而不是藏在代码里：它的可观测判据是
// 队员屏幕上柱子是否消失（操作者验收步骤第 3 条），一条 env 即可回退，不需要改代码。
// 之所以默认开：本轮任务书要求"默认 = 修复"，且这条 op38 用的是**客户端自己上报柱子时
// 用的同一个身份**，与波次怪退役那一条**同形同 opcode**（只是 key 不同）。
const (
	dungeonHellPartyWavePeerNotifyEnv = "DNFBRIDGE_HELL_PARTY_WAVE_PEER_NOTIFY"
	dungeonHellPartyWavePeerDropsEnv  = "DNFBRIDGE_HELL_PARTY_WAVE_PEER_DROPS"
	dungeonHellPartyPillarPeerEnv     = "DNFBRIDGE_HELL_PARTY_PILLAR_PEER_DESTROY"
	// abyssPartyDropOwnerEnv / abyssPartyDropOwnerMode（掉落归属档位 + 队伍实例共享账本的
	// 接线）见 `dungeon_abyss_party_drop_ledger.go`。
)

const (
	dungeonHellPartyWavePeerNotifyLegacyValue  = "legacy"
	dungeonHellPartyWavePeerDropsStrippedValue = "stripped"
	dungeonHellPartyPillarPeerLegacyValue      = "legacy"
)

type dungeonHellPartyWavePeerNotifyMode int

const (
	dungeonHellPartyWavePeerNotifyOn dungeonHellPartyWavePeerNotifyMode = iota
	dungeonHellPartyWavePeerNotifyLegacy
)

// readDungeonHellPartyWavePeerNotifyMode 只有显式 `legacy` 才回退。
func readDungeonHellPartyWavePeerNotifyMode(value string) dungeonHellPartyWavePeerNotifyMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonHellPartyWavePeerNotifyLegacyValue) {
		return dungeonHellPartyWavePeerNotifyLegacy
	}
	return dungeonHellPartyWavePeerNotifyOn
}

func (mode dungeonHellPartyWavePeerNotifyMode) String() string {
	if mode == dungeonHellPartyWavePeerNotifyLegacy {
		return dungeonHellPartyWavePeerNotifyLegacyValue
	}
	return "vendor"
}

// notifies 报告该档位是否把波次怪退役投影给队友。
func (mode dungeonHellPartyWavePeerNotifyMode) notifies() bool {
	return mode == dungeonHellPartyWavePeerNotifyOn
}

// dungeonHellPartyWavePeerNotifyModeFromEnv 每次用到时读一次 env。
func dungeonHellPartyWavePeerNotifyModeFromEnv() dungeonHellPartyWavePeerNotifyMode {
	return readDungeonHellPartyWavePeerNotifyMode(os.Getenv(dungeonHellPartyWavePeerNotifyEnv))
}

// dungeonHellPartyWavePeerDropsMode 决定队友那一份 op38 **带不带掉落行**。
type dungeonHellPartyWavePeerDropsMode int

const (
	// dungeonHellPartyWavePeerDropsShared 是默认档，也是修复档（零值 = 修复）：
	// 与击杀方逐字节同一份 body（含掉落行）⇒ 两名成员看到同一批地面掉落。
	dungeonHellPartyWavePeerDropsShared dungeonHellPartyWavePeerDropsMode = iota
	// dungeonHellPartyWavePeerDropsStripped 是上一轮那版：7 字节、无掉落行。
	dungeonHellPartyWavePeerDropsStripped
)

// readDungeonHellPartyWavePeerDropsMode 只有显式 `stripped` 才回退到上一轮那版。
func readDungeonHellPartyWavePeerDropsMode(value string) dungeonHellPartyWavePeerDropsMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonHellPartyWavePeerDropsStrippedValue) {
		return dungeonHellPartyWavePeerDropsStripped
	}
	return dungeonHellPartyWavePeerDropsShared
}

func (mode dungeonHellPartyWavePeerDropsMode) String() string {
	if mode == dungeonHellPartyWavePeerDropsStripped {
		return dungeonHellPartyWavePeerDropsStrippedValue
	}
	return "shared"
}

// shares 报告该档位是否把掉落行一起投影给队友。
func (mode dungeonHellPartyWavePeerDropsMode) shares() bool {
	return mode == dungeonHellPartyWavePeerDropsShared
}

func dungeonHellPartyWavePeerDropsModeFromEnv() dungeonHellPartyWavePeerDropsMode {
	return readDungeonHellPartyWavePeerDropsMode(os.Getenv(dungeonHellPartyWavePeerDropsEnv))
}

// dungeonHellPartyPillarPeerDestroyMode 决定柱子的破坏要不要投影给队友。
type dungeonHellPartyPillarPeerDestroyMode int

const (
	// dungeonHellPartyPillarPeerDestroyOn 是默认档，也是修复档（零值 = 修复）。
	dungeonHellPartyPillarPeerDestroyOn dungeonHellPartyPillarPeerDestroyMode = iota
	// dungeonHellPartyPillarPeerDestroyLegacy 是今天的线上行为：不投影。
	dungeonHellPartyPillarPeerDestroyLegacy
)

func readDungeonHellPartyPillarPeerDestroyMode(value string) dungeonHellPartyPillarPeerDestroyMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonHellPartyPillarPeerLegacyValue) {
		return dungeonHellPartyPillarPeerDestroyLegacy
	}
	return dungeonHellPartyPillarPeerDestroyOn
}

func (mode dungeonHellPartyPillarPeerDestroyMode) String() string {
	if mode == dungeonHellPartyPillarPeerDestroyLegacy {
		return dungeonHellPartyPillarPeerLegacyValue
	}
	return "vendor"
}

func (mode dungeonHellPartyPillarPeerDestroyMode) destroys() bool {
	return mode == dungeonHellPartyPillarPeerDestroyOn
}

func dungeonHellPartyPillarPeerDestroyModeFromEnv() dungeonHellPartyPillarPeerDestroyMode {
	return readDungeonHellPartyPillarPeerDestroyMode(os.Getenv(dungeonHellPartyPillarPeerEnv))
}

// notifyCurrentHellPartyWaveRetirementToPartyPeersLocked 把"这一只波次怪已权威退役"投影给
// 同一队伍副本实例内的其它成员会话，返回投递成功数。
//
// 只在**波次怪退役**这条路径上调用（`dungeon_combat_runtime.go` 的
// `RetireSpecialMonsterActor` 分支）；单人/无实例/回退档都是 0 字节副作用。
// 调用方持有击杀方会话的 `session.dungeon.mu`（本函数不取任何队友会话锁）。
//
// `drops` 是**击杀方那一条 op38 上真正的掉落行**（概率那一支 + 保底碎片）：
// 默认档把它们原样投影给队友（两名成员看到同一批地面对象）；`stripped` 档丢弃它们
// （逐字节复现上一轮那版）。
func (s *Service) notifyCurrentHellPartyWaveRetirementToPartyPeersLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	objectKey uint32,
	responseKind currentDungeonDeathResponseKind,
	drops []currentDungeonDeathDropWire,
) int {
	if s == nil || session == nil || runtime == nil || runtime.partyInstance == nil {
		return 0
	}
	mode := dungeonHellPartyWavePeerNotifyModeFromEnv()
	if !mode.notifies() {
		return 0
	}
	if objectKey == 0 || objectKey > uint32(^uint16(0)) {
		return 0
	}
	dropsMode := dungeonHellPartyWavePeerDropsModeFromEnv()
	ownerMode := abyssPartyDropOwnerModeFromEnv()
	projected := drops
	if !dropsMode.shares() {
		projected = nil
	}
	peers := s.partyDungeonInstancePeers(runtime, session.selectedCharacterID)
	delivered := 0
	for _, peer := range peers {
		if peer.Session == nil {
			continue
		}
		peerDrops := projected
		restamped := false
		switch {
		case ownerMode.perMember():
			// **独立掉落**（默认，= 厂商与新操作者口径）：每人只收到**属于自己的那几行**
			// （厂商 `DungeonKillApplicationService.cs:1399-1406` 也是"每人只发自己那一份"）。
			// 这样客户端不会留下"别人捡走了、我这儿还在"的地面残影。
			peerDrops = dropWiresOwnedBy(projected, currentSceneActorObjectKey(peer.CharacterID))
		case len(projected) != 0 && ownerMode.receiverOwned():
			// 第 4 次发版：一份掉落 + 行内所有者改写成收件人（谁先谁得）。
			peerDrops = restampDungeonDeathDropWireOwner(projected, currentSceneActorObjectKey(peer.CharacterID))
			restamped = true
		}
		body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(objectKey, responseKind, peerDrops)
		if err != nil {
			s.logGameEvent(session, "game-dungeon-hell-party-wave-peer-notify-failed",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"object_key", objectKey,
				"instance_id", runtime.partyInstance.id,
				"peer_char_id", peer.CharacterID,
				"reason", "wave_retirement_body_build_failed",
				"switch", mode.String(),
				"error", err)
			continue
		}
		if err := s.sendGameUpperRawClass(
			peer.Session,
			uint16(dnfenum.CmdPacketNotifyDieMonster),
			append([]byte(nil), body...),
			0,
		); err != nil {
			s.logGameEvent(session, "game-dungeon-hell-party-wave-peer-notify-failed",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"object_key", objectKey,
				"instance_id", runtime.partyInstance.id,
				"peer_char_id", peer.CharacterID,
				"reason", "peer_send_failed",
				"switch", mode.String(),
				"error", err)
			continue
		}
		delivered++
		// 这一行是"下一次实测一眼可判"的那一条：队友那一侧至少拿到了同一份权威死亡通知，
		// 以及**这一份带了几条掉落行**（`drop_rows_shared=true` 就是两名成员看到同一批地面对象）
		// 与**掉落行的所有者写成了谁**（`drop_owner_restamped=true` = 写成了收件人自己）。
		s.logGameEvent(peer.Session, "game-dungeon-hell-party-wave-peer-notify",
			"source", "op39_authoritative_special_monster_retirement",
			"instance_id", runtime.partyInstance.id,
			"object_key", objectKey,
			"from_char_id", session.selectedCharacterID,
			"to_char_id", peer.CharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"response_opcode", uint16(dnfenum.CmdPacketNotifyDieMonster),
			"body_len", len(body),
			"drop_rows", len(peerDrops),
			"drop_rows_shared", dropsMode.shares(),
			"drop_rows_stripped", len(peerDrops) == 0,
			"drops_switch", dropsMode.String(),
			"drops_env", dungeonHellPartyWavePeerDropsEnv,
			"drop_owner_restamped", restamped,
			"owner_actor_object_key", currentSceneActorObjectKey(peer.CharacterID),
			"owner_switch", ownerMode.String(),
			"owner_env", abyssPartyDropOwnerEnv,
			"switch", mode.String(),
			"response", "current_exe_op38_special_monster_retirement_with_the_killers_drop_rows")
	}
	return delivered
}

// restampDungeonDeathDropWireOwner 复制一份掉落行并把所有者改成给定 actor key
// （**不改调用方的切片**：击杀方那一份必须逐字节不变）。
func restampDungeonDeathDropWireOwner(
	drops []currentDungeonDeathDropWire,
	ownerActorObjectKey uint16,
) []currentDungeonDeathDropWire {
	if len(drops) == 0 {
		return nil
	}
	out := make([]currentDungeonDeathDropWire, 0, len(drops))
	for _, drop := range drops {
		restamped := drop
		restamped.OwnerActorObjectKey = ownerActorObjectKey
		out = append(out, restamped)
	}
	return out
}

// projectCurrentHellPartyPillarDestructionToPartyPeersLocked 把**深渊柱子**
// （房图 `[special passive object]` 里唯一带行内 `[hellparty]` 的那一行）的破坏投影给
// 同一队伍副本实例内的其它会话。
//
// 触发点 = 深渊房间**第一只**波次怪权威退役那一刻（这是服务端能观察到的最早的
// "深渊房间已经开打"的时刻；柱子本体的破坏客户端从不上报，见文件头）。每个会话在每个
// 房间只投一次；单人/无实例/回退档/非深渊房间都是 0 字节副作用。
//
// 返回投递成功数。
func (s *Service) projectCurrentHellPartyPillarDestructionToPartyPeersLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	responseKind currentDungeonDeathResponseKind,
) int {
	if s == nil || session == nil || runtime == nil || runtime.partyInstance == nil ||
		runtime.Room == nil {
		return 0
	}
	mode := dungeonHellPartyPillarPeerDestroyModeFromEnv()
	if !mode.destroys() {
		return 0
	}
	// 与波次怪退役投影**同一个回退约定**：`DNFBRIDGE_HELL_PARTY_WAVE_PEER_NOTIFY=legacy`
	// 的既有契约是"一条投影都不发、队友只会看到白色残影（逐字节等于上一轮修复前）"，
	// 柱子这一帧也是发给队友的投影，因此从属于它。
	if !dungeonHellPartyWavePeerNotifyModeFromEnv().notifies() {
		return 0
	}
	if !currentHellPartySceneIsHellRoom(runtime, scene) {
		return 0
	}
	// 触发点 = 本房间的波次行**全部退役**那一刻（服务端能观察到的最晚、也是最干净的
	// "这间深渊房打完了"的时刻：与 `game-dungeon-hell-party-clear-decision` 的
	// `wave_remaining=0` 同一条判据）。柱子本体的破坏客户端从不上报，服务端只能在这里
	// 把"队长那一侧早就打碎的柱子"补给队员。
	state := currentDungeonHellPartyClearState(runtime, scene)
	if state.WaveTotal == 0 || state.WaveRemaining != 0 {
		return 0
	}
	// 柱子的身份**只**从场景的 `[special passive object]` 行取（纯函数，不看
	// `DNFBRIDGE_MAP_OWNED_PASSIVE_OBJECT`）：那条开关管的是"要不要接住客户端的破坏
	// 事件"，与"柱子是哪一根"无关；绑在一起会让操作者关掉那条开关时这里静默失效。
	registration := &currentDungeonMapOwnedPassiveObjectRegistration{
		Objects: currentDungeonMapOwnedPassiveObjects(scene),
	}
	pillar, found := registration.hellPillarObject()
	if !found || pillar.ObjectCode <= 0 || pillar.ObjectCode > int64(^uint16(0)) {
		return 0
	}
	roomKey := fmt.Sprintf("%d:%s", scene.Map.Map.ID, scene.Coordinate.String())
	if runtime.hellPillarPeerDestroyKey == roomKey {
		return 0
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
		uint32(pillar.ObjectCode),
		responseKind,
		nil,
	)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-hell-party-pillar-peer-destroy-failed",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_index", pillar.ObjectIndex,
			"object_id", pillar.ObjectCode,
			"role", "killer_side",
			"reason", "pillar_destroy_body_build_failed",
			"switch", mode.String(),
			"error", err)
		return 0
	}
	peers := s.partyDungeonInstancePeers(runtime, session.selectedCharacterID)
	delivered := 0
	for _, peer := range peers {
		if peer.Session == nil {
			continue
		}
		if err := s.sendGameUpperRawClass(
			peer.Session,
			uint16(dnfenum.CmdPacketNotifyDieMonster),
			append([]byte(nil), body...),
			0,
		); err != nil {
			s.logGameEvent(session, "game-dungeon-hell-party-pillar-peer-destroy-failed",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"object_index", pillar.ObjectIndex,
				"object_id", pillar.ObjectCode,
				"peer_char_id", peer.CharacterID,
				"instance_id", runtime.partyInstance.id,
				"role", "killer_side",
				"reason", "peer_send_failed",
				"switch", mode.String(),
				"error", err)
			continue
		}
		delivered++
		// 这一行是判读柱子投影的那一条：`actor` = map-owned 柱子、`destroy_state` = 已破坏、
		// `received_by` = 拿到这一帧的队友。
		s.logGameEvent(peer.Session, "game-dungeon-hell-party-pillar-peer-destroy",
			"actor", "map_owned_special_passive_object_with_inline_hellparty",
			"actor_source", "dungeon_passive_object_drop.hellPillarObject",
			"destroy_state", "destroyed_on_the_killer_client_projected_to_the_peer",
			"received_by", peer.CharacterID,
			"from_char_id", session.selectedCharacterID,
			"instance_id", runtime.partyInstance.id,
			"object_index", pillar.ObjectIndex,
			"object_id", pillar.ObjectCode,
			"object_key", uint32(pillar.ObjectCode),
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"response_opcode", uint16(dnfenum.CmdPacketNotifyDieMonster),
			"body_len", len(body),
			"drop_rows", 0,
			"trigger", "all_hell_party_wave_monsters_retired_in_this_room",
			"switch", mode.String(),
			"env", dungeonHellPartyPillarPeerEnv,
			"response", "current_exe_op38_map_owned_object_code_no_drop_rows")
	}
	if delivered > 0 {
		runtime.hellPillarPeerDestroyKey = roomKey
	}
	return delivered
}
