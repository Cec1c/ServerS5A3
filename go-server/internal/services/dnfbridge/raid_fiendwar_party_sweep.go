package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/party"
)

// 超时空遭遇战临时队伍：入场时按队伍 ID 记下快照，等所有人离开副本后退役。
//
// 为什么不用 run 做键：客户端打完遭遇战是切场景重连，per-session 的 runtime
// （以及上面的 RunID）会被整份丢弃；只有队伍 ID 在重连后仍然稳定。
// armed 语义：必须先真的看到有人进了副本，才允许退役，避免入场前误拆。
type fiendWarEncounterPartyEntry struct {
	snapshot party.RuntimePartySnapshot
	armed    bool
	runID    string
}

func (s *Service) rememberFiendWarEncounterParty(raidKey uint32, snapshot party.RuntimePartySnapshot, insideDungeon bool) {
	if s == nil || raidKey == 0 || snapshot.ID == 0 {
		return
	}
	s.fiendWarPartyMu.Lock()
	defer s.fiendWarPartyMu.Unlock()
	if s.fiendWarEncounterParties == nil {
		s.fiendWarEncounterParties = map[uint32]map[uint16]*fiendWarEncounterPartyEntry{}
	}
	parties := s.fiendWarEncounterParties[raidKey]
	if parties == nil {
		parties = map[uint16]*fiendWarEncounterPartyEntry{}
		s.fiendWarEncounterParties[raidKey] = parties
	}
	if entry, exists := parties[snapshot.ID]; !exists {
		parties[snapshot.ID] = &fiendWarEncounterPartyEntry{snapshot: snapshot, armed: insideDungeon}
	} else if insideDungeon && entry != nil {
		entry.armed = true
	}
}

// retireStaleFiendWarEncounterParties 由区域移动提交后调用。
func (s *Service) retireStaleFiendWarEncounterParties(p *gameSession) {
	if s == nil || p == nil || p.selectedCharacterID == 0 {
		return
	}
	state, found := s.runtimeRaidForMember(p.selectedCharacterID)
	if !found {
		return
	}
	type pending struct {
		id       uint16
		snapshot party.RuntimePartySnapshot
		armed    bool
		runID    string
	}
	s.fiendWarPartyMu.Lock()
	parties := s.fiendWarEncounterParties[state.RaidKey]
	items := make([]pending, 0, len(parties))
	for id, entry := range parties {
		if entry == nil {
			continue
		}
		items = append(items, pending{id: id, snapshot: entry.snapshot, armed: entry.armed, runID: entry.runID})
	}
	s.fiendWarPartyMu.Unlock()
	if len(items) == 0 {
		return
	}
	for _, item := range items {
		occupied, runID := s.fiendWarPartyOccupancy(item.snapshot)
		if occupied {
			s.fiendWarPartyMu.Lock()
			if parties, ok := s.fiendWarEncounterParties[state.RaidKey]; ok {
				if entry, ok := parties[item.id]; ok && entry != nil {
					entry.armed = true
					if runID != "" {
						entry.runID = runID
					}
				}
			}
			s.fiendWarPartyMu.Unlock()
			continue
		}
		if !item.armed {
			continue
		}
		// 退役要给每个成员各发一条 op9。若此刻有人不在线（切场景重连中），
		// 那一侧的队伍框会残留 —— 所以等全员在线再退役，下一轮再试。
		if _, online := s.fiendWarPartyAllOnline(item.snapshot); !online {
			continue
		}
		s.retireCurrentFiendWarPartySnapshot(p, item.snapshot, item.runID)
		s.fiendWarPartyMu.Lock()
		if parties, ok := s.fiendWarEncounterParties[state.RaidKey]; ok {
			delete(parties, item.id)
			if len(parties) == 0 {
				delete(s.fiendWarEncounterParties, state.RaidKey)
			}
		}
		s.fiendWarPartyMu.Unlock()
	}
}

// fiendWarPartyOccupancy 返回队伍里是否还有人待在某次攻坚副本运行态里。
func (s *Service) fiendWarPartyOccupancy(snapshot party.RuntimePartySnapshot) (bool, string) {
	if s == nil {
		return false, ""
	}
	for _, member := range snapshot.Members {
		peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || peer.session == nil {
			continue
		}
		peer.session.dungeon.mu.Lock()
		r := peer.session.dungeon.runtime
		inside := r != nil && r.raidEntry != nil && r.raidEntry.RunID != ""
		runID := ""
		if inside {
			runID = r.raidEntry.RunID
		}
		peer.session.dungeon.mu.Unlock()
		if inside {
			return true, runID
		}
	}
	return false, ""
}

// fiendWarPartyAllOnline 返回所有成员是否都在线；不在线时带回第一个离线角色 ID。
func (s *Service) fiendWarPartyAllOnline(snapshot party.RuntimePartySnapshot) (uint16, bool) {
	if s == nil {
		return 0, false
	}
	for _, member := range snapshot.Members {
		peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || peer.session == nil {
			return member.UserID, false
		}
	}
	return 0, true
}
