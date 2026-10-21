package dnfbridge

import "longheng.io/server/internal/modules/dnf/worldmap"

// Session-owned evidence, bounded to two placements and two position reports
// per scene. This observes client reports; it never supplies a spawn position.
type fiendWarSceneDiagnosticState struct {
	key        dungeonPartyRunKey
	placements [2]bool
	positions  uint8
}

// Call with the same session/runtime ownership as the surrounding scene code.
// Do not take dungeon.mu here: op35 already holds it.
func (s *Service) logFiendWarSceneDiagnostic(p *gameSession, r *runtimeDungeonState, scene worldmap.DungeonRoomScene, stage string, x, y uint16) {
	if s == nil || p == nil || r == nil || !isCurrentFiendWarChannel(p) || (r.Dungeon.ID != 4953 && r.Dungeon.ID != 4978) {
		return
	}
	key := dungeonPartyRunKey{DungeonID: r.Dungeon.ID, MazeIndex: r.MazeIndex, RoomX: scene.Coordinate.X, RoomY: scene.Coordinate.Y, MapID: scene.Map.Map.ID, Seed: r.Seed}
	if r.raidEntry != nil {
		key.RaidKey, key.RaidInstanceID, key.RaidRunID = r.raidEntry.RaidKey, r.raidEntry.InstanceID, r.raidEntry.RunID
	}
	d := &r.fiendWarSceneDiagnostic
	if d.key != key {
		*d = fiendWarSceneDiagnosticState{key: key}
	}
	switch stage {
	case "post_start_placement", "finish_loading_placement":
		i := 0
		if stage == "finish_loading_placement" {
			i = 1
		}
		if d.placements[i] {
			return
		}
		d.placements[i] = true
	case "client_position":
		if d.positions >= 2 {
			return
		}
		d.positions++
	default:
		return
	}
	args := []any{"kind", "game-fiendwar-scene-diagnostic", "conn_id", p.connID, "channel_id", p.channel.ID,
		"char_id", p.selectedCharacterID, "stage", stage, "dungeon_id", key.DungeonID,
		"map_id", key.MapID, "maze", key.MazeIndex, "room_x", key.RoomX, "room_y", key.RoomY,
		"seed", key.Seed, "raid_key", key.RaidKey, "instance_id", key.RaidInstanceID, "run_id", key.RaidRunID,
		"post_start_ready", p.postStartMapPlayerStateSent, "finish_ready", p.postFinishLoadingPlayerStateSent}
	if stage == "client_position" {
		args = append(args, "position_report", d.positions, "position_x", x, "position_y", y)
	} else {
		placement := buildCurrentSceneActorPlacementBody()
		args = append(args, "actor_slot", placement[0], "placement_seed", placement[1])
	}
	s.logInfo("dnfbridge fiendwar scene diagnostic", args...)
}
