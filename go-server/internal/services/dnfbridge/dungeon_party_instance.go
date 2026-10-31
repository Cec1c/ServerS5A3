package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 阶段二：队伍级"共享副本实例"。
//
// 现状（阶段一取证，见 dungeon_party_copresence.go）：`gameSession.dungeon.runtime`
// 是 **per-session** 的，组队进图只保证"解析出的 dungeon/maze/room/map/seed 相同"，
// 两人各一份运行时 ⇒ 各打各的怪/掉落/房间状态。阶段一只补了"互相看得见"。
//
// 参考实现（只读）：`ServerS4A21-master\Server\DfoServer\Game\Dungeon\DungeonIdentity.cs`
// 的 `PartyDungeonInstanceId` + `DungeonInstanceRegistry.cs`（按 partyId 注册、
// `DungeonInstance.PartyMemberCount`、全员离开/超时后 `TryBeginEnding/TryMarkEnded`）。
// 本文件按同一形状做**队伍级实例注册表**，判据直接复用阶段一已有的
// `dungeonPartyRunKey`（队伍 + 副本 + 迷宫 + 房间 + 地图 + 种子），不另造一套。
//
// 共享的内容与线程模型：
//   - **房间（怪物/扩展 actor/房间清怪状态）**：`runtimeDungeonRoom` 自带 `sync.RWMutex`，
//     直接共享同一个对象即可安全并发（怪物列表、objectKey 索引、死亡/跳过状态都在它里面）。
//   - **掉落账本**：`runtimeDungeonDropOwner` 没有自己的锁，所有改写在既有代码里都发生在
//     `session.dungeon.mu` 之下。共享后两个会话用的是**两把不同的会话锁**，所以共享账本的
//     读改写统一在外面再套一层 `dungeonPartyInstance.mu`（`lockPartyDungeonInstance` /
//     `unlockPartyDungeonInstance`）。锁序固定为 session.dungeon.mu → instance.mu。
//   - **objectKey 分配**：掉落/丢弃物用 `runtime.NextObjectKey` 顺序发号。共享实例下改成
//     实例内的共享计数器（`dungeonNextObjectKey` / `commitDungeonNextObjectKey`），
//     避免两个会话发出同一个 object key（`registerBatch` 会因冲突整批拒绝）。
//   - **已访问房间**：实例记录 `(房间 → 共享 room 对象)`；后进图的成员在接入时把这些房间
//     复制进自己的 `runtime.Rooms`（`runtime.Rooms` 这张 map 仍是 per-session，
//     避免两个会话在不同锁下写同一张 map；`DropRNG` 也就自然保持 per-session）。
//
// 回收时机（写清口径）：
//  1. **全员脱离**（出图回城 / 阵亡回城 / 换角色 / 断线清场，即会话侧 `dungeon.runtime = nil`
//     的每一处）→ 该成员从实例里摘除；摘到 0 人时**立即**从注册表移除（`recycled reason=last_member_left`）。
//  2. **队伍解散**（`publishRuntimePartyMembershipSnapshot` 里队伍缩到 1 人/解散的收口点）
//     → 立刻回收该队伍的全部实例（`recycled reason=party_disbanded`）。
//  3. **进图时顺带清扫**：成员长时间没有活动（`currentPartyDungeonInstanceIdleTTL`）或注册表
//     超过 `currentPartyDungeonInstanceLimit` 时，按 `lastSeenAt` 从旧到新回收
//     （`recycled reason=idle_ttl` / `capacity`）。这是兜底，防止异常路径（进程内残留引用）
//     把实例永久留在内存里。
//
// 单人/无队伍不进注册表：`adoptPartyDungeonInstance` 在判据不足时直接返回 nil，
// 原有 per-session 路径（含全部既有用例）完全不变。
const (
	// currentPartyDungeonInstanceLimit 是注册表的内存上限：同时存在的队伍副本实例数。
	// 满员时按 lastSeenAt 淘汰最旧的一个（对应参考实现的 registry 容量思路）。
	currentPartyDungeonInstanceLimit = 32
	// currentPartyDungeonInstanceIdleTTL 是"成员还挂着但很久没动"的兜底回收时限。
	currentPartyDungeonInstanceIdleTTL = 30 * time.Minute
)

// partyDungeonActorPosition 是实例里记着的"某个成员最近一次上报的副本坐标"（客户端 op35）。
// 队友 actor 投影需要它：只补 mode0/mode1/op9（+房间落位）时客户端把队友放在默认位置，
// 屏幕上就"看不见"；城镇那条已验证路径靠 op22/op23 把坐标提交进去。
type partyDungeonActorPosition struct {
	X         uint16
	Y         uint16
	Direction byte
	Valid     bool
}

// partyDungeonDoorArrival 是"某个成员在副本里走到门口、客户端发出过图请求(op45)"那一刻的
// 证据：源房间、目标房间、以及**该成员在源房间门口的位置**。
//
// 为什么这条数据是可信的（不猜偏移）：
//   - 偏移由本仓库既有解码器给出（`dungeoncmd.DecodeMoveMapRequest`：`NextX=body[0]`、
//     `NextY=body[1]`、`PositionX=u32LE body[2:6]`、`PositionY=u32LE body[6:10]`、
//     `MoveKind=body[10]`，注释写明"written by the current EXE sub_17C9D80"）；
//   - 现场逐字节复核（packet_log.20260925-043259.txt，RECV op45 body 100B）：
//     `01 01 cf040000 dd000000 00 …` ⇒ NextX=1/NextY=1 与同一秒的
//     `game-dungeon-move-committed source=0:1 target=1:1` 对上，PositionX=0x4cf=1231 /
//     PositionY=0xdd=221。
//
// 这是当前客户端在副本里**唯一**带已证实坐标字段的上报（副本内没有 op35/op36，见
// dungeon_party_peer_position.go 的取证注释），因此它同时是"门规则（3 取 2 / 4 取 3）"
// 唯一可用的"就位选票"真源。本结构只记录、只记日志，不改变任何放行/拒绝判定。
type partyDungeonDoorArrival struct {
	CharacterID uint16
	X           uint32
	Y           uint32
	MoveKind    byte
	TargetX     int64
	TargetY     int64
}

// partyDungeonDoorArrivalKey 按 (源房间, 目标房间) 归并选票：同一对房间里的成员各算一票。
type partyDungeonDoorArrivalKey struct {
	Source runtimeDungeonRoomKey
	Target runtimeDungeonRoomKey
}

// dungeonPartyInstance 是一个队伍级副本实例：同一队伍解析到同一个实例键时共用它。
// 字段含义：room 是"当前房间"的共享对象（怪物/扩展 actor/清怪状态都挂在它上面）；
// dropOwner 是共享掉落账本（读改写由 mu 保护）；nextObjectKey 是共享 objectKey 发号器；
// rooms 记录本实例访问过的房间（坐标/地图 → 共享 room 对象 + 完整场景）；
// scene 是最近一次进房间的成员场景，后进图的队友按它做同场校验；
// sessions/scenes/positions 是成员各自的会话、副本会话与最近上报坐标（广播权威状态与
// 队友 actor 投影时用；不借它去锁队友的 dungeon.mu）；
// doorArrivals 是"门口就位"选票（op45 证据，只读判读用）；
// cardFlipSlots/cardFlipKnown/cardFlipOwner 是**翻牌结果的队伍级账本**（操作者缺陷
// 「双人组队正常通关后翻牌奖励双方都看不到对方翻的牌」）：op70/op71 的线格式本来就是
// 每名成员一槽（`dungeonCardSlotsPerSide` 槽 × StateA=免费行/StateB=付费行），所以
// "队友翻开的牌"必须由服务端投影进**每个成员**的 op71 里。账本随实例存活与回收，
// 读改写与其它实例内共享状态同锁序（session.dungeon.mu → instance.mu）。
// 见 dungeon_party_card_flip_projection.go。
type dungeonPartyInstance struct {
	guildRankingRun           *currentGuildRankingRun
	guildContributionRun      *currentGuildContributionRun
	ridableTerminals          *currentDungeonRidableTerminalOwner
	ridableSelectionSignature [32]byte

	semesterRun     *currentSemesterRun
	mu              sync.Mutex
	id              uint64
	key             dungeonPartyRunKey
	createdAt       time.Time
	entryPartyCount int
	lastSeenAt      time.Time
	members         map[uint16]time.Time
	sessions        map[uint16]*gameSession
	// Exact runtime attachment owns membership retirement. Character IDs can
	// already belong to a replacement socket/run when old cleanup arrives.
	memberRuntimes         map[uint16]*runtimeDungeonState
	scenes                 map[uint16]*worldmap.DungeonSession
	positions              map[uint16]partyDungeonActorPosition
	doorArrivals           map[partyDungeonDoorArrivalKey]map[uint16]partyDungeonDoorArrival
	deadMembers            map[uint16]bool
	cardFlipSlots          [dungeonCardSlotsPerSide]currentDungeonOp71Slot
	cardFlipKnown          [dungeonCardSideCount][dungeonCardSlotsPerSide]bool
	cardFlipOwner          [dungeonCardSideCount][dungeonCardSlotsPerSide]uint16
	cardReservations       [dungeonCardSideCount][dungeonCardSlotsPerSide]uint16
	blackDiamondCards      map[uint16]dungeonCardRewardBundle
	blackDiamondCardOwners [dungeonCardSlotsPerSide]uint16
	room                   *runtimeDungeonRoom
	dropOwner              *runtimeDungeonDropOwner
	nextObjectKey          uint32
	rooms                  map[runtimeDungeonRoomKey]partyDungeonSharedRoom
	scene                  worldmap.DungeonRoomScene
}

// partyDungeonSharedRoom 是实例内一个房间的共享内容：房间对象（怪物/清怪状态）
// 加上它的完整场景，供后进图的成员把该房间并进自己的 `runtime.Rooms`。
type partyDungeonSharedRoom struct {
	Room  *runtimeDungeonRoom
	Scene worldmap.DungeonRoomScene
}

// partyDungeonInstanceRegistry 按实例键注册队伍副本实例。
type partyDungeonInstanceRegistry struct {
	mu     sync.Mutex
	nextID uint64
	byKey  map[dungeonPartyRunKey]*dungeonPartyInstance
	now    func() time.Time
}

func newPartyDungeonInstanceRegistry() *partyDungeonInstanceRegistry {
	return &partyDungeonInstanceRegistry{
		byKey: make(map[dungeonPartyRunKey]*dungeonPartyInstance),
		now:   time.Now,
	}
}

func (registry *partyDungeonInstanceRegistry) clock() time.Time {
	if registry != nil && registry.now != nil {
		return registry.now()
	}
	return time.Now()
}

// partyDungeonInstanceRegistryForService 懒建注册表：很多协议用例直接构造 Service。
func (s *Service) partyDungeonInstanceRegistryForService() *partyDungeonInstanceRegistry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.partyDungeons == nil {
		s.partyDungeons = newPartyDungeonInstanceRegistry()
	}
	return s.partyDungeons
}

// lockPartyDungeonInstance / unlockPartyDungeonInstance 保护共享账本与共享发号器。
// 只能包住"共享状态的读改写"，不要在这里面回调会话事件循环。
func (runtime *runtimeDungeonState) lockPartyDungeonInstance() *dungeonPartyInstance {
	if runtime == nil || runtime.partyInstance == nil {
		return nil
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	return instance
}

func unlockPartyDungeonInstance(instance *dungeonPartyInstance) {
	if instance == nil {
		return
	}
	instance.mu.Unlock()
}

// adoptPartyDungeonInstance 把刚构建/提交的 per-session 运行时接入队伍级实例。
//
// 返回 nil 的三种情况（全部保持原有 per-session 行为）：
//   - 单人、无队伍或队伍不足 2 人（`dungeonPartyRunKeyFrom` 的既有判据）；
//   - 注册表不可用；
//   - 已有实例的当前房间与本次运行时的房间不是同一个坐标/地图（fail-closed，
//     宁可各打各的，也不把两份不同房间的怪物状态混在一起）。
//
// 第一个落位的成员把自己的房间/掉落账本/发号器**交给实例**（成为共享对象）；
// 后落位的成员改为**指向**这些共享对象，并继承已访问房间清单。
func (s *Service) adoptPartyDungeonInstance(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
) *dungeonPartyInstance {
	if s == nil || session == nil || runtime == nil || runtime.Room == nil {
		return nil
	}
	// fail-closed：只有"运行时房间 == 会话当前场景"时才允许把这份运行时接进共享实例，
	// 否则宁可按 per-session 跑，也不把两份不同房间的怪物状态混在一起。
	roomSnapshot := runtime.Room.Snapshot()
	if roomSnapshot.Coordinate != scene.Coordinate || roomSnapshot.MapID != scene.Map.Map.ID {
		return nil
	}
	key, ok := dungeonPartyRunKeyFrom(session, runtime, scene)
	if !ok {
		return nil
	}
	registry := s.partyDungeonInstanceRegistryForService()
	if registry == nil {
		return nil
	}
	characterID := session.selectedCharacterID
	now := registry.clock()

	var created bool
	var joined bool
	registry.mu.Lock()
	if registry.byKey == nil {
		registry.byKey = make(map[dungeonPartyRunKey]*dungeonPartyInstance)
	}
	s.prunePartyDungeonInstancesLocked(registry, now, key)
	instance := registry.byKey[key]
	if instance == nil {
		registry.nextID++
		instance = &dungeonPartyInstance{
			id:              registry.nextID,
			key:             key,
			createdAt:       now,
			entryPartyCount: runtime.entryPartyCount,
			lastSeenAt:      now,
			members:         map[uint16]time.Time{characterID: now},
			sessions:        map[uint16]*gameSession{characterID: session},
			memberRuntimes:  map[uint16]*runtimeDungeonState{characterID: runtime},
			scenes:          map[uint16]*worldmap.DungeonSession{},
			room:            runtime.Room,
			dropOwner:       runtime.DropOwner,
			nextObjectKey:   runtime.NextObjectKey,
			rooms:           map[runtimeDungeonRoomKey]partyDungeonSharedRoom{},
			scene:           scene,
		}
		if runtime.Session != nil {
			instance.scenes[characterID] = runtime.Session
		}
		if runtime.randomizedObjects != nil {
			instance.ridableTerminals = runtime.randomizedObjects.ridableTerminals
			instance.ridableSelectionSignature = runtime.randomizedObjects.entrySignature
		}
		instance.rooms[runtimeDungeonRoomKeyFromScene(scene)] = partyDungeonSharedRoom{Room: runtime.Room, Scene: scene}
		registry.byKey[key] = instance
		created = true
	} else {
		instance.mu.Lock()
		if instance.acceptsRuntimeLocked(runtime, scene) {
			instance.members[characterID] = now
			instance.lastSeenAt = now
			if instance.sessions == nil {
				instance.sessions = make(map[uint16]*gameSession, 4)
			}
			if instance.scenes == nil {
				instance.scenes = make(map[uint16]*worldmap.DungeonSession, 4)
			}
			instance.sessions[characterID] = session
			if instance.memberRuntimes == nil {
				instance.memberRuntimes = make(map[uint16]*runtimeDungeonState, 4)
			}
			instance.memberRuntimes[characterID] = runtime
			if runtime.Session != nil {
				instance.scenes[characterID] = runtime.Session
			}
			joined = true
		}
		instance.mu.Unlock()
	}
	registry.mu.Unlock()

	switch {
	case created:
		runtime.adoptPartyDungeonInstance(instance, scene)
		s.logGameEvent(session, "game-dungeon-party-instance-created",
			"instance_id", instance.id,
			"party_id", key.PartyID,
			"dungeon_id", key.DungeonID,
			"maze_index", key.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", key.MapID,
			"seed", key.Seed,
			"char_id", characterID,
			"monsters", runtime.Room.MonsterCount(),
			"reason", "first_party_member_published_shared_room_and_drop_ledger")
		return instance
	case joined:
		runtime.adoptPartyDungeonInstance(instance, scene)
		s.logGameEvent(session, "game-dungeon-party-instance-joined",
			"instance_id", instance.id,
			"party_id", key.PartyID,
			"dungeon_id", key.DungeonID,
			"maze_index", key.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", key.MapID,
			"seed", key.Seed,
			"char_id", characterID,
			"member_count", instance.memberCount(),
			"monsters", instance.room.MonsterCount(),
			"shared_rooms", instance.roomCount(),
			"reason", "same_party_same_run_shares_one_instance")
		return instance
	}
	s.logGameEvent(session, "game-dungeon-party-instance-declined",
		"party_id", key.PartyID,
		"dungeon_id", key.DungeonID,
		"room", scene.Coordinate.String(),
		"map_id", key.MapID,
		"seed", key.Seed,
		"char_id", characterID,
		"reason", "existing_instance_room_mismatch")
	return nil
}

// adoptPartyDungeonInstance 让运行时指向实例的共享对象（房间 / 掉落账本 / 发号器），
// 并把实例已经访问过的房间并进本会话的 `Rooms`（map 本身仍 per-session）。
// 房间按**坐标/地图**取共享对象：本次落位的房间如果队友已经进过，就直接用队友那份
// （怪物/清怪状态共享）；没进过则把本次的房间对象登记为共享。
func (runtime *runtimeDungeonState) adoptPartyDungeonInstance(instance *dungeonPartyInstance, scene worldmap.DungeonRoomScene) {
	if runtime == nil || instance == nil {
		return
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	runtime.partyInstance = instance
	runtime.entryPartyCount = instance.entryPartyCount
	if runtime.randomizedObjects != nil && runtime.randomizedObjects.entrySignature == instance.ridableSelectionSignature {
		runtime.randomizedObjects.ridableTerminals = instance.ridableTerminals
	}
	if shared, ok := instance.rooms[runtimeDungeonRoomKeyFromScene(scene)]; ok && shared.Room != nil {
		shared.Room.adoptAnnouncedSessionsFrom(runtime.Room)
		runtime.Room = shared.Room
	} else if runtime.Room != nil {
		instance.rooms[runtimeDungeonRoomKeyFromScene(scene)] = partyDungeonSharedRoom{Room: runtime.Room, Scene: scene}
	}
	runtime.DropOwner = instance.dropOwner
	if instance.nextObjectKey > runtime.NextObjectKey {
		runtime.NextObjectKey = instance.nextObjectKey
	}
	if runtime.Rooms == nil {
		runtime.Rooms = make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit)
	}
	for roomKey, shared := range instance.rooms {
		if shared.Room == nil {
			continue
		}
		if existing := runtime.Rooms[roomKey]; existing != nil && existing.Room == shared.Room {
			continue
		}
		runtime.Rooms[roomKey] = &runtimeDungeonRoomVisit{
			Scene:   shared.Scene,
			Room:    shared.Room,
			Seed:    runtime.Seed,
			DropRNG: runtime.Seed,
		}
	}
}

// acceptsRuntimeLocked 判断后到的运行时能不能并入这个实例：实例必须已经拥有
// "这个坐标/地图"的房间对象（即队伍确实进过同一间房），且双方房间的坐标/地图一致。
func (instance *dungeonPartyInstance) acceptsRuntimeLocked(runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) bool {
	if instance == nil || runtime == nil || runtime.Room == nil {
		return false
	}
	shared, ok := instance.rooms[runtimeDungeonRoomKeyFromScene(scene)]
	if !ok || shared.Room == nil {
		return false
	}
	snapshot := shared.Room.Snapshot()
	return snapshot.Coordinate == scene.Coordinate && snapshot.MapID == scene.Map.Map.ID
}

// partyDungeonPeer 是同一队伍副本实例内的另一个成员：它的会话 + 它自己的副本会话。
type partyDungeonPeer struct {
	CharacterID uint16
	Session     *gameSession
	Scene       *worldmap.DungeonSession
}

// partyDungeonInstancePeers 收集同一队伍副本实例内的其它成员。
// 只读实例注册信息，**不触碰队友的 session.dungeon.mu**：两个会话若互相等待对方的
// 事件循环/会话锁就会死锁（仓库既有取舍，见 dungeon_navigation_runtime.go 的异步转交）。
func (s *Service) partyDungeonInstancePeers(runtime *runtimeDungeonState, selfCharacterID uint16) []partyDungeonPeer {
	if s == nil || runtime == nil || runtime.partyInstance == nil {
		return nil
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	peers := make([]partyDungeonPeer, 0, len(instance.members))
	for characterID := range instance.members {
		if characterID == selfCharacterID {
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

// broadcastPartyDungeonInstanceDeathNotification 把一份**已提交的权威死亡通知**下发给同一
// 队伍副本实例内的其它会话，并把同一份权威清怪状态应用到队友自己的副本场景。
//
// 现场（操作者双开复测）：两人各自判定"我打死了这只怪"并各发 op39，先到者被接受、后到者被
// 拒绝且**没有任何下行**；同时先到者的死亡通知也从未广播 → 两边各留一只白色残影。
// 这里用客户端已验证的既有 op38 形状原样转发（不发明新包），并调用队友自己的
// `DungeonSession.MarkHostileDefeated`（内部自带锁），让房间清怪/阻挡怪按同一份权威状态走。
func (s *Service) broadcastPartyDungeonInstanceDeathNotification(
	session *gameSession,
	runtime *runtimeDungeonState,
	objectKey uint32,
	body []byte,
	source string,
) int {
	if s == nil || runtime == nil || len(body) == 0 {
		return 0
	}
	selfCharacterID := uint16(0)
	if session != nil {
		selfCharacterID = session.selectedCharacterID
	}
	peers := s.partyDungeonInstancePeers(runtime, selfCharacterID)
	delivered := 0
	for _, peer := range peers {
		if peer.Session == nil {
			continue
		}
		if err := s.sendGameUpperRawClass(
			peer.Session,
			uint16(dnfenum.CmdPacketNotifyDieMonster),
			partyDungeonDeathBodyForRecipient(runtime, body, currentSceneActorObjectKey(peer.CharacterID)),
			0,
		); err != nil {
			s.logGameEvent(session, "game-dungeon-party-instance-death-broadcast-failed",
				"peer_char_id", peer.CharacterID,
				"object_key", objectKey,
				"error", err)
			continue
		}
		delivered++
		s.dispatchQuestMaterialPeer(peer.Session)
		sceneCleared := false
		sceneMarked := false
		sceneMarkReason := "peer_scene_missing"
		if peer.Scene != nil && objectKey != 0 {
			cleared, markErr := peer.Scene.MarkHostileDefeated(objectKey)
			switch {
			case markErr == nil:
				sceneMarked = true
				sceneCleared = cleared
				sceneMarkReason = "marked"
			case errors.Is(markErr, worldmap.ErrHostileAlreadyDefeated):
				sceneMarked = true
				sceneMarkReason = "already_defeated"
			case errors.Is(markErr, worldmap.ErrHostileNotBound):
				// 队友那侧没有这个 object key：要么他还没宣告这间房（刚到图/在别的房间），
				// 要么两边的房间对象不是同一份（key 序列分叉，见 partyDungeonSharedRoom）。
				sceneMarkReason = "peer_scene_object_not_bound"
			default:
				sceneMarkReason = "peer_scene_mark_failed"
				s.logGameEvent(session, "game-dungeon-party-instance-death-broadcast-failed",
					"peer_char_id", peer.CharacterID,
					"object_key", objectKey,
					"reason", "peer_scene_mark_failed",
					"error", markErr)
			}
		}
		if !sceneMarked && sceneMarkReason == "peer_scene_object_not_bound" {
			s.logGameEvent(session, "game-dungeon-party-instance-death-broadcast-failed",
				"peer_char_id", peer.CharacterID,
				"object_key", objectKey,
				"reason", sceneMarkReason,
				"error", worldmap.ErrHostileNotBound)
		}
		if sceneCleared {
			// 队友的副本场景因为这份权威死亡被清空了：结算链只在"自己会话里被接受的 op39"
			// 上跑，所以必须替队友触发一次（否则只有击杀方会话会结算，队员通不了关）。
			s.dispatchPartyDungeonPeerFinalRoomCompletion(peer.Session, runtime.partyInstance.id, objectKey)
		}
		s.logGameEvent(peer.Session, "game-dungeon-party-instance-death-broadcast",
			"source", source,
			"instance_id", runtime.partyInstance.id,
			"object_key", objectKey,
			"from_char_id", selfCharacterID,
			"to_char_id", peer.CharacterID,
			"peer_scene_marked", sceneMarked,
			"peer_scene_mark_reason", sceneMarkReason,
			"peer_room_cleared", sceneCleared,
			"response_opcode", uint16(dnfenum.CmdPacketNotifyDieMonster),
			"body_len", len(body))
	}
	return delivered
}

// dispatchPartyDungeonPeerFinalRoomCompletion 在队友自己的事件循环里跑一次"最终房间完成"。
//
// 结算链（op34/op31/op35/op37 那一串）只在**自己会话里被接受的那次 op39** 上触发；共享实例下
// 最后一两只怪往往是队长打死的，队员那边只是被广播标记了清怪状态 ⇒ 队员永远结算不了。
// 这里异步投递（同步会跨会话互等：广播方持有自己的 dungeon.mu，队友的回调又要它的锁）。
func (s *Service) dispatchPartyDungeonPeerFinalRoomCompletion(peer *gameSession, instanceID uint64, objectKey uint32) {
	s.dispatchPartyDungeonPeerFinalRoomCompletionChecked(peer, instanceID, objectKey, nil)
}

// accept, when present, revalidates a scoped completion proof under the peer's
// dungeon lock. Ordinary hostile-death callers retain their existing path.
func (s *Service) dispatchPartyDungeonPeerFinalRoomCompletionChecked(peer *gameSession, instanceID uint64, objectKey uint32, accept func(*runtimeDungeonState, worldmap.DungeonRoomScene) bool) {
	if s == nil || peer == nil || instanceID == 0 {
		return
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(peer)
	if !bound {
		return
	}
	run := func() error {
		peer.dungeon.mu.Lock()
		defer peer.dungeon.mu.Unlock()
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		runtime := peer.dungeon.runtime
		if runtime == nil || runtime.Session == nil || runtime.partyInstance == nil ||
			runtime.partyInstance.id != instanceID {
			return nil
		}
		scene, ok := runtime.Session.Scene()
		if !ok || !scene.Cleared {
			return nil
		}
		if accept != nil && !accept(runtime, scene) {
			return nil
		}
		if err := s.persistCurrentDungeonRoomClearQuestLocked(peer, runtime); err != nil {
			return err
		}
		s.awardCurrentPetRoomClearLocked(peer, runtime, scene, "authoritative_peer_room_clear")
		completionErr := s.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(peer, runtime, scene, objectKey)
		if runtime.bossDieCheckAccepted && !runtime.settlementEntrySent {
			// A transient member-session write can fail after the final room and
			// boss-die completion state were committed but before bodyless op31
			// reached the client. The ordinary entry now returns early because
			// bossDieCheckAccepted is set, so retry through that same settlement
			// owner once. Identity, persistence, tournament and reward barriers
			// remain owned by completeCurrentDungeonBossDieCheckLocked.
			resumeErr := s.completeCurrentDungeonBossDieCheckLocked(
				peer,
				runtime,
				"authoritative_party_death_dispatch_resume_partial_settlement",
			)
			if resumeErr != nil {
				return errors.Join(completionErr, resumeErr)
			}
		} else if completionErr != nil {
			return completionErr
		}
		if err := s.completePVFTutorialBosslessFinalRoomAfterDeathLocked(peer, runtime, scene); err != nil {
			return err
		}
		s.logGameEvent(peer, "game-dungeon-party-instance-peer-final-room-completed",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"object_key", objectKey,
			"instance_id", instanceID,
			"reason", "authoritative_peer_death_cleared_this_session_scene")
		return nil
	}
	if peer.events == nil || peer.events.loop == nil {
		// 没有事件循环（协议用例）：同步执行，结果确定。
		if err := run(); err != nil {
			s.logGameEvent(peer, "game-dungeon-party-instance-peer-final-room-deferred",
				"object_key", objectKey,
				"instance_id", instanceID,
				"error", err)
		}
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		if err := s.callGameSession(ctx, peer, "party-dungeon-peer-final-room-completion", run); err != nil {
			s.logGameEvent(peer, "game-dungeon-party-instance-peer-final-room-deferred",
				"object_key", objectKey,
				"instance_id", instanceID,
				"error", err)
		}
	}()
}

func (instance *dungeonPartyInstance) memberCount() int {
	if instance == nil {
		return 0
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return len(instance.members)
}

func (instance *dungeonPartyInstance) roomCount() int {
	if instance == nil {
		return 0
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return len(instance.rooms)
}

// recordPartyDungeonActorPosition 把本会话最近上报的副本坐标记进实例，供队友 actor 投影
// 提交坐标（客户端 op35 上报；没有它客户端只能把队友放在默认位置）。调用方需持有
// 本会话的 session.dungeon.mu（锁序 session.dungeon.mu → instance.mu）。
func (runtime *runtimeDungeonState) recordPartyDungeonActorPosition() {
	if runtime == nil || runtime.partyInstance == nil || !runtime.actorPositionValid {
		return
	}
	instance := runtime.partyInstance
	characterID := characterIDOfRuntimeDungeonState(runtime)
	if characterID == 0 {
		return
	}
	position := partyDungeonActorPosition{
		X:         runtime.actorPositionX,
		Y:         runtime.actorPositionY,
		Direction: runtime.actorDirection,
		Valid:     true,
	}
	instance.mu.Lock()
	if instance.positions == nil {
		instance.positions = make(map[uint16]partyDungeonActorPosition, 4)
	}
	instance.positions[characterID] = position
	instance.lastSeenAt = time.Now()
	instance.mu.Unlock()
}

// recordPartyDungeonDoorArrival 记一条"门口就位"选票，并返回
// (该 (源房间,目标房间) 里已就位的**不同成员数**, 实例当前成员数)。
// 调用方需持有本会话的 session.dungeon.mu（锁序 session.dungeon.mu → instance.mu）。
// 只写证据表，**不参与任何放行/拒绝判定**：门规则的实现口径等父代理确认。
func (runtime *runtimeDungeonState) recordPartyDungeonDoorArrival(
	characterID uint16,
	source runtimeDungeonRoomKey,
	target runtimeDungeonRoomKey,
	arrival partyDungeonDoorArrival,
) (int, int) {
	if runtime == nil || runtime.partyInstance == nil || characterID == 0 {
		return 0, 0
	}
	instance := runtime.partyInstance
	key := partyDungeonDoorArrivalKey{Source: source, Target: target}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.doorArrivals == nil {
		instance.doorArrivals = make(map[partyDungeonDoorArrivalKey]map[uint16]partyDungeonDoorArrival, 4)
	}
	votes := instance.doorArrivals[key]
	if votes == nil {
		votes = make(map[uint16]partyDungeonDoorArrival, 4)
		instance.doorArrivals[key] = votes
	}
	arrival.CharacterID = characterID
	arrival.TargetX = target.X
	arrival.TargetY = target.Y
	votes[characterID] = arrival
	instance.lastSeenAt = time.Now()
	return len(votes), len(instance.members)
}

// partyDungeonDoorArrivalQuorum 给出操作者期望的门规则阈值：3 人三取二、4 人四取三
// （2 人时只能全到）。**只用于日志判读**，本轮不据此放行任何过图（等父代理确认口径）。
func partyDungeonDoorArrivalQuorum(memberCount int) int {
	switch {
	case memberCount >= 4:
		return 3
	case memberCount == 3:
		return 2
	case memberCount == 2:
		return 2
	default:
		return 0
	}
}

// recordCurrentDungeonDoorArrivalLocked 把一条 op45 记成"门口就位"选票并记日志。
// 调用方需持有 session.dungeon.mu。**只记录、只记日志**：本轮不据此放行任何过图。
func (s *Service) recordCurrentDungeonDoorArrivalLocked(
	session *gameSession,
	source runtimeDungeonRoomKey,
	request dungeoncmd.MoveMapRequest,
) {
	if s == nil || session == nil || session.dungeon.runtime == nil {
		return
	}
	target := runtimeDungeonRoomKey{X: int64(request.NextX), Y: int64(request.NextY)}
	votes, members := session.dungeon.runtime.recordPartyDungeonDoorArrival(
		session.selectedCharacterID,
		source,
		target,
		partyDungeonDoorArrival{
			X:        request.PositionX,
			Y:        request.PositionY,
			MoveKind: request.MoveKind,
		},
	)
	s.logGameEvent(session, "game-dungeon-party-door-arrival",
		"char_id", session.selectedCharacterID,
		"source_room", dungeonRoomCoordinate(source.X, source.Y),
		"source_map_id", source.MapID,
		"target_room", dungeonRoomCoordinate(target.X, target.Y),
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"move_kind", request.MoveKind,
		"arrived_members", votes,
		"party_member_count", members,
		"quorum_expected", partyDungeonDoorArrivalQuorum(members),
		"quorum_rule", "operator_expectation_three_of_two_four_of_three",
		"enforced", false,
		"position_source", "current_exe_op45_sub_17C9D80_body_2_6_and_6_10_proven_offsets")
}

// partyDungeonActorPositionFor 取实例里某个成员最近上报的副本坐标。
func (runtime *runtimeDungeonState) partyDungeonActorPositionFor(characterID uint16) (partyDungeonActorPosition, bool) {
	if runtime == nil || runtime.partyInstance == nil || characterID == 0 {
		return partyDungeonActorPosition{}, false
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	position, ok := instance.positions[characterID]
	if !ok || !position.Valid {
		return partyDungeonActorPosition{}, false
	}
	return position, true
}

// characterIDOfRuntimeDungeonState 取这份运行时所属的角色（副本运行时的 Character 已加载）。
func characterIDOfRuntimeDungeonState(runtime *runtimeDungeonState) uint16 {
	if runtime == nil {
		return 0
	}
	return uint16(numericCharacterStat(runtime.Character.CharacterID))
}

// partyDungeonSharedRoom 查询实例里某个坐标/地图的共享房间（只读，不登记）。
//
// 队伍级共享实例下**同一坐标/地图的房间只能有一个对象**：两个会话各建一份会让
// object key 序列分叉（各自的 NextObjectKey 进度不同），权威死亡/清怪状态就落不到
// 对方场景里（现场：`peer_scene_marked=false`、
// `monster-death-blocked … runtime object is not found: object_key=418/420`）。
func (runtime *runtimeDungeonState) partyDungeonSharedRoom(scene worldmap.DungeonRoomScene) (*runtimeDungeonRoom, bool) {
	if runtime == nil || runtime.partyInstance == nil {
		return nil, false
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	shared, ok := instance.rooms[runtimeDungeonRoomKeyFromScene(scene)]
	if !ok || shared.Room == nil {
		return nil, false
	}
	return shared.Room, true
}

// registerPartyDungeonSharedRoom 把本会话建好的房间登记为实例里的共享房间（首个访客生效），
// 并立刻把共享 objectKey 计数器推到该房间用掉的最高位之后 —— 否则队友在**同一个房间被提交
// 之前**再来复用这份房间时，会拿到一个还没推进的计数器，发出的 key 会和房间里的怪撞车。
func (runtime *runtimeDungeonState) registerPartyDungeonSharedRoom(
	scene worldmap.DungeonRoomScene,
	room *runtimeDungeonRoom,
	nextObjectKey uint32,
) {
	if runtime == nil || runtime.partyInstance == nil || room == nil {
		return
	}
	instance := runtime.partyInstance
	key := runtimeDungeonRoomKeyFromScene(scene)
	instance.mu.Lock()
	if instance.rooms == nil {
		instance.rooms = make(map[runtimeDungeonRoomKey]partyDungeonSharedRoom)
	}
	if existing, ok := instance.rooms[key]; !ok || existing.Room == nil {
		instance.rooms[key] = partyDungeonSharedRoom{Room: room, Scene: scene}
		instance.lastSeenAt = time.Now()
	}
	instance.mu.Unlock()
	runtime.commitDungeonNextObjectKey(nextObjectKey)
}

// shareOrRegisterDungeonRoom 在进房间时把该房间并进共享实例：
// 队友已经进过同一坐标/地图时返回队友那份房间对象（本次会话改为使用它，
// 于是怪物/清怪状态共享）；否则把本次的房间对象登记为共享并原样返回。
// 单人/无队伍直接返回 runtime.Room，调用方行为不变。
func (runtime *runtimeDungeonState) shareOrRegisterDungeonRoom(scene worldmap.DungeonRoomScene) *runtimeDungeonRoom {
	if runtime == nil {
		return nil
	}
	if runtime.partyInstance == nil || runtime.Room == nil {
		return runtime.Room
	}
	if shared, ok := runtime.partyDungeonSharedRoom(scene); ok {
		// 房间对象换成队友那份时，把"本会话已经宣告过这间房"的登记并过去：
		// 宣告是会话维度的事实，不能因为它挂在即将被丢弃的本地房间对象上而丢掉。
		shared.adoptAnnouncedSessionsFrom(runtime.Room)
		runtime.commitDungeonNextObjectKey(runtime.Room.highObjectKey())
		return shared
	}
	runtime.registerPartyDungeonSharedRoom(scene, runtime.Room, runtime.Room.highObjectKey())
	return runtime.Room
}

// prepareRuntimeDungeonTargetRoom 建（或复用）目标房间，并完成扩展 actor 与教程
// basic-action 配置 —— 房间切换的三条路径（普通/剧情/分层）都从这里走。
//
// 复用队友那份房间时：扩展 actor 已经挂在房间里，**不能**再规划/附加一次；返回的
// objectKey 计数器取共享值（一定不小于该房间已用掉的 key）。
func (s *Service) prepareRuntimeDungeonTargetRoom(
	runtime *runtimeDungeonState,
	target worldmap.DungeonRoomScene,
	monsterCatalog *pvfDungeonMonsterCatalog,
	errPrefix string,
) (*runtimeDungeonRoom, uint32, error) {
	if shared, ok := runtime.partyDungeonSharedRoom(target); ok {
		return shared, runtime.dungeonNextObjectKey(), nil
	}
	targetRoom, nextObjectKey, err := newRuntimeDungeonRoom(target, monsterCatalog, runtime.dungeonNextObjectKey())
	if err != nil {
		return nil, 0, fmt.Errorf("%s monster owner: %w", errPrefix, err)
	}
	var aiCatalog *pvfDungeonAICharacterCatalog
	if len(target.AICharacters) != 0 {
		aiCatalog, err = s.dungeonAICharacterCatalog()
		if err != nil {
			return nil, 0, err
		}
	}
	extendedPlan, err := planRuntimeDungeonExtendedActors(
		target,
		monsterCatalog,
		aiCatalog,
		runtime.Dungeon.Metadata.BasisLevel,
		nextObjectKey,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("%s extended actor owner: %w", errPrefix, err)
	}
	// 深渊波次隐藏模板行（厂商 `DungeonMapHandler.cs:847-879`）：目标房间就是本次冻结的
	// 深渊房间时，把波次怪作为同一张 actor 表里的隐藏模板行追加进去。默认档生效、
	// `DNFBRIDGE_HELL_PARTY_WAVE_ROWS=legacy` 时 `runtime.HellPartyWaves` 恒为空 ⇒ 一个
	// 字节都不动。见 dungeon_hell_party_wave.go。
	if currentHellPartySceneIsHellRoom(runtime, target) {
		if err := s.appendCurrentHellPartyTemplateRows(runtime.HellPartyWaves, target, monsterCatalog, &extendedPlan); err != nil {
			return nil, 0, fmt.Errorf("%s hell party template rows: %w", errPrefix, err)
		}
	}
	if err := targetRoom.AttachExtendedActors(extendedPlan); err != nil {
		return nil, 0, fmt.Errorf("%s attach extended actor owner: %w", errPrefix, err)
	}
	if _, err := s.configurePVFTutorialBasicActionRoom(runtime.Dungeon, target, targetRoom); err != nil {
		return nil, 0, fmt.Errorf("%s tutorial basic-action room: %w", errPrefix, err)
	}
	if err := s.prepareCurrentGuildPracticeTargetRoom(runtime, targetRoom, target); err != nil {
		return nil, 0, fmt.Errorf("%s guild practice room: %w", errPrefix, err)
	}
	runtime.registerPartyDungeonSharedRoom(target, targetRoom, extendedPlan.NextObjectKey)
	return targetRoom, extendedPlan.NextObjectKey, nil
}

// dungeonNextObjectKey 读共享/本会话的 objectKey 发号器。
func (runtime *runtimeDungeonState) dungeonNextObjectKey() uint32 {
	if runtime == nil {
		return 0
	}
	instance := runtime.partyInstance
	if instance == nil {
		return runtime.NextObjectKey
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return instance.nextObjectKey
}

// commitDungeonNextObjectKey 提交发号器。共享实例下取 `max(共享值, next)`：
// 顺序提交（正常路径）等价于直接赋值；真并发时宁可让后一批 objectKey 冲突被
// `registerBatch` 拒绝（有日志），也不让两个会话发出同一个 key。
func (runtime *runtimeDungeonState) commitDungeonNextObjectKey(next uint32) {
	if runtime == nil {
		return
	}
	instance := runtime.lockPartyDungeonInstance()
	runtime.commitDungeonNextObjectKeyLocked(next)
	unlockPartyDungeonInstance(instance)
}

// commitDungeonNextObjectKeyLocked 与上者同义，供**已经持有实例锁**的调用方使用
// （`sync.Mutex` 不可重入，掉落提交路径正是这种情况）。
func (runtime *runtimeDungeonState) commitDungeonNextObjectKeyLocked(next uint32) {
	if runtime == nil {
		return
	}
	instance := runtime.partyInstance
	if instance == nil {
		runtime.NextObjectKey = next
		return
	}
	if next > instance.nextObjectKey {
		instance.nextObjectKey = next
	}
	runtime.NextObjectKey = instance.nextObjectKey
}

// releasePartyDungeonInstance 把一个成员从实例里摘除；摘到 0 人时立即回收实例。
// 每个 `session.dungeon.runtime = nil` 的收口点都应调用它（出图/阵亡回城/换角色/断线）。
func (s *Service) releasePartyDungeonInstance(runtime *runtimeDungeonState, characterID uint16, source string) {
	if source != "completed_run_retry" {
		s.releaseCurrentRaidParticipation(runtime, characterID)
	}
	if s == nil || runtime == nil || runtime.partyInstance == nil || characterID == 0 {
		return
	}
	instance := runtime.partyInstance
	runtime.partyInstance = nil
	registry := s.partyDungeonInstanceRegistryForService()
	if registry == nil {
		return
	}
	recycled := false
	var remaining int
	registry.mu.Lock()
	instance.mu.Lock()
	if instance.memberRuntimes[characterID] != runtime {
		instance.mu.Unlock()
		registry.mu.Unlock()
		s.logPacketEvent("game-dungeon-party-instance-stale-release-ignored",
			"instance_id", instance.id, "char_id", characterID, "source", source)
		return
	}
	delete(instance.members, characterID)
	delete(instance.sessions, characterID)
	delete(instance.memberRuntimes, characterID)
	delete(instance.scenes, characterID)
	delete(instance.positions, characterID)
	remaining = len(instance.members)
	if remaining == 0 && registry.byKey != nil {
		if current, ok := registry.byKey[instance.key]; ok && current == instance {
			delete(registry.byKey, instance.key)
			recycled = true
		}
	}
	instance.mu.Unlock()
	registry.mu.Unlock()
	s.logPacketEvent("game-dungeon-party-instance-member-left",
		"instance_id", instance.id,
		"party_id", instance.key.PartyID,
		"dungeon_id", instance.key.DungeonID,
		"char_id", characterID,
		"member_count", remaining,
		"source", source)
	if recycled {
		s.logPacketEvent("game-dungeon-party-instance-recycled",
			"instance_id", instance.id,
			"party_id", instance.key.PartyID,
			"dungeon_id", instance.key.DungeonID,
			"reason", "last_member_left",
			"source", source)
	}
}

// recyclePartyDungeonInstances 回收某个队伍的全部实例（队伍解散/缩到 1 人的收口点）。
func (s *Service) recyclePartyDungeonInstances(partyID int, reason string) {
	if s == nil || partyID <= 0 || partyID > int(^uint16(0)) {
		return
	}
	registry := s.partyDungeonInstanceRegistryForService()
	if registry == nil {
		return
	}
	recycled := make([]*dungeonPartyInstance, 0, 2)
	registry.mu.Lock()
	for key, instance := range registry.byKey {
		if key.PartyID != uint16(partyID) {
			continue
		}
		delete(registry.byKey, key)
		recycled = append(recycled, instance)
	}
	registry.mu.Unlock()
	for _, instance := range recycled {
		s.logPacketEvent("game-dungeon-party-instance-recycled",
			"instance_id", instance.id,
			"party_id", instance.key.PartyID,
			"dungeon_id", instance.key.DungeonID,
			"reason", reason)
	}
}

// prunePartyDungeonInstancesLocked 清扫：整队已无成员 / 超时未活动 / 超过容量上限。
// 调用方必须持有 registry.mu；`keep` 是本次即将写入的实例键，永远不淘汰它。
func (s *Service) prunePartyDungeonInstancesLocked(
	registry *partyDungeonInstanceRegistry,
	now time.Time,
	keep dungeonPartyRunKey,
) {
	if registry == nil || registry.byKey == nil {
		return
	}
	recycled := make([]*dungeonPartyInstance, 0, 2)
	for key, instance := range registry.byKey {
		if key == keep || instance == nil {
			continue
		}
		instance.mu.Lock()
		empty := len(instance.members) == 0
		idle := now.Sub(instance.lastSeenAt) >= currentPartyDungeonInstanceIdleTTL
		instance.mu.Unlock()
		if !empty && !idle {
			continue
		}
		delete(registry.byKey, key)
		recycled = append(recycled, instance)
	}
	// 容量上限：按 lastSeenAt 从旧到新淘汰，直到回到上限以内（为本次新建留一个位置）。
	for len(registry.byKey) >= currentPartyDungeonInstanceLimit {
		oldestKey, oldest := dungeonPartyRunKey{}, (*dungeonPartyInstance)(nil)
		for key, instance := range registry.byKey {
			if key == keep || instance == nil {
				continue
			}
			instance.mu.Lock()
			seen := instance.lastSeenAt
			instance.mu.Unlock()
			if oldest == nil || seen.Before(oldest.lastSeenAt) {
				oldestKey, oldest = key, instance
			}
		}
		if oldest == nil {
			break
		}
		delete(registry.byKey, oldestKey)
		recycled = append(recycled, oldest)
	}
	for _, instance := range recycled {
		reason := "idle_ttl"
		instance.mu.Lock()
		if len(instance.members) == 0 {
			reason = "no_members"
		}
		instance.mu.Unlock()
		s.logPacketEvent("game-dungeon-party-instance-recycled",
			"instance_id", instance.id,
			"party_id", instance.key.PartyID,
			"dungeon_id", instance.key.DungeonID,
			"reason", reason)
	}
}

// partyDungeonInstanceCount 供用例与诊断读取注册表现状；partyDungeonInstanceForRuntime
// 读取某份运行时的实例归属。
func (s *Service) partyDungeonInstanceCount() int {
	registry := s.partyDungeonInstanceRegistryForService()
	if registry == nil {
		return 0
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.byKey)
}

func (s *Service) partyDungeonInstanceForRuntime(runtime *runtimeDungeonState) *dungeonPartyInstance {
	if runtime == nil {
		return nil
	}
	return runtime.partyInstance
}
