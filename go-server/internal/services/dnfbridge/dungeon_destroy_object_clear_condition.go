package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// currentDungeonDestroyObjectClearConditionSource binds a client-owned
// passive object to the active maze's explicit one-object clear condition.
//
// 早先这里用 `objectKey > 0xFFFF` 当"这是 PVF 被动对象代码、不是 u16 actor key"
// 的判据。真档 Script.pvf 证伪了这个边界：**艾肯（单人）- 遗落的神殿**
// （dungeon 5097，maze 0/1 都声明 `[clear condition][destroy object] 53764 1`，
// 目标 `ActionObject/Map/Legend/Ekern/System/BossChecker/BossChecker.obj`）
// 的目标 id 53764 **小于 0x10000**，于是这条闸门让该副本自己的通关条件永远
// 匹配不上：客户端销毁 BossChecker 后 op39 被拒、房间无法收尾。
//
// 真正的身份判据是"本房间地图里这个 id 的摆放次数 == 该 clear condition 的 count"，
// 这条判据对 u16 / 超 u16 的 id 同样成立，因此 u16 边界被移除（objectKey == 0
// 仍然拒绝）。可见的坐标位摆放（Count 不匹配）与无摆放都会被上面的摆放计数拒绝。
func currentDungeonDestroyObjectClearConditionSource(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
) string {
	if runtime == nil || runtime.Room == nil || runtime.Session == nil ||
		objectKey == 0 || !scene.Cleared || !scene.Boss ||
		!runtime.BossSet || scene.Coordinate != runtime.BossCoordinate ||
		runtime.MazeIndex < 0 || runtime.MazeIndex >= len(runtime.Dungeon.Mazes) {
		return ""
	}
	room := runtime.Room.Snapshot()
	if room.Coordinate != scene.Coordinate || room.MapID != scene.Map.Map.ID {
		return ""
	}

	placementCount := int64(0)
	for _, object := range scene.PassiveObjects {
		if object.ObjectID == int64(objectKey) {
			placementCount++
		}
	}
	if placementCount != 1 {
		return ""
	}

	for _, condition := range runtime.Dungeon.Mazes[runtime.MazeIndex].ClearConditions {
		if normalizeDungeonPVFSymbol(condition.Type) == "destroy object" &&
			condition.TargetID == int64(objectKey) && condition.Count == placementCount {
			return fmt.Sprintf(
				"current_pvf_clear_condition_destroy_object_target_%d_count_%d",
				condition.TargetID,
				condition.Count,
			)
		}
	}
	return ""
}

// handleCurrentDungeonDestroyObjectClearConditionDeathLocked accepts the
// variable op39 emitted after the current client has already removed a PVF
// passive object locally. No op38 is sent: this object is a map placement the
// client owns and has already destroyed, not an announced room actor, so an
// op38 carrying its (possibly u16-range) id could be matched against a real
// actor key instead. The caller owns session.dungeon.mu.
func (s *Service) handleCurrentDungeonDestroyObjectClearConditionDeathLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
) (bool, error) {
	source := currentDungeonDestroyObjectClearConditionSource(runtime, scene, objectKey)
	if source == "" {
		return false, nil
	}
	s.logGameEvent(session, "game-dungeon-destroy-object-clear-condition-accepted",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"object_key", objectKey,
		"death_ack_sent", false,
		"reason", "current_exe_op38_target_is_u16_and_client_owned_passive_object_is_already_destroyed",
		"source", source)
	if err := s.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(
		session,
		runtime,
		scene,
		objectKey,
	); err != nil {
		return true, err
	}
	return true, nil
}
