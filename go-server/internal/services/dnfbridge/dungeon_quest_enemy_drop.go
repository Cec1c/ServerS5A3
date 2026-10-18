package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentDungeonQuestDropEventKey struct {
	RunStartedAt int64
	RunSeed      uint32
	CharacterID  uint16
	DungeonID    int64
	MazeIndex    int
	Difficulty   int64
	Room         runtimeDungeonRoomKey
	ObjectKey    uint32
	EnemyType    int64
	EnemyCode    int64
}

type currentDungeonQuestDropReceipt struct {
	Resolved  bool
	Granted   bool
	Refreshed bool
	Slots     []int16
}

// settleCurrentDungeonRewardEnemyQuestDropLocked runs only after the room has
// accepted the actor death (initial commit or already-defeated replay). Type 2
// is the sole new branch here; Type 3 remains owned by the quest passive-object
// path, while ordinary monsters continue through the existing type-1 caller.
func (s *Service) settleCurrentDungeonRewardEnemyQuestDropLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	objectKey uint32,
) error {
	if s == nil || session == nil || runtime == nil || runtime.Room == nil ||
		runtime.Session == nil || objectKey == 0 {
		return nil
	}
	target, defeated := runtime.Room.DefeatedDeathTarget(objectKey)
	if !defeated {
		return nil
	}
	enemy, matched := runtime.Room.RewardEnemyByReference(
		target.Reference,
		runtimeDungeonMonsterAnnounced,
		runtimeDungeonMonsterDefeated,
	)
	if !matched || enemy.EnemyType != 2 {
		return nil
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		return worldmap.ErrDungeonRunRequired
	}
	key := currentDungeonQuestDropEventKeyFor(session, runtime, scene, objectKey, enemy)
	if runtime.questDropReceipts != nil {
		if receipt, found := runtime.questDropReceipts[key]; found {
			return s.refreshCurrentDungeonQuestDropReceiptLocked(session, runtime, key, receipt)
		}
	}

	result, err := s.grantCurrentDungeonQuestEnemyDrops(
		session,
		runtime,
		enemy.EnemyType,
		enemy.EnemyCode,
		currentDungeonQuestDropRNGSeed(key),
	)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-quest-enemy-drop-save-failed",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_key", objectKey,
			"enemy_type", enemy.EnemyType,
			"enemy_code", enemy.EnemyCode,
			"error", err)
		return err
	}
	if !result.Resolved {
		return errors.New("dungeon quest enemy drop did not resolve")
	}
	receipt := currentDungeonQuestDropReceipt{
		Resolved: true,
		Granted:  result.Granted,
		Slots:    append([]int16(nil), result.Slots...),
	}
	if runtime.questDropReceipts == nil {
		runtime.questDropReceipts = make(map[currentDungeonQuestDropEventKey]currentDungeonQuestDropReceipt)
	}
	runtime.questDropReceipts[key] = receipt
	if !result.Granted {
		return nil
	}
	if err := s.sendCurrentDungeonQuestDropRefresh(
		session,
		result.Inventory,
		result.Slots,
		enemy.EnemyCode,
	); err != nil {
		s.logGameEvent(session, "game-dungeon-quest-enemy-drop-refresh-failed",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_key", objectKey,
			"enemy_type", enemy.EnemyType,
			"enemy_code", enemy.EnemyCode,
			"error", err)
		return err
	}
	receipt.Refreshed = true
	runtime.questDropReceipts[key] = receipt
	s.logGameEvent(session, "game-dungeon-quest-enemy-drop-committed",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"object_key", objectKey,
		"enemy_type", enemy.EnemyType,
		"enemy_code", enemy.EnemyCode,
		"slots", receipt.Slots)
	return nil
}

func currentDungeonQuestDropRNGSeed(key currentDungeonQuestDropEventKey) uint32 {
	hash := fnv.New32a()
	_, _ = fmt.Fprintf(hash,
		"%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d",
		key.RunStartedAt,
		key.RunSeed,
		key.CharacterID,
		key.DungeonID,
		key.MazeIndex,
		key.Difficulty,
		key.Room.X,
		key.Room.Y,
		key.Room.MapID,
		key.ObjectKey,
		key.EnemyType,
		key.EnemyCode,
	)
	return hash.Sum32()
}

func currentDungeonQuestDropEventKeyFor(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	enemy runtimeDungeonRewardEnemy,
) currentDungeonQuestDropEventKey {
	return currentDungeonQuestDropEventKey{
		RunStartedAt: runtime.startedAt.UnixNano(),
		RunSeed:      runtime.Seed,
		CharacterID:  session.selectedCharacterID,
		DungeonID:    int64(runtime.Dungeon.ID),
		MazeIndex:    runtime.MazeIndex,
		Difficulty:   int64(runtime.Request.Difficulty),
		Room:         runtimeDungeonRoomKeyFromScene(scene),
		ObjectKey:    objectKey,
		EnemyType:    enemy.EnemyType,
		EnemyCode:    enemy.EnemyCode,
	}
}

func (s *Service) refreshCurrentDungeonQuestDropReceiptLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	key currentDungeonQuestDropEventKey,
	receipt currentDungeonQuestDropReceipt,
) error {
	if !receipt.Resolved || !receipt.Granted || receipt.Refreshed {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		return errDungeonQuestRepositoryUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := fmt.Sprintf("%d", key.CharacterID)
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found {
		return dnfrepo.ErrRepoMissing
	}
	if err := s.sendCurrentDungeonQuestDropRefresh(session, inventory, receipt.Slots, key.EnemyCode); err != nil {
		return err
	}
	receipt.Refreshed = true
	runtime.questDropReceipts[key] = receipt
	return nil
}
