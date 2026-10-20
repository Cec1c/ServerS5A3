package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件实现 op39（DIE_MONSTER，S2C 侧是 op38 死亡通知）的**目标房间证明**开关。
//
// # 现场症状
//
// 操作者：「副本 艾肯(单人) - 遗落的神殿 第一个房间固定有一只怪物死亡后变白但不消失
// 导致无法继续前进」。「变白但不消失」在这套客户端里的准确含义是：客户端本地把怪打到
// 0 HP（自身进入半死/白色状态），但**没有收到服务端的权威死亡通知**，所以永远不把它
// 从场景里移除；房间在客户端侧永远不算清空 ⇒ 门不开 ⇒ op45 过图发不出（或发出去也被
// 服务端以 `ErrRunCurrentRoomNotCleared` 拒掉）⇒ 卡在房间里。
//
// # 服务端为什么会不发这一条 op38
//
// `handleDungeonMonsterDeath` 在真正提交死亡之前有两道**客户端形状**闸门：
//
//	① `OwnerObjectKey == 0xFFFF`（哨兵）时，请求体必须恰好是 variable-combat 形状
//	   且 combat entry 数为 0，否则 `non_hostile_ai_request_shape_unproven`；
//	② `OwnerObjectKey` 既不等于本地角色 object key、也不指向本房间在册 actor 时，
//	   `room_related_actor_object_key_mismatch`。
//
// 两条闸门都只回一行日志、**一个字节都不发给客户端**，因此只要客户端在这种场景里用了
// 别的形状（固定 62 字节 / 带 combat entry / 把击杀者写成召唤物或场景对象），症状就
// 正好是"白色残影 + 房间清不掉"。
//
// # 两条闸门是有意为之的（默认必须保留）
//
// 它们由既有回归用例钉死（`dungeon_runtime_test.go` 的 mismatched-owner / owner=ffff
// 反例，`dungeon_owner_sentinel_hostile_test.go` 的 fixed-layout / combat-entry 反例），
// 目的是不让伪造或形状不明的 op39 改动房间状态。没有现场 op39 之前不应推翻它们。
//
// # 本开关
//
//	`DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF`
//	  - `strict`（默认，零值）= 与改动前逐字节一致：形状不可证就拒收、只记日志；
//	  - `room_proven` = 追加一条**服务端自证**的放行判据：只要 op39 的目标 object key
//	    确实是本房间已经宣告、仍在场、尚未阵亡的敌对 actor（并且当前会话场景把它绑在
//	    同一个 reference 上），就按普通怪死亡提交并回 op38，客户端声称的 owner 与请求
//	    体形状只进日志。
//
// 放行判据全部来自服务端自己的状态（`runtimeDungeonRoom` 的在册 actor 表 + 会话场景的
// RuntimeObjects/DefeatedObjects），玩家无法用它把别人的怪判死：目标必须已经**被本会话
// 宣告过**，而宣告只发生在 op29 房间提交时。教程副本有自己的脚本化死亡判据
// （`isPVFTutorialScriptedMonsterDeath`），本判据与既有 owner-sentinel 兜底同口径，
// 一律不介入。
const dungeonDeathRoomProofEnv = "DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF"

const (
	dungeonDeathRoomProofValueStrict     = "strict"
	dungeonDeathRoomProofValueRoomProven = "room_proven"
)

// dungeonDeathRoomProofMode 是 dungeonDeathRoomProofEnv 的归一化取值。
type dungeonDeathRoomProofMode int

const (
	// dungeonDeathRoomProofStrict 是默认档（零值）= 改动前行为。
	dungeonDeathRoomProofStrict dungeonDeathRoomProofMode = iota
	// dungeonDeathRoomProofRoomProven = 目标在册即放行。
	dungeonDeathRoomProofRoomProven
)

func (mode dungeonDeathRoomProofMode) String() string {
	if mode == dungeonDeathRoomProofRoomProven {
		return dungeonDeathRoomProofValueRoomProven
	}
	return dungeonDeathRoomProofValueStrict
}

// allowsProvenRoomHostile 表示是否启用"目标在册即放行"判据。
func (mode dungeonDeathRoomProofMode) allowsProvenRoomHostile() bool {
	return mode == dungeonDeathRoomProofRoomProven
}

// readDungeonDeathRoomProofMode 解析 DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF。
// **默认 strict**：空/未设置/非法值都是 strict，只有显式写 `room_proven` 才放行，
// 拼错不会静默放开这条边界。
func readDungeonDeathRoomProofMode(value string) dungeonDeathRoomProofMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonDeathRoomProofValueRoomProven) {
		return dungeonDeathRoomProofRoomProven
	}
	return dungeonDeathRoomProofStrict
}

// dungeonDeathRoomProof 返回当前 Service 的归一化档位（零值 options 的测试/探针路径 =
// strict = 默认档）。
func (s *Service) dungeonDeathRoomProof() dungeonDeathRoomProofMode {
	if s == nil {
		return dungeonDeathRoomProofStrict
	}
	return s.options.dungeonDeathRoomProof
}

// currentDungeonRoomAnnouncedLiveHostile 判定 op39 的**目标** object key 是不是本房间
// 已经宣告、仍在场、尚未阵亡的敌对 actor。它只读服务端自己的状态：
//
//	① 目标属于本房间（runtime 房间的坐标/地图 == 当前会话场景）；
//	② 目标已经宣告（planned 的怪客户端还没见过，不可能报它死亡）；
//	③ 目标尚未阵亡（重复上报走既有的幂等重放路径，不走这里）；
//	④ 当前会话场景把这个 key 绑在同一个 reference 上，该 reference 在期望敌怪表里，
//	   且不在已阵亡表里。
//
// 教程副本有自己的脚本化死亡判据，这里与既有 owner-sentinel 兜底同口径，一律不介入。
func currentDungeonRoomAnnouncedLiveHostile(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
) bool {
	if runtime == nil || runtime.Room == nil || runtime.Session == nil ||
		objectKey == 0 || objectKey > uint32(^uint16(0)) {
		return false
	}
	if isPVFTutorialDungeon(runtime) || currentDungeonPathUsesTutorialNamespace(runtime.Dungeon.Path) {
		return false
	}
	room := runtime.Room.Snapshot()
	if room.Coordinate != scene.Coordinate || room.MapID != scene.Map.Map.ID {
		return false
	}
	reference, bound := scene.RuntimeObjects[objectKey]
	if !bound || !runtimeSceneContainsHostile(scene.ExpectedHostiles, reference) ||
		dungeonSceneObjectDefeated(scene.DefeatedObjects, objectKey) {
		return false
	}
	if monster, announced := runtime.Room.AnnouncedMonster(objectKey); announced {
		return monster.Reference == reference
	}
	for _, actor := range room.ExtendedActors {
		if actor.ObjectKey != objectKey || actor.State != runtimeDungeonMonsterAnnounced ||
			actor.HostileReference == nil {
			continue
		}
		return *actor.HostileReference == reference
	}
	return false
}

// dungeonDeathRoomProofActive 汇总"本会话是否可以用在册目标放行"。默认档下恒为 false，
// 因此默认路径与改动前完全一致。
func (s *Service) dungeonDeathRoomProofActive() bool {
	return s.dungeonDeathRoomProof().allowsProvenRoomHostile()
}
