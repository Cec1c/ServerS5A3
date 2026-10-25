package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"slices"
)

func (s *Service) currentFiendWarLanding(town, area int) (int64, int64, error) {
	a, ok := s.townArea(int64(town), int64(area))
	if !ok {
		return 0, 0, fmt.Errorf("超时空PVF城镇区域不存在")
	}
	if x, y, _, found := currentTownSeriaRoomLandingPosition(a); found {
		return x, y, nil
	}
	c, err := s.loadCurrentFiendWarConfig()
	if err != nil {
		return 0, 0, err
	}
	if town == c.Battle.WaitingTown && area == c.Battle.WaitingArea {
		s.initialEquipmentMu.Lock()
		archive, err := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if err != nil {
			return 0, 0, err
		}
		m, ok := currentTownAreaMapDocument(archive, a)
		if ok {
			for _, npc := range m.NPCs {
				if slices.Contains(c.Battle.WaitingNPCs, int(npc.NPCID)) {
					return npc.Position.X, npc.Position.Y, nil
				}
			}
		}
	}
	return 0, 0, fmt.Errorf("超时空PVF区域没有可验证落点 %d/%d", town, area)
}
func (s *Service) currentFiendWarLoginLocation(p *gameSession, id uint16) (currentTownLoginLocation, error) {
	c, err := s.loadCurrentFiendWarConfig()
	if err != nil {
		return currentTownLoginLocation{}, err
	}
	area := 0
	// 只有原分线的同一角色账号可回团本等待区，不能把其它分线的保留席位
	// 当成本频道正在进行的攻坚队。冷登录与换线共用此判据。
	if state, ok := s.runtimeRaidForMember(id); p != nil && ok && state.Started && state.Battle != nil && state.Battle.FiendWar != nil &&
		state.Battle.ChannelID == p.channel.ID && state.Battle.ChannelType == p.channel.Type &&
		state.Battle.MemberAccounts[id] == s.accountIDForSession(p) {
		area = c.Battle.WaitingArea
	}
	a, ok := s.townArea(int64(c.Battle.WaitingTown), int64(area))
	if !ok || a.MapPath == "" {
		return currentTownLoginLocation{}, fmt.Errorf("超时空PVF入口缺失")
	}
	x, y, err := s.currentFiendWarLanding(c.Battle.WaitingTown, area)
	if err != nil {
		return currentTownLoginLocation{}, err
	}
	if x < -32768 || x > 32767 || y < -32768 || y > 32767 {
		return currentTownLoginLocation{}, fmt.Errorf("超时空入口坐标越界")
	}
	return currentTownLoginLocation{TownID: byte(c.Battle.WaitingTown), AreaID: byte(area), PositionX: x, PositionY: y, Direction: newCharacterInitialDirection, AreaState: newCharacterInitialAreaState, MapPath: a.MapPath, Source: "超时空真实PVF入口", PositionSource: "超时空真实PVF落点"}, nil
}
func (s *Service) recordCurrentFiendWarMap(p *gameSession, r *runtimeDungeonState) {
	if !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) {
		return
	}
	p.dungeon.mu.Lock()
	defer p.dungeon.mu.Unlock()
	if r == nil || p.dungeon.runtime != r || r.Session == nil || r.raidEntry == nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		return
	}
	scene, ok := r.Session.Scene()
	if !ok || scene.Map.Map.ID <= 0 {
		return
	}
	s.raidMu.Lock()
	state := s.raids[r.raidEntry.RaidKey]
	var snapshot runtimeRaidState
	if state != nil && state.Battle != nil && state.Battle.InstanceID == r.raidEntry.InstanceID && state.Battle.FiendWar != nil && !state.Battle.TimedOut && !state.Battle.PhaseCompleted && state.Battle.MemberAccounts[p.selectedCharacterID] == s.accountIDForSession(p) {
		b := state.Battle
		if err := validateCurrentFiendWarDungeon(b, p.selectedCharacterID, r); err == nil {
			if b.FiendWarMaps == nil {
				b.FiendWarMaps = map[string]map[int64]bool{}
			}
			if b.FiendWarMaps[r.raidEntry.RunID] == nil {
				b.FiendWarMaps[r.raidEntry.RunID] = map[int64]bool{}
			}
			if !b.FiendWarMaps[r.raidEntry.RunID][scene.Map.Map.ID] {
				next := b.FiendWar.Clone()
				if err := next.Apply(fiendwar.FiendWarEvent{Kind: "on enter map", ID: int(scene.Map.Map.ID)}, s.gameplayNow()); err == nil {
					b.FiendWar = next
					b.FiendWarMaps[r.raidEntry.RunID][scene.Map.Map.ID] = true
					b.ProgressVersion++
					snapshot = cloneRuntimeRaidState(*state)
				}
			}
		}
	}
	s.raidMu.Unlock()
	if snapshot.Battle != nil {
		s.queueCurrentRaidDungeonProgress(snapshot)
	}
}
