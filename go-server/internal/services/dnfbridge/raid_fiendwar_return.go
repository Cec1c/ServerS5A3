package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"strings"
	"time"
)

// Called with raidMu. The exact run owns cleanup; an old runtime cannot free
// a replacement encounter in the same area.
func (s *Service) releaseCurrentFiendWarEncounterLocked(b *runtimeRaidBattle, r *runtimeDungeonState, leaver uint16) {
	if b.FiendWar == nil || r == nil || r.raidEntry == nil {
		return
	}
	a, ok := b.FiendWar.AreaForDungeon(int(r.Dungeon.ID))
	if !ok {
		return
	}
	e, ok := b.FiendWarEncounters[a.Index]
	if !ok || e.RunID != r.raidEntry.RunID {
		return
	}
	if e.Reservation != nil {
		return // The entry owner's deferred cancellation restores this reservation.
	}
	if leaver != 0 {
		if b.RunDepartures == nil {
			b.RunDepartures = map[string]map[uint16]bool{}
		}
		if b.RunDepartures[r.raidEntry.RunID] == nil {
			b.RunDepartures[r.raidEntry.RunID] = map[uint16]bool{}
		}
		b.RunDepartures[r.raidEntry.RunID][leaver] = true
		for id, entry := range b.Participation {
			if id != leaver && entry.RunID == r.raidEntry.RunID && entry.Runtime != nil {
				return
			}
		}
	}
	next := b.FiendWar.Clone()
	if err := next.Abandon(int(r.Dungeon.ID), time.Now()); err != nil {
		return
	}
	// A real boss retreat may eject the other room via PVF. Preserve that
	// exact run's party during its ensuing town transition, as for op42.
	for area, encounter := range b.FiendWarEncounters {
		if encounter.RunID != e.RunID && b.FiendWar.Areas[area].Group != 0 && next.Areas[area].Group == 0 {
			if b.FiendWarRetreatRuns == nil {
				b.FiendWarRetreatRuns = map[string]bool{}
			}
			b.FiendWarRetreatRuns[encounter.RunID] = true
		}
	}
	b.FiendWar = next
	delete(b.FiendWarEncounters, a.Index)
	delete(b.FiendWarRetreatRuns, r.raidEntry.RunID)
	b.ProgressVersion++
	syncCurrentFiendWarDungeons(b)
}
func (s *Service) prepareCurrentFiendWarReturn(p *gameSession, r *runtimeDungeonState) (currentDungeonTownTransition, error) {
	if !s.currentRaidAuthenticated(p) || r == nil || r.raidEntry == nil || !dungeonRuntimeOwnsCharacter(r, p.selectedCharacterID) {
		return currentDungeonTownTransition{}, fmt.Errorf("超时空回城角色不符")
	}
	s.raidMu.Lock()
	state := s.raids[r.raidEntry.RaidKey]
	if state == nil || state.Battle == nil || state.Battle.FiendWar == nil || state.Battle.InstanceID != r.raidEntry.InstanceID || state.Battle.MemberAccounts[p.selectedCharacterID] != s.accountIDForSession(p) {
		s.raidMu.Unlock()
		return currentDungeonTownTransition{}, fmt.Errorf("超时空回城战场已更换")
	}
	b := state.Battle
	f := b.FiendWar
	area := f.Config.WaitingArea
	a, found := f.AreaForDungeon(int(r.Dungeon.ID))
	_, cleared := b.ClearedRuns[r.raidEntry.RunID]
	if found && cleared && !b.PhaseCompleted && !b.TimedOut && !strings.HasSuffix(a.Kind, "boss") {
		area = a.Index
	}
	town := f.Config.WaitingTown
	s.raidMu.Unlock()
	target, ok := s.townArea(int64(town), int64(area))
	if !ok || target.MapPath == "" {
		return currentDungeonTownTransition{}, fmt.Errorf("超时空真实PVF回城落点缺失 %d/%d", town, area)
	}
	origin := r.townReturnOrigin
	if origin.CharacterID == p.selectedCharacterID && origin.PositionValid && int(origin.TownID) == town && int(origin.AreaID) == area {
		row := currentSceneTransitionRow{ObjectOrResourceKey: currentSceneActorObjectKey(p.selectedCharacterID), Value1: origin.PositionX, Value2: origin.PositionY, Value3: origin.MovementCode}
		body, err := buildCurrentSceneTransitionBody(byte(town), byte(area), []currentSceneTransitionRow{row})
		if err != nil {
			return currentDungeonTownTransition{}, err
		}
		return currentDungeonTownTransition{TownID: byte(town), AreaID: byte(area), ActorObjectKey: row.ObjectOrResourceKey, PositionX: int16(origin.PositionX), PositionY: int16(origin.PositionY), Direction: origin.MovementCode, PositionSource: "超时空已冻结区域原点", Body: body}, nil
	}
	x, y, err := s.currentFiendWarLanding(town, area)
	if err != nil {
		return currentDungeonTownTransition{}, err
	}
	if x < -32768 || x > 32767 || y < -32768 || y > 32767 {
		return currentDungeonTownTransition{}, fmt.Errorf("超时空回城坐标越界")
	}
	row := currentSceneTransitionRow{ObjectOrResourceKey: currentSceneActorObjectKey(p.selectedCharacterID), Value1: uint16(int16(x)), Value2: uint16(int16(y))}
	body, err := buildCurrentSceneTransitionBody(byte(town), byte(area), []currentSceneTransitionRow{row})
	if err != nil {
		return currentDungeonTownTransition{}, err
	}
	return currentDungeonTownTransition{TownID: byte(town), AreaID: byte(area), ActorObjectKey: row.ObjectOrResourceKey, PositionX: int16(x), PositionY: int16(y), PositionSource: "超时空PVF区域落点", Body: body}, nil
}

// Called only after the typed op24 was confirmed by a town-side client report.
func (s *Service) commitCurrentFiendWarReturnLocked(p *gameSession, r *runtimeDungeonState) error {
	if r == nil || r.raidEntry == nil || r.raidEntry.ChannelType != 42 {
		return nil
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	bstate := s.raids[r.raidEntry.RaidKey]
	if bstate == nil || bstate.Battle == nil || bstate.Battle.FiendWar == nil || bstate.Battle.InstanceID != r.raidEntry.InstanceID || bstate.Battle.MemberAccounts[p.selectedCharacterID] != s.accountIDForSession(p) {
		return fmt.Errorf("超时空回城提交归属失效")
	}
	b := bstate.Battle
	next := b.FiendWar.Clone()
	next.MemberAreas[p.selectedCharacterID] = int(r.townReturnTransition.AreaID)
	if r.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
		next.ImprisonMember(p.selectedCharacterID, time.Now())
	}
	b.FiendWar = next
	b.ProgressVersion++
	return nil
}
func (s *Service) queueCurrentFiendWarMaintenance() {
	s.raidMu.Lock()
	var ids []uint16
	for _, state := range s.raids {
		if state == nil || !state.Started || state.Battle == nil || state.Battle.FiendWar == nil {
			continue
		}
		for _, m := range state.Members {
			ids = append(ids, m.CharID)
		}
	}
	s.raidMu.Unlock()
	var peers []*gameSession
	for _, id := range ids {
		if p, ok := s.onlineGameSession(id); ok {
			peers = append(peers, p)
		}
	}
	for _, p := range peers {
		go func(p *gameSession) {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			if err := s.callGameSession(ctx, p, "超时空战场维护", func() error { return s.maintainCurrentFiendWarSession(p) }); err != nil {
				s.logGameEvent(p, "超时空维护等待重试", "error", err)
			}
		}(p)
	}
}
func (s *Service) maintainCurrentFiendWarSession(p *gameSession) error {
	if !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) {
		return nil
	}
	p.dungeon.mu.Lock()
	r := p.dungeon.runtime
	if r == nil {
		p.dungeon.mu.Unlock()
		return s.tryCurrentFiendWarEncounter(p)
	}
	if r.raidEntry == nil || r.Session == nil {
		p.dungeon.mu.Unlock()
		return nil
	}
	e := *r.raidEntry
	pending, op24Sent := r.townReturnPending, r.townReturnOp24Sent
	s.raidMu.Lock()
	state := s.raids[e.RaidKey]
	exit := false
	retreat := false
	if state != nil && state.Battle != nil && state.Battle.InstanceID == e.InstanceID && state.Battle.FiendWar != nil {
		b := state.Battle
		a, ok := b.FiendWar.AreaForDungeon(int(r.Dungeon.ID))
		owned := ok && b.FiendWarEncounters[a.Index].RunID == e.RunID
		_, cleared := b.ClearedRuns[e.RunID]
		retreat = b.FiendWarRetreatRuns[e.RunID]
		exit = b.PhaseCompleted || b.TimedOut || !owned || ok && !b.FiendWar.Areas[a.Index].Deadline.After(time.Now()) || cleared || retreat
	}
	var snapshot runtimeRaidState
	if state != nil {
		snapshot = cloneRuntimeRaidState(*state)
	}
	s.raidMu.Unlock()
	p.dungeon.mu.Unlock()
	if !exit || pending && (!retreat || op24Sent) {
		return nil
	}
	if snapshot.Battle != nil && snapshot.Battle.PhaseCompleted {
		if err := s.sendCurrentRaidProgressProjection(p, snapshot, false, false); err != nil {
			return err
		}
		if err := s.sendCurrentAntonTerminalResult(p, snapshot); err != nil {
			return err
		}
		if err := s.sendCurrentAntonRewardFlowProjection(p, snapshot); err != nil {
			return err
		}
		if f := snapshot.Battle.RewardFlow[0]; f == nil || f.FinishedAt.IsZero() {
			return nil
		}
	}
	transition, err := s.prepareCurrentFiendWarReturn(p, r)
	if err != nil {
		return err
	}
	p.dungeon.mu.Lock()
	defer p.dungeon.mu.Unlock()
	if p.dungeon.runtime != r || r.townReturnPending && (!retreat || r.townReturnOp24Sent) {
		return nil
	}
	if retreat {
		r.fiendWarRetreatKeepsParty = true
	}
	return s.sendCurrentDungeonReturnToTownLocked(p, r, transition, 0, "超时空区域战斗结束")
}
