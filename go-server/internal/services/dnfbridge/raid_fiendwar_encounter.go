package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"sort"
	"time"
)

// The selected actor's mailbox serializes movement completion and encounter entry.
func (s *Service) queueCurrentFiendWarEncounter(p *gameSession) {
	if !isCurrentFiendWarChannel(p) {
		return
	}
	id, generation := p.selectedCharacterID, p.characterGeneration
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		err := s.callGameSession(ctx, p, "超时空区域遭遇战", func() error {
			if p.selectedCharacterID != id || p.characterGeneration != generation {
				return nil
			}
			return s.tryCurrentFiendWarEncounter(p)
		})
		if err != nil {
			s.logGameEvent(p, "超时空区域遭遇战延后", "error", err)
		}
	}()
}
func (s *Service) tryCurrentFiendWarEncounter(p *gameSession) error {
	if !isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) {
		return nil
	}
	p.dungeon.mu.Lock()
	busy := p.dungeon.runtime != nil
	p.dungeon.mu.Unlock()
	if busy {
		return nil
	}
	p.townMu.Lock()
	position := p.townPositionSnapshot
	pendingMove := p.fiendWarPendingMove
	ready := p.townSceneReadyCharacterID == p.selectedCharacterID
	p.townMu.Unlock()
	if !ready {
		return nil
	}
	state, ok := s.runtimeRaidForMember(p.selectedCharacterID)
	if !ok || state.Battle == nil || state.Battle.FiendWar == nil || !state.Started || state.Battle.TimedOut || state.Battle.PhaseCompleted || time.Now().Before(state.Battle.EnterAfter) {
		return nil
	}
	b := state.Battle
	f := b.FiendWar
	area := f.MemberArea(p.selectedCharacterID, b.MemberGroups[p.selectedCharacterID])
	a := f.Config.Areas[area]
	if int(position.TownID) != f.Config.WaitingTown || int(position.AreaID) != area || !f.AreaOpen(area) || f.Areas[area].Group != 0 {
		return nil
	}
	origin := area
	if pendingMove != nil && pendingMove.instance == b.InstanceID && pendingMove.character == p.selectedCharacterID && pendingMove.to == area {
		origin = pendingMove.from
	}
	// Take every authenticated, ready participant in this exact battle cell,
	// independently of the eight one-person attack-party labels.
	roster := []runtimeRaidMemberState{}
	for _, m := range state.Members {
		if m.CharID == p.selectedCharacterID {
			roster = append(roster, m)
		}
	}
	candidates := append([]runtimeRaidMemberState(nil), state.Members...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return f.MemberArea(candidates[i].CharID, b.MemberGroups[candidates[i].CharID]) == area && f.MemberArea(candidates[j].CharID, b.MemberGroups[candidates[j].CharID]) != area
	})
	for _, m := range candidates {
		if m.CharID == p.selectedCharacterID || (f.MemberArea(m.CharID, b.MemberGroups[m.CharID]) != area && f.MemberArea(m.CharID, b.MemberGroups[m.CharID]) != origin) || len(roster) >= a.MemberMax {
			continue
		}
		peer, online := s.onlineGameSession(m.CharID)
		if !online || !s.currentRaidAuthenticated(peer) || peer.channel.ID != p.channel.ID || b.MemberAccounts[m.CharID] != s.accountIDForSession(peer) {
			continue // 断线保留的攻坚席位不占这次进图名单。
		}
		peer.dungeon.mu.Lock()
		busy := peer.dungeon.runtime != nil
		peer.dungeon.mu.Unlock()
		peer.townMu.Lock()
		pos := peer.townPositionSnapshot
		ready := peer.townSceneReadyCharacterID == m.CharID
		peer.townMu.Unlock()
		if busy || !ready || int(pos.TownID) != f.Config.WaitingTown || (int(pos.AreaID) != area && int(pos.AreaID) != origin) {
			return nil
		}
		roster = append(roster, m)
	}
	if len(roster) == 0 || len(roster) > a.MemberMax {
		return nil
	}
	for _, m := range roster {
		peer, ok := s.onlineGameSession(m.CharID)
		if !ok {
			return nil
		}
		if _, ok = bindCurrentTownSelectorOrigin(peer); !ok {
			return nil
		}
	}
	// Freeze the destination of the exact ready roster before preparing maps.
	// Entry rollback restores only actors that never acquired a runtime.
	previous, err := s.moveCurrentFiendWarEncounterRoster(state, roster, area)
	if err != nil {
		return err
	}
	defer s.rollbackCurrentFiendWarEncounterRoster(state, previous, area)
	managed, ok := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(state, roster))
	if !ok {
		return fmt.Errorf("超时空遭遇战队伍创建失败")
	}
	if err := s.sendManagedRuntimePartySnapshots(managed); err != nil {
		return err
	}
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, uint32(a.Dungeon))
	err = s.handleDungeonSelectUpperPlanned(p, body, nil, true, nil)
	p.dungeon.mu.Lock()
	entered := p.dungeon.runtime != nil && p.dungeon.runtime.raidEntry != nil && p.dungeon.runtime.raidEntry.InstanceID == b.InstanceID
	p.dungeon.mu.Unlock()
	if entered {
		p.townMu.Lock()
		if p.fiendWarPendingMove == pendingMove {
			p.fiendWarPendingMove = nil
		}
		p.townMu.Unlock()
	}
	return err
}
