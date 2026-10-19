package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"time"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentRoomClearQuestReceipt struct {
	Key  string
	At   time.Time
	Sent bool
}

// The caller holds session.dungeon.mu. This is a room objective, independent
// of the final-dungeon reward barrier. A retry uses the same room receipt.
func (s *Service) persistCurrentDungeonRoomClearQuestLocked(session *gameSession, runtime *runtimeDungeonState) error {
	if session == nil || runtime == nil || runtime.Session == nil || runtime.Room == nil ||
		session.dungeon.runtime != runtime || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return nil
	}
	snapshot := runtime.Session.Snapshot()
	scene := snapshot.Scene
	if snapshot.Run.Status != worldmap.DungeonRunActive || snapshot.Run.DungeonID != runtime.Dungeon.ID ||
		snapshot.Run.MazeIndex != runtime.MazeIndex || snapshot.Run.Current != scene.Coordinate ||
		!scene.Cleared || scene.Boss || scene.Map.Map.ID <= 0 || len(scene.ExpectedHostiles) == 0 || len(scene.DefeatedObjects) == 0 {
		return nil
	}
	if runtime.MazeIndex < 0 || runtime.MazeIndex >= len(runtime.Dungeon.Mazes) {
		return nil
	}
	connection := runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection
	if len(connection) < 2 || connection[0] != 0 || connection[1] <= 0 {
		return nil
	}
	if len(connection) > 2 && connection[2] >= 0 && int64(runtime.Request.Difficulty) < connection[2] {
		return nil
	}
	key := runtimeDungeonRoomKeyFromScene(scene)
	if runtime.roomClearQuestReceipts == nil {
		runtime.roomClearQuestReceipts = make(map[runtimeDungeonRoomKey]*currentRoomClearQuestReceipt)
	}
	receipt := runtime.roomClearQuestReceipts[key]
	if receipt != nil && receipt.Sent {
		return nil
	}
	if receipt == nil {
		now := s.gameplayNow()
		receipt = &currentRoomClearQuestReceipt{
			Key: fmt.Sprintf("room_clear/%d/%d/%d/%d/%d/%d/%d/%d", session.selectedCharacterID, runtime.Dungeon.ID, runtime.MazeIndex, runtime.startedAt.UnixNano(), key.X, key.Y, key.MapID, now.UnixNano()),
			At:  now,
		}
		runtime.roomClearQuestReceipts[key] = receipt
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return errDungeonClearMapCompletionUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load room-clear quest catalog: %w", err)
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return err
	}
	result, err := owner.ApplyRoomClearMapCompletion(ctx, catalog, strconv.FormatUint(uint64(session.selectedCharacterID), 10), dnfquest.RoomClearMapInput{
		DungeonID: runtime.Dungeon.ID, MapID: scene.Map.Map.ID, LinkedQuestIDs: []int64{connection[1]},
		CompletionKey: receipt.Key, CompletedAt: receipt.At,
	})
	if err != nil {
		return fmt.Errorf("persist room-clear quest: %w", err)
	}
	if len(result.Completions) > 0 {
		if err := s.sendCurrentLinkedObjectiveSnapshots(session, result.PostCommitQuest, "authoritative_middle_room_clear"); err != nil {
			return err
		}
	}
	receipt.Sent = true
	s.logGameEvent(session, "game-dungeon-room-clear-quest-committed",
		"dungeon_id", runtime.Dungeon.ID, "maze_index", runtime.MazeIndex, "map_id", scene.Map.Map.ID,
		"room", scene.Coordinate.String(), "quest_id", connection[1], "completion_key", receipt.Key,
		"applied", result.Applied, "idempotent", result.Idempotent, "reward_granted", false)
	return nil
}
