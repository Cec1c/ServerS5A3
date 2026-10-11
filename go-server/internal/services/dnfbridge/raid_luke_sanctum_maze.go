package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"time"
)

type currentLukeSanctumMazeGroup struct {
	InstanceID    string
	Phase         byte
	Attempt       uint64
	Record        int
	Configuration raid.LukeSanctumMazeConfiguration
}

type currentLukeSanctumMazePreparation struct {
	RaidKey    uint32
	InstanceID string
	Phase      byte
	Attempt    uint64
	Record     int
	DungeonID  uint32
	Ordinal    uint32
	MazeIndex  int
}

// The first existing server maze choice selects a resource triplet once. Every
// subsequent preparation reads the accepted clear ordinal, never a client field.
func (s *Service) selectCurrentLukeSanctumMaze(session *gameSession, dungeon worldmap.Dungeon, choose func() (dungeonMazeSelection, error), frozenParty bool) (dungeonMazeSelection, *currentLukeSanctumMazePreparation, error) {
	var selection dungeonMazeSelection
	ordinary := func() (dungeonMazeSelection, *currentLukeSanctumMazePreparation, error) {
		selected, err := choose()
		return selected, nil, err
	}
	if !s.currentRaidDungeonBindingRequired(session) || dungeon.ID < 5054 || dungeon.ID > 5056 {
		return ordinary()
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil {
		return selection, nil, fmt.Errorf("Luke maze admitted battle unavailable")
	}
	b := state.Battle
	if b.Mode != 2 || b.Single || b.Phase != 0 {
		return ordinary()
	}
	now := time.Now()
	if !s.currentRaidAuthenticated(session) || b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type ||
		b.MemberAccounts[session.selectedCharacterID] == "" || b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) ||
		!currentRaidHasTwoPhaseProgram(b) || b.TimedOut || b.PhaseCompleted || now.Before(b.EnterAfter) || !now.Before(b.Deadline) {
		return selection, nil, fmt.Errorf("Luke maze admitted owner expired")
	}
	attempt := b.Anton.Epoch()
	if b.LukeSanctumMazeGroup == nil || b.LukeSanctumMazeGroup.InstanceID != b.InstanceID || b.LukeSanctumMazeGroup.Phase != b.Phase || b.LukeSanctumMazeGroup.Attempt != attempt {
		var err error
		selection, err = choose()
		if err != nil {
			return selection, nil, err
		}
		s.initialEquipmentMu.Lock()
		archive, err := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if err != nil {
			return selection, nil, err
		}
		config, err := raid.LoadLukeSanctumMazeConfiguration(archive)
		if err != nil {
			return selection, nil, err
		}
		record, err := config.RecordForMaze(uint32(dungeon.ID), selection.Index, len(dungeon.Mazes))
		if err != nil {
			return selection, nil, err
		}
		for _, id := range config.Dungeons {
			if b.Anton.ClearCount(id) != 0 {
				return selection, nil, fmt.Errorf("Luke maze group missing after accepted clear")
			}
		}
		b.LukeSanctumMazeGroup = &currentLukeSanctumMazeGroup{InstanceID: b.InstanceID, Phase: b.Phase, Attempt: attempt, Record: record, Configuration: config.Clone()}
	}
	group := b.LukeSanctumMazeGroup
	component, ok := group.Configuration.Component(uint32(dungeon.ID))
	ordinal := b.Anton.ClearCount(uint32(dungeon.ID))
	if !ok || group.Record < 0 || group.Record >= len(group.Configuration.Records) || ordinal >= 3 {
		return selection, nil, fmt.Errorf("Luke maze accepted ordinal unavailable")
	}
	if frozenParty {
		var err error
		selection, err = choose()
		if err != nil {
			return selection, nil, err
		}
	}
	maze := int(group.Configuration.Records[group.Record][component]) + int(ordinal)
	if record, err := group.Configuration.RecordForMaze(uint32(dungeon.ID), maze, len(dungeon.Mazes)); err != nil || record != group.Record {
		return selection, nil, fmt.Errorf("Luke frozen triplet does not match actual dungeon")
	}
	if maze < 0 || maze >= len(dungeon.Mazes) || (frozenParty && selection.Index != maze) {
		return selection, nil, fmt.Errorf("Luke maze prepared party selection changed")
	}
	selection.Index, selection.Reason = maze, "luke_battle_triplet_accepted_clear_ordinal"
	return selection, &currentLukeSanctumMazePreparation{RaidKey: state.RaidKey, InstanceID: b.InstanceID, Phase: b.Phase, Attempt: attempt, Record: group.Record, DungeonID: uint32(dungeon.ID), Ordinal: ordinal, MazeIndex: maze}, nil
}

// Caller holds raidMu. This witness rejects a phase/clear change between maze
// preparation and group reservation, before any member runtime is committed.
func validateCurrentLukeSanctumMazePreparation(state *runtimeRaidState, runtime *runtimeDungeonState) error {
	witness := runtime.lukeSanctumMaze
	if state == nil || state.Battle == nil {
		return fmt.Errorf("Luke prepared maze battle unavailable")
	}
	b := state.Battle
	group := b.LukeSanctumMazeGroup
	if witness == nil {
		if group != nil && b.Mode == 2 && !b.Single && b.Phase == 0 && runtime.Dungeon.ID >= 5054 && runtime.Dungeon.ID <= 5056 {
			return fmt.Errorf("Luke prepared maze witness missing")
		}
		return nil
	}
	if b.Mode != 2 || b.Single || b.Phase != 0 || b.Anton == nil || group == nil ||
		witness.RaidKey != state.RaidKey || witness.InstanceID != b.InstanceID || witness.Phase != b.Phase || witness.Attempt != b.Anton.Epoch() ||
		group.InstanceID != b.InstanceID || group.Phase != b.Phase || group.Attempt != witness.Attempt || group.Record != witness.Record ||
		int64(witness.DungeonID) != runtime.Dungeon.ID || witness.Ordinal != b.Anton.ClearCount(witness.DungeonID) || witness.Ordinal >= 3 || witness.MazeIndex != runtime.MazeIndex {
		return fmt.Errorf("Luke prepared maze owner or ordinal changed")
	}
	component, ok := group.Configuration.Component(witness.DungeonID)
	if !ok || group.Record < 0 || group.Record >= len(group.Configuration.Records) || int(group.Configuration.Records[group.Record][component])+int(witness.Ordinal) != runtime.MazeIndex {
		return fmt.Errorf("Luke prepared maze triplet changed")
	}
	return nil
}
