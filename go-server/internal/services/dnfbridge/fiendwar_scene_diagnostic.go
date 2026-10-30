package dnfbridge

import "longheng.io/server/internal/modules/dnf/worldmap"

// Session-owned evidence: at most two sent placements, one observed finish
// request and two position reports per scene. This observes client reports;
// it never supplies a spawn position.
type fiendWarSceneDiagnosticState struct {
	key            dungeonPartyRunKey
	placements     [2]bool
	finishObserved bool
	positions      uint8
}

// currentFiendWarSceneDiagnosticRun accepts the run identity recorded by the
// existing FiendWar admission path. It deliberately does not enumerate the
// FiendWar dungeon IDs: the live 20:45 evidence includes facility dungeons
// such as 4975/4964, which are real FiendWar runs even though they are not
// the two single-maze boss dungeons.
func currentFiendWarSceneDiagnosticRun(p *gameSession, r *runtimeDungeonState) bool {
	if p == nil || r == nil || !isCurrentFiendWarChannel(p) || r.raidEntry == nil {
		return false
	}
	entry := r.raidEntry
	return entry.ChannelType == p.channel.Type &&
		entry.ChannelID == p.channel.ID &&
		entry.RunID != ""
}

// Call with the same session/runtime ownership as the surrounding scene code.
// Do not take dungeon.mu here: op35 already holds it.
func (s *Service) logFiendWarSceneDiagnostic(p *gameSession, r *runtimeDungeonState, scene worldmap.DungeonRoomScene, stage string, x, y uint16) {
	if s == nil || p == nil || r == nil || !currentFiendWarSceneDiagnosticRun(p, r) {
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
	case "finish_loading_observed":
		if d.finishObserved {
			return
		}
		d.finishObserved = true
	case "client_position":
		if d.positions >= 2 {
			return
		}
		d.positions++
	default:
		return
	}
	finishGates := p.finishLoadingGateSnapshot()
	args := []any{"kind", "game-fiendwar-scene-diagnostic", "conn_id", p.connID, "channel_id", p.channel.ID,
		"char_id", p.selectedCharacterID, "stage", stage, "dungeon_id", key.DungeonID,
		"map_id", key.MapID, "maze", key.MazeIndex, "room_x", key.RoomX, "room_y", key.RoomY,
		"seed", key.Seed, "raid_key", key.RaidKey, "instance_id", key.RaidInstanceID, "run_id", key.RaidRunID,
		"post_start_ready", p.postStartMapPlayerStateSent, "finish_ready", finishGates.PostFinish}
	if stage == "client_position" {
		args = append(args, "position_report", d.positions, "position_x", x, "position_y", y)
	} else {
		sent := stage != "finish_loading_observed"
		args = append(args, "placement_sent", sent)
		if sent {
			placement := buildCurrentSceneActorPlacementBody()
			args = append(args, "actor_slot", placement[0], "placement_seed", placement[1])
		}
	}
	s.logInfo("dnfbridge fiendwar scene diagnostic", args...)
}

// logCurrentFiendWarFinishLoadingStage records either an observed finish
// request (before the actor-initialization early return) or a successfully
// sent finish placement. The two stages have separate per-room limits so an
// observation never consumes the real placement event.
func (s *Service) logCurrentFiendWarFinishLoadingStage(p *gameSession, stage string) {
	if s == nil || p == nil {
		return
	}
	p.dungeon.mu.Lock()
	defer p.dungeon.mu.Unlock()
	r := p.dungeon.runtime
	if r == nil || r.Session == nil {
		return
	}
	if scene, ok := r.Session.Scene(); ok {
		s.logFiendWarSceneDiagnostic(p, r, scene, stage, 0, 0)
	}
}
