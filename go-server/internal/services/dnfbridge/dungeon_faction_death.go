package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 同房不同阵营怪物互杀。
//
// 现场（艾肯 5097，2026-11-09）：房间里的怪会互相打，客户端把击杀者写进贡献项
// （kind=0x211，键是被打死那只之外的另一个怪）。原有 `currentEkernFactionDeath`
// 只认「普通 [monster] 行」，遇到运行期扩展 actor（地图 special object 投放的怪）
// 就解析不出阵营，于是这对里的后一只被拒、留白。
//
// 这里做通用版本：只要贡献者和目标都是本房间**在册、存活**的怪物 actor
// （普通行或扩展 actor 均可），且两者在 PVF `[monster team]` 里是**不同阵营**，
// 就按普通怪死亡结算。同阵营、跨房、未知对象继续拒绝。
// 不要求消耗凭证：阵营互杀与玩家是否用人偶无关。
func (s *Service) currentDungeonRoomFactionDeath(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	request dungeoncmd.DieMonsterRequest,
) bool {
	if s == nil || runtime == nil || runtime.Room == nil ||
		s.options.currentClientVersion != "official-24.9" {
		return false
	}
	if request.OwnerObjectKey != ^uint16(0) ||
		request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat ||
		request.CombatEntryCount != 1 ||
		len(request.CombatEntries) != dungeoncmd.Official249DieMonsterEntrySize ||
		len(request.OpaqueTail) != 0 {
		return false
	}
	entry := request.CombatEntries
	if binary.LittleEndian.Uint16(entry) != currentDungeonPuppetContributionKindMonster ||
		binary.LittleEndian.Uint64(entry[4:]) == 0 {
		return false
	}
	contributor := uint32(binary.LittleEndian.Uint16(entry[2:]))
	target := request.RuntimeObjectKey
	if contributor == 0 || contributor == target {
		return false
	}
	if !currentDungeonRoomAnnouncedLiveHostile(runtime, scene, target) {
		return false
	}
	reference, bound := scene.RuntimeObjects[target]
	if !bound || !runtimeSceneContainsHostile(scene.BlockingHostiles, reference) {
		return false
	}
	if !currentDungeonRoomKnownMonsterActor(runtime, contributor) {
		s.logInfo("dnfbridge room faction death rejected",
			"dungeon_id", runtime.Dungeon.ID,
			"target_key", target,
			"contributor_key", contributor,
			"reason", "contributor_not_announced_monster_actor")
		return false
	}
	// 阵营只用于日志：PVF [monster team] 在部分房间行数不足或与运行期投放的
	// actor 对不上，把它当硬门槛会让"两只在册怪互杀"里的一只永远结算不了。
	// 硬边界保留为：双方都在册、目标存活且阻挡、贡献者不是目标。
	contributorTeam, contributorOK := currentDungeonRoomActorTeam(runtime, scene, contributor)
	targetTeam, targetOK := currentDungeonRoomActorTeam(runtime, scene, target)
	s.logInfo("dnfbridge room faction death accepted",
		"dungeon_id", runtime.Dungeon.ID,
		"map_id", runtime.Room.Snapshot().MapID,
		"room", runtime.Room.Snapshot().Coordinate.String(),
		"target_key", target,
		"contributor_key", contributor,
		"contributor_team_resolved", contributorOK,
		"contributor_team", contributorTeam,
		"target_team_resolved", targetOK,
		"target_team", targetTeam)
	return true
}

// currentDungeonRoomKnownMonsterActor 判断某个对象键是不是本房间在册的
// 怪物 actor（普通 [monster] 行，或带敌对引用的扩展 actor）。AI 角色和非敌对对象不算。
func currentDungeonRoomKnownMonsterActor(runtime *runtimeDungeonState, objectKey uint32) bool {
	if runtime == nil || runtime.Room == nil || objectKey == 0 {
		return false
	}
	room := runtime.Room.Snapshot()
	for _, monster := range room.Monsters {
		// 贡献者可能已经死了：它的残留效果/召唤物晚一步打死目标，客户端仍记在它头上。
		if monster.ObjectKey == objectKey && monster.State != runtimeDungeonMonsterPlanned {
			return true
		}
	}
	for _, actor := range room.ExtendedActors {
		if actor.ObjectKey == objectKey && actor.State != runtimeDungeonMonsterPlanned &&
			actor.HostileReference != nil {
			return true
		}
	}
	return false
}

// currentDungeonRoomActorTeam 解析一个在册怪物 actor 的 PVF 阵营，普通 [monster] 行
// 与扩展 actor 都支持。返回 false 表示这个键不是本房间在册的怪物 actor，或没有阵营行。
func currentDungeonRoomActorTeam(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
) (int64, bool) {
	if runtime == nil || runtime.Room == nil || objectKey == 0 {
		return 0, false
	}
	index := int(-1)
	monsterID := int64(0)
	if monster, announced := runtime.Room.AnnouncedMonster(objectKey); announced {
		index = monster.Reference.Index
		monsterID = monster.Spawn.MonsterID
	} else {
		for _, actor := range runtime.Room.Snapshot().ExtendedActors {
			if actor.ObjectKey != objectKey || actor.State != runtimeDungeonMonsterAnnounced ||
				actor.HostileReference == nil {
				continue
			}
			index = actor.HostileReference.Index
			monsterID = int64(actor.Packet.Code)
			break
		}
	}
	if index < 0 || index >= len(scene.Map.Map.MonsterTeam) || index >= len(scene.Map.Map.Monsters) {
		return 0, false
	}
	if monsterID != 0 && scene.Map.Map.Monsters[index].MonsterID != monsterID {
		return 0, false
	}
	return scene.Map.Map.MonsterTeam[index], true
}
