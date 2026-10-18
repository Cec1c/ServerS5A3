package dnfbridge

import (
	"context"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The bonus belongs to the accepted epic quest selected by the server's PVF
// maze owner. Neither an opaque request field nor a previously cleared quest
// can activate it. Capture once, before this run can finish the quest.
type currentDungeonStoryExperienceProfile struct {
	QuestID     int64
	RatePercent int64
}

func (s *Service) captureCurrentDungeonStoryExperience(ctx context.Context, dungeon worldmap.Dungeon, selection dungeonMazeSelection, difficulty byte, record dnfrepo.QuestRecord) currentDungeonStoryExperienceProfile {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return currentDungeonStoryExperienceProfile{}
	}
	mode := dungeon.Metadata.StoryMode
	if mode == nil || mode.Malformed || selection.Reason != "active_quest_connection" || selection.QuestID <= 0 || int64(difficulty) >= mode.DifficultySize || int(difficulty) >= len(mode.IncreaseExperienceRates) {
		return currentDungeonStoryExperienceProfile{}
	}
	if selection.Index < 0 || selection.Index >= len(dungeon.Mazes) {
		return currentDungeonStoryExperienceProfile{}
	}
	connection := dungeon.Mazes[selection.Index].QuestConnection
	if len(connection) < 2 || connection[0] != 0 || connection[1] != selection.QuestID {
		return currentDungeonStoryExperienceProfile{}
	}
	active, _ := dungeonQuestStateSets(record)
	if _, ok := active[selection.QuestID]; !ok {
		return currentDungeonStoryExperienceProfile{}
	}
	declared := false
	for _, questID := range mode.QuestIDs {
		declared = declared || questID == selection.QuestID
	}
	if !declared {
		return currentDungeonStoryExperienceProfile{}
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return currentDungeonStoryExperienceProfile{}
	}
	definition, ok := catalog.Find(selection.QuestID)
	if !ok || strings.Trim(strings.ToLower(strings.TrimSpace(definition.Grade)), "[]") != "epic" {
		return currentDungeonStoryExperienceProfile{}
	}
	rate := mode.IncreaseExperienceRates[int(difficulty)]
	if rate < 0 {
		return currentDungeonStoryExperienceProfile{}
	}
	return currentDungeonStoryExperienceProfile{QuestID: selection.QuestID, RatePercent: rate}
}

func currentDungeonStoryExperienceMultiplier(runtime *runtimeDungeonState) float64 {
	if runtime == nil || runtime.storyExperience.QuestID <= 0 || runtime.storyExperience.RatePercent <= 0 {
		return 1
	}
	return 1 + float64(runtime.storyExperience.RatePercent)/100
}
