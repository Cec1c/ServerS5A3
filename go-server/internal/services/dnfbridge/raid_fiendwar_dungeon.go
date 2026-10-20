package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"time"
)

// Encounter reservations are server-owned and tied to the existing dungeon run.
type currentFiendWarEncounter struct {
	RunID        string
	Dungeon      int
	Participants []uint16
}

func currentFiendWarSelectionAllowed(b *runtimeRaidBattle, id uint16, dungeon int) bool {
	if b == nil || b.FiendWar == nil {
		return false
	}
	a, ok := b.FiendWar.AreaForDungeon(dungeon)
	return ok && b.MemberGroups[id] > 0 && b.FiendWar.MemberArea(id, b.MemberGroups[id]) == a.Index && b.FiendWar.AreaOpen(a.Index)
}
func reserveCurrentFiendWarDungeon(b *runtimeRaidBattle, id uint16, r *runtimeDungeonState, e currentRaidDungeonEntry, now time.Time) error {
	f := b.FiendWar
	a, ok := f.AreaForDungeon(int(r.Dungeon.ID))
	if !ok || !currentFiendWarSelectionAllowed(b, id, int(r.Dungeon.ID)) {
		return fmt.Errorf("超时空进图区域不符")
	}
	if b.FiendWarEncounters == nil {
		b.FiendWarEncounters = map[int]currentFiendWarEncounter{}
	}
	if old, found := b.FiendWarEncounters[a.Index]; found {
		if old.RunID == e.RunID && old.Dungeon == int(r.Dungeon.ID) && sameRaidParticipants(old.Participants, e.Participants) {
			return nil
		}
		return fmt.Errorf("超时空区域已有其他遭遇战")
	}
	if len(e.Participants) > a.MemberMax {
		return fmt.Errorf("超时空区域人数超过PVF")
	}
	for _, peer := range e.Participants {
		if !currentFiendWarSelectionAllowed(b, peer, int(r.Dungeon.ID)) {
			return fmt.Errorf("超时空同场成员区域不符")
		}
	}
	next := f.Clone()
	group := b.MemberGroups[id]
	next.GroupAreas[group] = a.Index
	if err := next.Enter(group, int(r.Dungeon.ID), now); err != nil {
		return err
	}
	b.FiendWar = next
	b.FiendWarEncounters[a.Index] = currentFiendWarEncounter{e.RunID, int(r.Dungeon.ID), append([]uint16(nil), e.Participants...)}
	b.ProgressVersion++
	syncCurrentFiendWarDungeons(b)
	return nil
}
func validateCurrentFiendWarDungeon(b *runtimeRaidBattle, id uint16, r *runtimeDungeonState) error {
	if r == nil || r.raidEntry == nil || b.FiendWar == nil {
		return fmt.Errorf("超时空副本绑定缺失")
	}
	a, ok := b.FiendWar.AreaForDungeon(int(r.Dungeon.ID))
	if !ok {
		return fmt.Errorf("超时空副本区域缺失")
	}
	e, ok := b.FiendWarEncounters[a.Index]
	if !ok || e.RunID != r.raidEntry.RunID || e.Dungeon != int(r.Dungeon.ID) || !sameRaidParticipants(e.Participants, r.raidEntry.Participants) || b.FiendWar.Areas[a.Index].Group == 0 || !time.Now().Before(b.FiendWar.Areas[a.Index].Deadline) {
		return fmt.Errorf("超时空遭遇战已过期或被替换")
	}
	if b.RunDepartures[r.raidEntry.RunID][id] {
		return fmt.Errorf("超时空报告者已退出本次遭遇战")
	}
	for _, peer := range e.Participants {
		if !b.RunDepartures[r.raidEntry.RunID][peer] && b.FiendWar.MemberArea(peer, b.MemberGroups[peer]) != a.Index {
			return fmt.Errorf("超时空遭遇战成员已移动")
		}
	}
	return nil
}
func (s *Service) selectCurrentFiendWarMaze(p *gameSession, d worldmap.Dungeon) (dungeonMazeSelection, error) {
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(p.selectedCharacterID)
	if state == nil || state.Battle == nil || !currentFiendWarSelectionAllowed(state.Battle, p.selectedCharacterID, int(d.ID)) {
		return dungeonMazeSelection{}, fmt.Errorf("超时空选图资格无效")
	}
	f := state.Battle.FiendWar
	a, _ := f.AreaForDungeon(int(d.ID))
	index := 0
	if a.Kind == "" {
		u := f.Areas[a.Index]
		if u.Enemy.ID != 0 {
			index = int(u.Enemy.Type) - 1
		} else if u.Monster.ID != 0 {
			index = int(u.Monster.Type) + 3
		} else {
			return dungeonMazeSelection{}, worldmap.ErrMazeNotFound
		}
	} else if fixed, ok := f.MazeForArea(a.Index); ok {
		index = fixed
	}
	if index < 0 || index >= len(d.Mazes) {
		return dungeonMazeSelection{}, worldmap.ErrMazeNotFound
	}
	return dungeonMazeSelection{Index: index, Reason: "超时空PVF区域怪物与设施对应地图"}, nil
}

const currentFiendWarBossCommand uint16 = 1528

func fiendWarGameplayModule() gameplayModuleDefinition {
	return gameplayModuleDefinition{Name: "fiendwar", UpperHandlers: map[uint16]gameplayHandler{currentFiendWarBossCommand: func(s *Service, p *gameSession, q gameplayRequest) error {
		if q.Classification != 1 {
			return nil
		}
		return s.handleCurrentFiendWarBoss(p, q.Body)
	}}}
}
func (s *Service) handleCurrentFiendWarBoss(p *gameSession, body []byte) error {
	if !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) || len(body) != 8 {
		return nil
	}
	kind, value := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint32(body[4:])
	if kind > 2 || value > 100 {
		return nil
	}
	p.dungeon.mu.Lock()
	r := p.dungeon.runtime
	if r == nil || r.raidEntry == nil || r.Session == nil || r.townReturnPending || !p.postStartMapPlayerStateSent || !dungeonRuntimeOwnsCharacter(r, p.selectedCharacterID) || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		p.dungeon.mu.Unlock()
		return nil
	}
	s.raidMu.Lock()
	state := s.raids[r.raidEntry.RaidKey]
	var snapshot runtimeRaidState
	if state != nil && state.Started && state.Battle != nil && state.Battle.InstanceID == r.raidEntry.InstanceID && state.Battle.FiendWar != nil && !state.Battle.PhaseCompleted && !state.Battle.TimedOut && state.Battle.MemberAccounts[p.selectedCharacterID] == s.accountIDForSession(p) && state.Battle.ChannelID == p.channel.ID {
		b := state.Battle
		if err := validateCurrentFiendWarDungeon(b, p.selectedCharacterID, r); err == nil {
			next := b.FiendWar.Clone()
			if err = next.BossReport(int(r.Dungeon.ID), int(kind), int(value), time.Now()); err == nil {
				b.FiendWar = next
				b.ProgressVersion++
				snapshot = cloneRuntimeRaidState(*state)
			}
		}
	}
	s.raidMu.Unlock()
	p.dungeon.mu.Unlock()
	if snapshot.Battle != nil {
		s.queueCurrentRaidDungeonProgress(snapshot)
	}
	return nil
}
func (s *Service) reduceCurrentFiendWarClearLocked(state *runtimeRaidState, p *gameSession, r *runtimeDungeonState, now time.Time) (runtimeRaidState, bool, error) {
	b := state.Battle
	if err := validateCurrentFiendWarDungeon(b, p.selectedCharacterID, r); err != nil {
		return runtimeRaidState{}, false, err
	}
	next := b.FiendWar.Clone()
	// 仅在上层已经确认本次副本死亡及完成账本后收尾；HP上报本身不产生通关。
	if err := next.ClearClientBossRoom(int(r.Dungeon.ID), now); err != nil {
		return runtimeRaidState{}, false, err
	}
	b.FiendWar = next
	syncCurrentFiendWarDungeons(b)
	if b.ClearedRuns == nil {
		b.ClearedRuns = map[string]currentRaidClearedRun{}
	}
	if b.MemberClears == nil {
		b.MemberClears = map[uint16]uint32{}
	}
	e := r.raidEntry
	b.ClearedRuns[e.RunID] = currentRaidClearedRun{uint32(r.Dungeon.ID), e.Phase, e.Attempt, append([]uint16(nil), e.Participants...)}
	for _, id := range e.Participants {
		b.MemberClears[id]++
	}
	a, _ := next.AreaForDungeon(int(r.Dungeon.ID))
	delete(b.FiendWarEncounters, a.Index)
	b.PhaseCompleted = next.Complete
	if next.Complete {
		b.PhaseCompletedAt = now
		if err := s.freezeCurrentFiendWarRewardFlowLocked(*state); err != nil {
			return cloneRuntimeRaidState(*state), true, err
		}
	}
	b.ProgressVersion++
	return cloneRuntimeRaidState(*state), true, nil
}

func (s *Service) cancelUncommittedCurrentFiendWarEntry(r *runtimeDungeonState) {
	if r == nil || r.raidEntry == nil || r.raidEntry.ChannelType != 42 {
		return
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[r.raidEntry.RaidKey]
	if state != nil && state.Battle != nil && state.Battle.InstanceID == r.raidEntry.InstanceID {
		s.releaseCurrentFiendWarEncounterLocked(state.Battle, r, 0)
	}
}
