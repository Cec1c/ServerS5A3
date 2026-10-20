package dnfbridge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentDungeonQuestPassiveDropMarkerOffset = 37

type currentDungeonPassiveQuestDropEventKey struct {
	Connection string
	Sequence   uint64
}

type currentDungeonPassiveQuestDropReceipt struct {
	Room          runtimeDungeonRoomKey
	EnemyCode     int64
	BodySignature [32]byte
	Resolved      bool
	Granted       bool
	Refreshed     bool
	Slots         []int16
}

// handleCurrentDungeonQuestPassiveDropDeathLocked accepts only the native
// official-24.9 passive-object report shape. The connection sequence identifies
// the event because the 70-byte writer body can collide for distinct objects.
// The decoded request includes the additional four-byte network footer.
func (s *Service) handleCurrentDungeonQuestPassiveDropDeathLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	request dungeoncmd.DieMonsterRequest,
	body []byte,
	sequence uint64,
) (bool, error) {
	if s == nil || session == nil || runtime == nil || runtime.Room == nil ||
		s.options.currentClientVersion != "official-24.9" ||
		request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat ||
		len(body) != dungeoncmd.Official249DieMonsterBaseSize ||
		request.OwnerObjectKey != ^uint16(0) ||
		request.CombatEntryCount != 0 ||
		body[currentDungeonQuestPassiveDropMarkerOffset] != 1 {
		return false, nil
	}

	enemyCode := int64(request.RuntimeObjectKey)
	sourceCount := currentDungeonPassiveObjectSourceCount(scene, enemyCode)
	room := runtimeDungeonRoomKeyFromScene(scene)
	eventKey := currentDungeonPassiveQuestDropEventKeyFor(session, sequence)
	signature := sha256.Sum256(body)
	if runtime.passiveQuestDropReceipts == nil {
		runtime.passiveQuestDropReceipts = make(
			map[currentDungeonPassiveQuestDropEventKey]currentDungeonPassiveQuestDropReceipt,
		)
	}

	receipt, known := runtime.passiveQuestDropReceipts[eventKey]
	if known {
		if receipt.Room != room || receipt.EnemyCode != enemyCode || receipt.BodySignature != signature {
			s.logGameEvent(session, "game-dungeon-quest-passive-drop-rejected",
				"sequence", sequence,
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"enemy_code", enemyCode,
				"reason", "sequence_reused_with_changed_event_binding")
			return true, nil
		}
		if receipt.Resolved {
			if receipt.Granted && !receipt.Refreshed {
				if err := s.refreshCurrentDungeonPassiveQuestDropReceiptLocked(
					session,
					runtime,
					eventKey,
					receipt,
				); err != nil {
					return true, err
				}
			}
			return true, nil
		}
		// A registered transaction failure is retryable even when later packets
		// have advanced the connection high-water mark.
	} else {
		high, initialized := session.guild.purchaseSequence.currentHigh()
		if !initialized || high != sequence {
			s.logGameEvent(session, "game-dungeon-quest-passive-drop-rejected",
				"sequence", sequence,
				"current_high", high,
				"initialized", initialized,
				"reason", "unseen_sequence_is_not_current_high_watermark")
			return true, nil
		}
		if sourceCount == 0 {
			s.logGameEvent(session, "game-dungeon-quest-passive-drop-rejected",
				"sequence", sequence,
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"enemy_code", enemyCode,
				"reason", "passive_object_not_placed_in_current_room")
			return true, nil
		}

		if runtime.passiveQuestDropSourceUsage == nil {
			runtime.passiveQuestDropSourceUsage = make(
				map[runtimeDungeonRoomKey]map[int64]int,
			)
		}
		usage := runtime.passiveQuestDropSourceUsage[room]
		if usage == nil {
			usage = make(map[int64]int)
			runtime.passiveQuestDropSourceUsage[room] = usage
		}
		if usage[enemyCode] >= sourceCount {
			s.logGameEvent(session, "game-dungeon-quest-passive-drop-rejected",
				"sequence", sequence,
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"enemy_code", enemyCode,
				"source_count", sourceCount,
				"registered_events", usage[enemyCode],
				"reason", "source_quota_exhausted")
			return true, nil
		}
		usage[enemyCode]++
		receipt = currentDungeonPassiveQuestDropReceipt{
			Room:          room,
			EnemyCode:     enemyCode,
			BodySignature: signature,
		}
		runtime.passiveQuestDropReceipts[eventKey] = receipt
	}

	result, err := s.grantCurrentDungeonQuestEnemyDrops(
		session,
		runtime,
		currentDungeonQuestEnemyTypePassiveObject,
		enemyCode,
		currentDungeonPassiveQuestDropRNGSeed(runtime, eventKey, room, enemyCode),
	)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-quest-passive-drop-save-failed",
			"sequence", sequence,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"enemy_code", enemyCode,
			"error", err)
		return true, err
	}
	if !result.Resolved {
		return true, errors.New("dungeon quest passive drop did not resolve")
	}
	receipt.Resolved = true
	receipt.Granted = result.Granted
	receipt.Slots = append([]int16(nil), result.Slots...)
	runtime.passiveQuestDropReceipts[eventKey] = receipt
	if !result.Granted {
		s.logGameEvent(session, "game-dungeon-quest-passive-drop-resolved",
			"sequence", sequence,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"enemy_code", enemyCode,
			"reason", "no_matching_active_type3_reward_or_probability_miss")
		return true, nil
	}
	if err := s.sendCurrentDungeonQuestDropRefresh(
		session,
		result.Inventory,
		result.Slots,
		enemyCode,
	); err != nil {
		s.logGameEvent(session, "game-dungeon-quest-passive-drop-refresh-failed",
			"sequence", sequence,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"enemy_code", enemyCode,
			"error", err)
		return true, err
	}
	receipt.Refreshed = true
	runtime.passiveQuestDropReceipts[eventKey] = receipt
	s.logGameEvent(session, "game-dungeon-quest-passive-drop-committed",
		"sequence", sequence,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"enemy_code", enemyCode,
		"slots", receipt.Slots)
	return true, nil
}

func currentDungeonPassiveObjectSourceCount(scene worldmap.DungeonRoomScene, code int64) int {
	count := 0
	for _, object := range scene.PassiveObjects {
		if object.ObjectID == code {
			count++
		}
	}
	for _, object := range scene.SpecialPassiveObjects {
		if object.ObjectID == code {
			count++
		}
	}
	return count
}

func currentDungeonPassiveQuestDropEventKeyFor(
	session *gameSession,
	sequence uint64,
) currentDungeonPassiveQuestDropEventKey {
	connection := ""
	if session != nil {
		connection = session.connID
		if connection == "" {
			connection = "character:" + strconv.FormatUint(uint64(session.selectedCharacterID), 10)
		}
	}
	return currentDungeonPassiveQuestDropEventKey{
		Connection: connection,
		Sequence:   sequence,
	}
}

func currentDungeonPassiveQuestDropRNGSeed(
	runtime *runtimeDungeonState,
	key currentDungeonPassiveQuestDropEventKey,
	room runtimeDungeonRoomKey,
	enemyCode int64,
) uint32 {
	hash := fnv.New32a()
	_, _ = fmt.Fprintf(hash,
		"%d|%d|%s|%d|%d|%d|%d|%d|%d",
		runtime.startedAt.UnixNano(),
		runtime.Seed,
		key.Connection,
		key.Sequence,
		runtime.Dungeon.ID,
		runtime.MazeIndex,
		room.X,
		room.Y,
		room.MapID,
	)
	_, _ = fmt.Fprintf(hash, "|%d", enemyCode)
	return hash.Sum32()
}

func (s *Service) refreshCurrentDungeonPassiveQuestDropReceiptLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	eventKey currentDungeonPassiveQuestDropEventKey,
	receipt currentDungeonPassiveQuestDropReceipt,
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
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("dungeon quest passive drop inventory was not found")
	}
	if err := s.sendCurrentDungeonQuestDropRefresh(
		session,
		inventory,
		receipt.Slots,
		receipt.EnemyCode,
	); err != nil {
		return err
	}
	receipt.Refreshed = true
	runtime.passiveQuestDropReceipts[eventKey] = receipt
	return nil
}
