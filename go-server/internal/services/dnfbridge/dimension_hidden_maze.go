package dnfbridge

import (
	"fmt"
	"math"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentDimensionHiddenPolicy struct {
	WeeklyLimit     int64
	AppearanceRates []int
}

func parseCurrentDimensionHiddenPolicy(doc *dnfpvf.Document) (currentDimensionHiddenPolicy, error) {
	var p currentDimensionHiddenPolicy
	weekly, err := currentDimensionPolicyScalar(doc, "hidden boss appear count on week")
	if err != nil {
		return p, err
	}
	if weekly <= 0 || weekly > 255 {
		return p, fmt.Errorf("invalid dimension weekly reward limit %d", weekly)
	}
	rows, ok := doc.Section("hidden boss appear rate by enter count")
	if !ok || len(rows) == 0 || len(rows)%2 != 0 {
		return p, fmt.Errorf("invalid dimension appearance table")
	}
	for i := 0; i < len(rows); i += 2 {
		if rows[i].Kind != dnfpvf.TokenInt || rows[i+1].Kind != dnfpvf.TokenInt || rows[i].Int != int64(i/2+1) || rows[i+1].Int < 0 || rows[i+1].Int > 10000 {
			return p, fmt.Errorf("invalid dimension appearance row %d", i/2)
		}
		p.AppearanceRates = append(p.AppearanceRates, int(rows[i+1].Int))
	}
	if p.AppearanceRates[len(p.AppearanceRates)-1] != 10000 {
		return p, fmt.Errorf("dimension appearance table lacks terminal guarantee")
	}
	p.WeeklyLimit = weekly
	return p, nil
}

// The leader freezes one maze for the group. Each participant commits their
// own encounter sequence and weekly reward eligibility with their entry assets.
// Reaching the weekly reward cap never disables hidden-maze selection.
func (s *Service) selectCurrentDimensionHiddenMaze(dungeon worldmap.Dungeon, character dnfrepo.CharacterRecord, ordinary func() (dungeonMazeSelection, error), frozenParty bool) (dungeonMazeSelection, *dnfdungeon.DimensionHiddenAdmission, error) {
	hidden := make([]int, 0, 1)
	for i, maze := range dungeon.Mazes {
		if maze.HiddenBoss {
			hidden = append(hidden, i)
		}
	}
	if len(hidden) == 0 || !s.currentDimensionGateDailyEntryEnabled(dungeon) {
		selected, err := ordinary()
		return selected, nil, err
	}
	doc, err := s.currentDimensionInfoDocument()
	if err != nil {
		return dungeonMazeSelection{}, nil, err
	}
	policy, err := parseCurrentDimensionHiddenPolicy(doc)
	if err != nil {
		return dungeonMazeSelection{}, nil, err
	}
	state, err := dnfdungeon.DimensionHiddenSnapshot(character, s.gameplayNow())
	if err != nil {
		return dungeonMazeSelection{}, nil, err
	}
	if state.EntriesSinceEncounter == math.MaxInt64 {
		return dungeonMazeSelection{}, nil, dnfdungeon.ErrEntryItemConsumeInvalid
	}
	plan := &dnfdungeon.DimensionHiddenAdmission{ExpectedSequence: state.Sequence, WeeklyRewardLimit: policy.WeeklyLimit}
	var selection dungeonMazeSelection
	if frozenParty {
		selection, err = ordinary()
		if err != nil {
			return selection, nil, err
		}
		if selection.Index < 0 || selection.Index >= len(dungeon.Mazes) {
			return selection, nil, worldmap.ErrMazeNotFound
		}
		plan.Encountered = dungeon.Mazes[selection.Index].HiddenBoss
		return selection, plan, nil
	}
	ordinal := state.EntriesSinceEncounter
	if ordinal >= int64(len(policy.AppearanceRates)) {
		ordinal = int64(len(policy.AppearanceRates) - 1)
	}
	roll, err := s.chooseDungeonIndex(10000)
	if err != nil {
		return selection, nil, err
	}
	if roll < 0 || roll >= 10000 {
		return selection, nil, fmt.Errorf("dimension roll outside PVF denominator")
	}
	if roll < policy.AppearanceRates[ordinal] {
		index, err := chooseDungeonCandidate(hidden, s.chooseDungeonIndex)
		if err != nil {
			return selection, nil, err
		}
		plan.Encountered = true
		return dungeonMazeSelection{Index: index, Reason: "dimension_persistent_encounter_probability"}, plan, nil
	}
	selection, err = ordinary()
	return selection, plan, err
}
