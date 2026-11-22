package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ordinaryRoomSeedRetirementEnv 控制「普通副本过图后退队是否按实例登记的房间 Seed 放行」。
//
// legacy / off / 0 / false / no → 回到改动前的行为：只有跑团（RaidKey != 0 且
// RaidInstanceID / RaidRunID 相等）才把 Seed 从比较里剔除，普通副本只要
// current.Seed != retired.Seed 一律拒绝退队。
//
// 默认（未设置 / on）→ 交给我们自己登记过的房间 Seed 判定，见
// ownsDungeonPartyRunSeed 的注释。
const ordinaryRoomSeedRetirementEnv = "DNFBRIDGE_PARTY_ORDINARY_ROOM_SEED_RETIREMENT"

func ordinaryRoomSeedRetirementEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ordinaryRoomSeedRetirementEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

// Captured at the real return commit, while the exact runtime still owns the
// session. Room seeds change during an ordinary run; they are not run IDs.
type currentDungeonPartyRetirement struct {
	instance *dungeonPartyInstance
	token    uint64
	key      dungeonPartyRunKey
	keyValid bool
}

func captureCurrentDungeonPartyRetirementLocked(session *gameSession, runtime *runtimeDungeonState) {
	if session == nil || runtime == nil || session.dungeon.runtime != runtime || runtime.partyInstance == nil {
		return
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	session.dungeon.partyRetirement = currentDungeonPartyRetirement{runtime.partyInstance, session.dungeon.runToken, session.dungeonPartyRun, session.dungeonPartyRunValid}
}

// The same lobby can start another dungeon without changing PartyID. A return
// callback runs after releasing its old runtime, so membership alone cannot
// distinguish that callback from a return belonging to the new run.
// Callers hold no dungeon lock. apply is a manager-only mutation, with no I/O.
//
// 锁序：session.dungeon.mu → instance.mu 是本文件外既有约定的顺序
// （见 dungeon_party_instance.go 的结构体注释）。这里额外遵守一条：**不在持有
// s.mu 的情况下取 instance.mu**，否则会和 instance.mu 内部再去拿 s.mu 的路径
// 形成反向锁序。所以 Seed 归属在进入 s.mu 之前先算好，取到时再比对一次运行键。
func (s *Service) withCurrentDungeonPartyRetirement(identity boundGameSessionCharacter, instance *dungeonPartyInstance, apply func() bool) bool {
	if s == nil || identity.session == nil || instance == nil || apply == nil {
		return false
	}
	session := identity.session
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if current := session.dungeon.runtime; current != nil && current.partyInstance != instance {
		return false
	}
	activeInstanceMatches := session.dungeon.runtime != nil && session.dungeon.runtime.partyInstance == instance

	// Warm the room-seed ledger without holding s.mu. A mismatch later simply
	// falls back to the strict comparison, which rejects the retirement.
	session.dungeonPartyRunMu.Lock()
	probeKey, probeValid := session.dungeonPartyRun, session.dungeonPartyRunValid
	session.dungeonPartyRunMu.Unlock()
	seedOwnedByInstance := probeValid &&
		ordinaryRoomSeedRetirementEnabled() &&
		instance.ownsDungeonPartyRunSeed(probeKey)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gameSessions[identity.character] != session || session.selectedCharacterID != identity.character || session.characterGeneration != identity.generation {
		return false
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if session.dungeonPartyRunValid {
		current, retired := session.dungeonPartyRun, instance.key
		sameRaidRun := current.RaidKey != 0 && current.RaidKey == retired.RaidKey &&
			current.RaidInstanceID != "" && current.RaidInstanceID == retired.RaidInstanceID &&
			current.RaidRunID != "" && current.RaidRunID == retired.RaidRunID
		// 上游新增：回城提交那一刻捕获的实例/令牌证明（同一大厅连开两把时，
		// 仅凭成员关系无法区分新一局的回调）。
		proof := session.dungeon.partyRetirement
		committedOwner := session.dungeon.runtime == nil && proof.instance == instance &&
			proof.token == session.dungeon.runToken && proof.keyValid && proof.key == current
		// 过门会替换房间 Seed，而 instance.key 永远停在进图首房。跑团靠冻结的
		// 报名标识认场；普通副本没有这个标识，改用「当前仍持有该副本实例」或
		// 「这个 Seed 是本实例在落房时登记过的」，既放行合法过图/断线退队，又挡住其它一局的陈旧回调。
		if committedOwner || sameRaidRun || activeInstanceMatches || (seedOwnedByInstance && current == probeKey) {
			current.Seed, retired.Seed = 0, 0
		}
		// Room coordinates legitimately advance inside the same instance.
		current.RoomX, current.RoomY, current.MapID = 0, 0, 0
		retired.RoomX, retired.RoomY, retired.MapID = 0, 0, 0
		if current != retired {
			return false
		}
	}
	return apply()
}

// ownsDungeonPartyRunSeed 判断 current 的房间 Seed 是否确凿来自**这一个**
// dungeonPartyInstance：进图首房（Seed 存在 instance.key 里），或者本实例过门
// 落房时登记进 instance.rooms 的房间 Seed。
//
// 同队连开两把时，第 2 把的 Seed 只会登记在 instance_2.rooms 上；携带
// instance_1 的陈旧回调拿第 2 把的 Seed 来问，这里必然返回 false，于是被拦下。
func (instance *dungeonPartyInstance) ownsDungeonPartyRunSeed(current dungeonPartyRunKey) bool {
	if instance == nil {
		return false
	}
	if current.Seed == instance.key.Seed {
		return true
	}
	roomKey := runtimeDungeonRoomKey{
		X:     current.RoomX,
		Y:     current.RoomY,
		MapID: current.MapID,
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.rooms == nil {
		return false
	}
	shared, ok := instance.rooms[roomKey]
	if !ok {
		return false
	}
	return shared.MoveSeedKnown && shared.MoveSeed == current.Seed
}

// notePartyDungeonInstanceRoomSeed 在落房时把这一间的权威 Seed 记到共享实例上。
//
// 已有的 freezePartyDungeonMoveSeed 只覆盖"过门"这一条路径，而且只写已经存在
// 的共享房间；剧情阶段切换、分层回退等不走它的落位也需要登记，否则那些房间
// 退队时会退化成严格 Seed 比较而被拒。
func notePartyDungeonInstanceRoomSeed(runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) {
	if runtime == nil || runtime.partyInstance == nil {
		return
	}
	instance := runtime.partyInstance
	roomKey := runtimeDungeonRoomKeyFromScene(scene)
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.rooms == nil {
		instance.rooms = make(map[runtimeDungeonRoomKey]partyDungeonSharedRoom)
	}
	shared := instance.rooms[roomKey]
	if shared.Room == nil && runtime.Room != nil {
		shared.Room = runtime.Room
		shared.Scene = scene
	}
	shared.MoveSeed = runtime.Seed
	shared.MoveSeedKnown = true
	instance.rooms[roomKey] = shared
}
