package dnfbridge

import dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"

func (s *Service) currentDungeonLuckyStarPolicy(runtime *runtimeDungeonState) (*dnfdungeon.LuckyStarPolicy, error) {
	if !currentDungeonIsRecommendedClear(runtime) || runtime.Dungeon.Metadata.NoFatigue || runtime.Tournament != nil {
		return nil, nil
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	catalog, err := parseCurrentRentalCatalog(archive)
	if err != nil {
		return nil, err
	}
	return &dnfdungeon.LuckyStarPolicy{Limit: catalog.Limit, Amount: catalog.DungeonClearPoints}, nil
}

// The wallet has already been committed with experience under the completion
// receipt. A failed/repeated send must never award again. Reuse op985's complete
// wallet/rental snapshot, including active rentals, instead of 86JP op883/855.
func (s *Service) sendSuitableDungeonLuckyStarState(session *gameSession, runtime *runtimeDungeonState) {
	if session == nil || runtime == nil || !runtime.settlementLuckyStar.Resolved || runtime.settlementLuckyStarStateSent {
		return
	}
	result := runtime.settlementLuckyStar
	sent, err := s.sendSelectedCurrentRentalPointStateResult(session, "dungeon_settlement_after_lucky_star", true)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-lucky-star-state-send-failed", "dungeon_id", runtime.Dungeon.ID, "reason", err)
		return
	}
	if !sent {
		return
	}
	runtime.settlementLuckyStarStateSent = true
	s.logGameEvent(session, "game-dungeon-lucky-star-state-sent", "char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID, "entry_level", runtime.entryCharacterLevel, "points_before", result.Before, "points_after", result.After, "point_limit", result.Limit, "msg_id", currentRentalStateMsgID)
}
