package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func (s *Service) currentDayDungeonEntryEnabled(id int64) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && dnfdungeon.DayDungeonCharacterEntryLimit(id) != 0
}

func (s *Service) currentDayDungeonEntryCommand(ctx context.Context, character dnfrepo.CharacterRecord, dungeon worldmap.Dungeon, session *gameSession) (*dnfdungeon.DayDungeonEntryOwner, dnfdungeon.DayDungeonEntryCommand, error) {
	// All five published DGN assets are single-player and fatigue/ticket free.
	// Reject changed cost policy until it can join the same admission transaction.
	if !dungeon.Metadata.NoFatigue || len(currentTournamentEntryItems(dungeon)) != 0 || !dungeon.Metadata.LimitPartyCount.Set || dungeon.Metadata.LimitPartyCount.Value != 1 {
		return nil, dnfdungeon.DayDungeonEntryCommand{}, dnfdungeon.ErrEntryItemConsumeInvalid
	}
	if session == nil || session.selectedCharacterID == 0 || character.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) {
		return nil, dnfdungeon.DayDungeonEntryCommand{}, dnfdungeon.ErrEntryItemConsumeInvalid
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, dnfdungeon.DayDungeonEntryCommand{}, dnfdungeon.ErrOwnerUnavailable
	}
	owner, err := dnfdungeon.NewDayDungeonEntryOwner(repos)
	if err != nil {
		return nil, dnfdungeon.DayDungeonEntryCommand{}, err
	}
	summary, err := s.currentAccountAdventureGroupSummary(ctx, character, true, session)
	if err != nil {
		return nil, dnfdungeon.DayDungeonEntryCommand{}, err
	}
	s.adventureGroupMu.Lock()
	table := s.adventureGroupTable
	s.adventureGroupMu.Unlock()
	if table == nil {
		return nil, dnfdungeon.DayDungeonEntryCommand{}, errAdventureGroupTableUnavailable
	}
	limit, err := table.Runtime().DayDungeonAccountCharacterLimit(summary.ManageLevel)
	if err != nil {
		return nil, dnfdungeon.DayDungeonEntryCommand{}, fmt.Errorf("day-dungeon original account benefit: %w", err)
	}
	return owner, dnfdungeon.DayDungeonEntryCommand{AccountID: s.accountIDForSession(session), CharacterID: character.CharacterID, Dungeon: dungeon, AccountCharacterLimit: limit, ObservedAt: s.gameplayNow()}, nil
}

func (s *Service) validateCurrentDayDungeonEntry(ctx context.Context, character dnfrepo.CharacterRecord, dungeon worldmap.Dungeon, session *gameSession) error {
	if err := validateDungeonEntry(character, dungeon, session); err != nil {
		return err
	}
	owner, cmd, err := s.currentDayDungeonEntryCommand(ctx, character, dungeon, session)
	if err != nil {
		return err
	}
	_, err = owner.Status(ctx, cmd)
	return err
}

// Commit only after runtime preparation, packet preflight and the active-run
// guard. The domain owner rechecks the service day and both quotas atomically.
func (s *Service) consumeCurrentDayDungeonEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if !s.currentDayDungeonEntryEnabled(runtime.Dungeon.ID) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	owner, cmd, err := s.currentDayDungeonEntryCommand(ctx, runtime.Character, runtime.Dungeon, session)
	if err != nil {
		return err
	}
	result, err := owner.Consume(ctx, cmd)
	if err != nil {
		return err
	}
	runtime.Character = result.Character
	s.logGameEvent(session, "game-day-dungeon-entry-committed", "char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID, "character_used", result.Status.CharacterUsed, "character_limit", result.Status.CharacterLimit, "account_characters_used", result.Status.AccountCharactersUsed, "account_character_limit", result.Status.AccountCharacterLimit, "reset", "06:00_UTC+8")
	return nil
}
