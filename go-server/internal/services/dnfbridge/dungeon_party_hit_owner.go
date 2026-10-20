package dnfbridge

import (
	"os"
	"strconv"
	"strings"
)

// 本文件实现"**两名队员打同一只怪时，其中一人的伤害变成 1**"这条缺陷的服务端侧修法。
//
// # 先把"服务端有没有伤害路径"钉死（取证结论，反证见报告 §B）
//
//   - 服务端**从来不解析、也从不计算**任何一次命中的伤害数值：
//     `dungeoncmd.DieMonsterRequest` 只把 op39 的 `CombatEntries` 当**不透明字节**保留
//     （request.go:102-103，`CombatEntryCount byte` + `CombatEntries []byte`，
//     每行 10 字节，没有任何字段名）；
//   - op180 `SDC_DAMAGE_CHECK`、op734 `DUNGEON_DAMAGE_INFO`、op735
//     `ANALYSIS_DUNGEON_DAMAGE_INFO` 在 `dnf\network` 里**只有枚举名、没有任何处理器**
//     （全仓 grep 只命中 dnfenum/cmdpacket.go）；
//   - 全仓（含三份厂商 C#）都**没有** `Math.Max(1, damage)` / `damage < 1` / `MinDamage`
//     这类伤害下限；厂商侧同样没有 monster HP 归属、没有 owner 校验、没有命中包
//     （见报告 §B 的逐文件引用）。
//
// ⇒ **那个 "1" 是客户端画出来的**：客户端把"服务端拒绝结算的那次命中"退化成最小伤害。
// 服务端唯一能造成"同一只怪、两个攻击者里恰好一人的命中被判无效"的条件，是
// `handleDungeonMonsterDeath` 的 **op39 owner 闸门**：
//
//	dungeon_combat_runtime.go:262-275（改动前）
//	  playerObjectKey := currentSceneActorObjectKey(session.selectedCharacterID)
//	  if !scriptedTutorialDeath && !ownerSentinelBlockingClearTarget && !ownerSentinelOrdinaryTarget &&
//	      request.OwnerObjectKey != playerObjectKey &&
//	      !roomProvenHostile &&
//	      !dungeonRuntimeContainsDeathOwnerObjectKey(runtime, uint32(request.OwnerObjectKey)) {
//	          logGameEvent(..., "room_related_actor_object_key_mismatch")
//	          return nil            // ← 一个字节都不回：客户端那只怪永远停在 0 血/半死
//	  }
//
// 这条闸门在**两打一**形状下必然命中：现场 packet_log.txt（2026-09-28，双开，队伍 1，
// 副本 310 房间 1:0）里队长（角色 3）的客户端上报的 op39，把击杀者写成了**队友 actor**：
//
//	:95770 EVENT kind=game-dungeon-monster-death-owner-relaxed conn_id=game-000021
//	       dungeon_id=310 room=1:0 map_id=36752 object_key=418 related_actor_object_key=6
//	       player_object_key=3 request_layout=2 room_proven_hostile=true
//	       reason=claimed_killer_owner_unproven_but_target_is_announced_live_room_hostile
//	       t=2026-09-28T13:37:09.152404248Z
//
// 注意这一行只有在**非默认**的 `DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF=room_proven` 下才会出现
// （`dungeon_death_room_proof.go:84-89`：默认档是 `strict`，`roomProvenHostile` 恒为 false）。
// 也就是说：**默认档下同一个请求会被判成 `room_related_actor_object_key_mismatch` 直接丢弃**，
// 客户端那只怪打不死 ⇒ 之后对该怪的命中只能读到最小值 1。这正是操作者看到的
// 「有概率有一人的伤害为 1」：谁的客户端先上报、谁的命中被判无效，取决于两个人的出手顺序。
//
// # 本开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_HIT_OWNER`
//	  - `party`（默认，零值/空/未设置/非法值）= 修好后的行为：op39 的"相关 actor"
//	    指向**同一支队伍、且仍在同一场队伍副本实例里**的队友时，这条命中/击杀必须按
//	    普通怪死亡结算（两打一的正常形状），并记 `game-dungeon-party-hit-accepted`；
//	    其余形状仍然照旧拒收，但会多记一条 `game-dungeon-party-hit-clamped-to-one`
//	    写明"这次命中会退化成最小伤害 1"的原因；
//	  - `strict` = 逐字节回到改动前：命中/击杀一律按今天那条闸门判，**不记本文件的新事件**，
//	    线格式与日志都与改动前逐字节相同。
const dungeonPartyHitOwnerEnv = "DNFBRIDGE_PARTY_DUNGEON_HIT_OWNER"

const (
	dungeonPartyHitOwnerValueParty  = "party"
	dungeonPartyHitOwnerValueStrict = "strict"
)

// dungeonPartyHitOwnerMode 是 dungeonPartyHitOwnerEnv 的归一化取值。
type dungeonPartyHitOwnerMode int

const (
	// dungeonPartyHitOwnerParty 是默认档（零值）= 修好后的行为。
	dungeonPartyHitOwnerParty dungeonPartyHitOwnerMode = iota
	// dungeonPartyHitOwnerStrict 是改动前行为，一键回退档。
	dungeonPartyHitOwnerStrict
)

func (mode dungeonPartyHitOwnerMode) String() string {
	if mode == dungeonPartyHitOwnerStrict {
		return dungeonPartyHitOwnerValueStrict
	}
	return dungeonPartyHitOwnerValueParty
}

// readDungeonPartyHitOwnerMode 解析 DNFBRIDGE_PARTY_DUNGEON_HIT_OWNER。
// **默认 `party`（= 修好后的行为）**：空/未设置/非法值都是 party；
// 只有显式 `strict` 回到改动前那条"相关 actor 不是自己就拒收"的闸门。
func readDungeonPartyHitOwnerMode(value string) dungeonPartyHitOwnerMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dungeonPartyHitOwnerValueStrict:
		return dungeonPartyHitOwnerStrict
	default:
		return dungeonPartyHitOwnerParty
	}
}

// dungeonPartyHitOwnerMode 自读开关（不改 options.go）。
func (s *Service) dungeonPartyHitOwnerGate() dungeonPartyHitOwnerMode {
	return readDungeonPartyHitOwnerMode(os.Getenv(dungeonPartyHitOwnerEnv))
}

// currentDungeonPartyHitMinimumDamage 是**客户端**对"服务端拒绝结算的那次命中"能画出的
// 最小伤害。服务端不计算伤害，这个常量只用于把"服务端是否拒收"映射成一个可测试、可判读的
// 判定（事件字段 `damage_floor`）：`real` = 客户端按自己的真实数值结算；`1` = 退化成最小值。
const currentDungeonPartyHitMinimumDamage = 1

// dungeonPartyHitVerdict 是"这一次命中/击杀上报"的服务端判定。
type dungeonPartyHitVerdict int

const (
	// dungeonPartyHitRealValue = 被接受：客户端按自己的真实伤害数值结算。
	dungeonPartyHitRealValue dungeonPartyHitVerdict = iota
	// dungeonPartyHitClampedToOne = 被判无效：客户端只能读到最小伤害 1（缺陷 B 的现场口径）。
	dungeonPartyHitClampedToOne
)

// currentDungeonPartyHitDamageFloor 把判定映射成伤害下限：接受 ⇒ 0（= 服务端不设下限，
// 客户端用自己的真实数值）；拒收 ⇒ 1（客户端的最小伤害）。
func currentDungeonPartyHitDamageFloor(verdict dungeonPartyHitVerdict) int {
	if verdict == dungeonPartyHitClampedToOne {
		return currentDungeonPartyHitMinimumDamage
	}
	return 0
}

// dungeonPartyHitVerdictForAccepted 是"闸门判定 → 伤害下限判定"的唯一映射点。
func dungeonPartyHitVerdictForAccepted(accepted bool) dungeonPartyHitVerdict {
	if accepted {
		return dungeonPartyHitRealValue
	}
	return dungeonPartyHitClampedToOne
}

// currentDungeonPartyHitDamageFloorLabel 是事件字段 `damage_floor` 的取值：
// `real` = 客户端按自己的真实数值结算（服务端不设下限）；`1` = 退化成最小伤害
// （缺陷 B 的现场口径）。
func currentDungeonPartyHitDamageFloorLabel(verdict dungeonPartyHitVerdict) string {
	if verdict == dungeonPartyHitClampedToOne {
		return strconv.Itoa(currentDungeonPartyHitMinimumDamage)
	}
	return "real"
}

// currentDungeonPartyHitOwnerResolution 是一次 op39 "相关 actor 是不是本队队友"的判定结果。
type currentDungeonPartyHitOwnerResolution struct {
	// accepted = 这次命中/击杀按普通怪死亡结算（两打一形状）。
	accepted bool
	// partyPeerCharID 是 op39 的 related actor 指向的那个队友角色（object key == 角色 id）。
	partyPeerCharID uint16
	// partyMember = 这个 key 是不是本会话队伍名单里的成员。
	partyMember bool
	// sameInstance = 这个队友此刻是否仍在**同一场**队伍副本实例里。
	sameInstance bool
	partyID      int
	instanceID   uint64
	// reason 是拒收/接受的原因，直接进事件字段。
	reason string
}

// resolveCurrentDungeonPartyHitOwner 判定 op39 的"相关 actor"（客户端声称的击杀者）是不是
// **同一支队伍、同一场队伍副本实例里的队友**。
//
// 全部输入都来自服务端自己的状态，玩家无法用它把别人的怪判死：
//   - 队伍名单来自本会话的队伍投影（`runtimePartyStateSnapshot`，只读 session.party.mu 这个叶子锁，
//     避免在 `session.dungeon.mu` 之下再去拿 Service.mu）；
//   - "同一场"来自队伍副本实例注册表（`runtime.partyInstance`，只读 instance.mu），
//     而实例键本身已经把 PartyID 算进去了（`dungeonPartyRunKey.PartyID`）。
//
// 回退档（`strict`）恒返回 `accepted=false`：与改动前逐字节相同。
func (s *Service) resolveCurrentDungeonPartyHitOwner(
	session *gameSession,
	runtime *runtimeDungeonState,
	relatedActorObjectKey uint16,
	mode dungeonPartyHitOwnerMode,
) currentDungeonPartyHitOwnerResolution {
	resolution := currentDungeonPartyHitOwnerResolution{reason: "strict_mode_legacy_gate"}
	if mode == dungeonPartyHitOwnerStrict {
		return resolution
	}
	if s == nil || session == nil || runtime == nil || relatedActorObjectKey == 0 ||
		relatedActorObjectKey == ^uint16(0) {
		resolution.reason = "related_actor_object_key_missing"
		return resolution
	}
	state := runtimePartyStateSnapshot(session)
	resolution.partyID = state.PartyID
	if state.PartyID <= 0 {
		resolution.reason = "reporting_session_has_no_party_projection"
		return resolution
	}
	peerCharID := uint16(0)
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == relatedActorObjectKey {
			peerCharID = member.UserID
			break
		}
	}
	if peerCharID == 0 {
		resolution.reason = "related_actor_is_not_a_member_of_the_reporting_session_party"
		return resolution
	}
	resolution.partyPeerCharID = peerCharID
	resolution.partyMember = true
	instance := runtime.partyInstance
	if instance == nil {
		resolution.reason = "reporting_session_has_no_shared_party_dungeon_instance"
		return resolution
	}
	resolution.instanceID = instance.id
	if instance.key.PartyID != uint16(state.PartyID) {
		resolution.reason = "shared_instance_belongs_to_another_party_generation"
		return resolution
	}
	if !dungeonPartyInstanceHasMember(instance, peerCharID) {
		resolution.reason = "party_peer_is_not_in_this_dungeon_instance"
		return resolution
	}
	resolution.sameInstance = true
	resolution.accepted = true
	resolution.reason = "party_peer_attacker_in_the_same_dungeon_instance"
	return resolution
}

// dungeonPartyInstanceHasMember 只读实例注册表判断某角色是否仍在场。
func dungeonPartyInstanceHasMember(instance *dungeonPartyInstance, characterID uint16) bool {
	if instance == nil || characterID == 0 {
		return false
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	_, ok := instance.members[characterID]
	return ok
}
