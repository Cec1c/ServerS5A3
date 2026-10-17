package dnfbridge

import (
	"context"
	"errors"
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var errDungeonMapQuestRequired = errors.New("dungeon map unlock quest is incomplete")

// Scope is the current client's particular-hell map. Other worldmap marker
// forms need their own evidence before generalising this admission rule.
// The quest ID comes from the PVF's ordinary dungeon/quest pair, not hell quest.
func (s *Service) validateCurrentRiftUnlock(ctx context.Context, session *gameSession, dungeon worldmap.Dungeon) error {
	if !s.currentRiftEntryEnabled(dungeon.ID) {
		return nil
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return err
	}
	area, ok := table.FindAreaByDungeon(dungeon.ID)
	if !ok {
		return fmt.Errorf("rift unlock worldmap unavailable")
	}
	required := int64(0)
	for _, entry := range area.Dungeons {
		if entry.DungeonID != dungeon.ID || entry.InProgressOnly {
			continue
		}
		if entry.QuestID <= 0 || required != 0 {
			return fmt.Errorf("rift unlock quest definition unavailable or ambiguous")
		}
		required = entry.QuestID
	}
	if required == 0 {
		return fmt.Errorf("rift unlock quest definition missing")
	}
	id := sessionSelectedCharacterID(session)
	if id == 0 {
		return errDungeonCharacterRequired
	}
	record, _, err := s.dungeonQuestRecord(ctx, id)
	if err != nil {
		return err
	}
	_, cleared := dungeonQuestStateSets(record)
	if _, ok := cleared[required]; !ok {
		return fmt.Errorf("%w: character=%d dungeon=%d quest=%d", errDungeonMapQuestRequired, id, dungeon.ID, required)
	}
	return nil
}

// Read the authoritative roster before any member's prepare/asset phase. Each
// prepare repeats its own check, including direct and leader-planned entry.
func (s *Service) validateCurrentRiftPartyUnlock(ctx context.Context, session *gameSession, state alignedcmd.PartyState, dungeonID int64) error {
	if !s.currentRiftEntryEnabled(dungeonID) {
		return nil
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return err
	}
	d, ok := table.FindDungeon(dungeonID)
	if !ok {
		return worldmap.ErrDungeonNotIndexed
	}
	if err := s.validateCurrentRiftUnlock(ctx, session, d); err != nil {
		return err
	}
	if state.PartyID <= 0 || state.UserID != sessionSelectedCharacterID(session) {
		return nil
	}
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 || member.UserID == sessionSelectedCharacterID(session) {
			continue
		}
		// Only the selected character ID is needed; no per-session unlock latch.
		if err := s.validateCurrentRiftUnlock(ctx, &gameSession{selectedCharacterID: member.UserID}, d); err != nil {
			return err
		}
	}
	return nil
}
