package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
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
	state, ok := s.runtimeRaidForMember(p.selectedCharacterID)
	if !ok || state.Battle == nil || state.Battle.FiendWar == nil || !state.Started || state.Battle.TimedOut || state.Battle.PhaseCompleted || time.Now().Before(state.Battle.EnterAfter) {
		return nil
	}
	p.townMu.Lock()
	pendingMove := p.fiendWarPendingMove
	p.townMu.Unlock()
	b := state.Battle
	f := b.FiendWar
	area := f.MemberArea(p.selectedCharacterID, b.MemberGroups[p.selectedCharacterID])
	a := f.Config.Areas[area]
	if !f.AreaOpen(area) || f.Areas[area].Group != 0 {
		return nil
	}
	source, ready, atTarget, fromPending, ok := currentFiendWarEncounterSourceArea(p, area, f.Config.WaitingTown, b.InstanceID)
	if !ok || !ready || !atTarget {
		return nil
	}
	// Freeze only the confirmed pre-combat map. pending.from is the origin;
	// destination members are never unioned into this roster. Peers still at
	// that origin are included even though the leader already reports target.
	var self runtimeRaidMemberState
	for _, m := range state.Members {
		if m.CharID == p.selectedCharacterID {
			self = m
			break
		}
	}
	if self.CharID == 0 {
		return nil
	}
	roster := []runtimeRaidMemberState{self}
	admission := []currentFiendWarAdmissionMember{{CharacterID: self.CharID, Generation: p.characterGeneration, Source: source, TargetMove: fromPending}}
	for _, m := range state.Members {
		if m.CharID == p.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(m.CharID)
		if !online || !s.currentRaidAuthenticated(peer) || peer.channel.ID != p.channel.ID || b.MemberAccounts[m.CharID] != s.accountIDForSession(peer) {
			continue // 断线保留的攻坚席位不占这次进图名单。
		}
		peerSource, peerReady, _, peerTargetMove, peerOK := currentFiendWarEncounterSourceArea(peer, area, f.Config.WaitingTown, b.InstanceID)
		if !peerOK || peerSource != source {
			continue
		}
		if !peerReady {
			return nil
		}
		peer.dungeon.mu.Lock()
		peerBusy := peer.dungeon.runtime != nil
		peer.dungeon.mu.Unlock()
		if peerBusy || len(roster) >= a.MemberMax {
			return nil
		}
		roster = append(roster, m)
		admission = append(admission, currentFiendWarAdmissionMember{CharacterID: m.CharID, Generation: peer.characterGeneration, Source: peerSource, TargetMove: peerTargetMove})
	}
	if len(roster) == 0 || len(roster) > a.MemberMax {
		return nil
	}
	for _, m := range roster {
		peer, found := s.onlineGameSession(m.CharID)
		if !found {
			return nil
		}
		if _, bound := bindCurrentTownSelectorOrigin(peer); !bound {
			return nil
		}
	}
	// One battle-wide claim precedes every party replacement. Same-area and
	// overlapping callers cannot replace this winner before reservation.
	claim, err := s.claimCurrentFiendWarEncounter(state, p, source, area, admission)
	if err != nil {
		if errors.Is(err, errCurrentFiendWarAdmissionBusy) {
			return nil
		}
		return err
	}
	defer s.releaseCurrentFiendWarEncounterClaim(state, claim)
	previous, err := s.moveCurrentFiendWarEncounterRoster(state, roster, area)
	if err != nil {
		return err
	}
	defer s.rollbackCurrentFiendWarEncounterRoster(state, previous, area)
	managed, ok := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(state, roster))
	if !ok {
		return fmt.Errorf("超时空遭遇战队伍创建失败")
	}
	temporary, found := s.runtimePartyManagerForService().SnapshotByID(uint16(managed.PartyID))
	if !found {
		return fmt.Errorf("超时空遭遇战临时队伍已更换")
	}
	entered := false
	defer func() {
		if !entered {
			s.retireCurrentFiendWarPartySnapshot(p, temporary, "entry_not_committed")
		}
	}()
	if err := s.sendManagedRuntimePartySnapshots(managed); err != nil {
		return err
	}
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body, uint32(a.Dungeon))
	err = s.handleDungeonSelectUpperPlannedAdmitted(p, body, nil, true, nil)
	p.dungeon.mu.Lock()
	entered = p.dungeon.runtime != nil && p.dungeon.runtime.raidEntry != nil && p.dungeon.runtime.raidEntry.InstanceID == b.InstanceID
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
