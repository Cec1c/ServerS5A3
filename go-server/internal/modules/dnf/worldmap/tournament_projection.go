package worldmap

import "fmt"

// ProjectTournamentRoomActors 用给定 actor 列表**替换**目标房间的 `[monster]` 列表。
//
// 用途（唯一调用方：`dnfbridge/dungeon_tournament_entry.go`，且只在
// `DNFBRIDGE_DUNGEON_TOURNAMENT` 显式打开时才调用）：
//
// 厂商锦标赛把当轮对手放在 `run.Instance.Mechanisms.Tournament.PathActors`，
// **每条 `IsBlocking = true`**（Game\Dungeon\Tournament\TournamentDungeonRuntime.cs:507-513），
// 由 `TournamentDungeonApplicationService.TryProjectStartMap`（:86-107）投影进
// START_MAP 的 `Monsters`。本仓在 `BuildDungeonLayout` 之后、`NewDungeonRun` 之前
// 做同一件事，这样 `dungeonRoomScene`（dungeon_session.go:717-752）构造出来的
// 场景天然带 blocking 怪、`Cleared == false`，而不是"空房间 ⇒ 进场即通关"。
//
// 投影只影响本次 run 的这张 topology：它先把该房间的 `ResolvedMap` 复制一份再改，
// Resolver 缓存与其它房间不受影响。
func (t *DungeonTopology) ProjectTournamentRoomActors(
	coordinate RoomCoordinate,
	monsters []MonsterSpawn,
) error {
	if t == nil {
		return ErrRunTopologyRequired
	}
	key := coordinateKey{x: coordinate.X, y: coordinate.Y}
	room, ok := t.rooms[key]
	if !ok {
		return fmt.Errorf("%w: dungeon=%d maze=%d room=%s", ErrRoomNotFound, t.DungeonID, t.MazeIndex, coordinate)
	}
	if room.Map == nil {
		return fmt.Errorf("%w: dungeon=%d maze=%d room=%s", ErrRoomMapUnresolved, t.DungeonID, t.MazeIndex, coordinate)
	}
	projected := *room.Map
	projected.Map = cloneMap(room.Map.Map)
	projected.Map.Monsters = append([]MonsterSpawn(nil), monsters...)
	// 锦标赛的对手属于"敌方队伍"：`[monster team]` 段在这 5 张图里本就不存在，
	// 这里把队伍表一并清掉，保证 monsterSpawnBlocksRoom 走"缺失即阻挡"的旧口径。
	projected.Map.MonsterTeam = nil
	room.Map = &projected
	t.rooms[key] = room
	return nil
}
