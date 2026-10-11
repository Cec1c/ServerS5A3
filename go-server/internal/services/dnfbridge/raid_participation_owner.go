package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"sort"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentRaidParticipation struct {
	RunID      string
	DungeonID  uint32
	Phase      byte
	Attempt    uint64
	Generation uint64
	Runtime    *runtimeDungeonState
	Session    *gameSession
}

type currentRaidParticipationPosition struct {
	DungeonID  uint32
	Cleared    bool
	GroupIndex byte
	RunID      string
}

// Capture only this actor's successful entry tail, after actual op29 and the
// post-start state. Prepared party peers do not become occupied UI positions.
func (s *Service) recordCurrentRaidParticipation(session *gameSession, r *runtimeDungeonState) {
	if session == nil || r == nil || r.raidEntry == nil || r.Dungeon.ID <= 0 || r.Dungeon.ID > int64(^uint32(0)) || r.raidEntry.RunID == "" {
		return
	}
	session.dungeon.mu.Lock()
	if !s.currentRaidAuthenticated(session) || session.dungeon.runtime != r || r.Session == nil || !session.postStartMapPlayerStateSent || !dungeonRuntimeOwnsCharacter(r, session.selectedCharacterID) || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		session.dungeon.mu.Unlock()
		return
	}
	e := r.raidEntry
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	valid := state != nil && state.Started && state.Battle != nil && state.RaidKey == e.RaidKey
	if valid {
		b := state.Battle
		valid = b.InstanceID == e.InstanceID && b.Phase == e.Phase && !b.TimedOut && b.ChannelID == e.ChannelID && b.ChannelType == e.ChannelType && session.channel.ID == e.ChannelID && session.channel.Type == e.ChannelType && b.MemberAccounts[session.selectedCharacterID] == r.AccountID && r.AccountID == s.accountIDForSession(session)
		if b.Anton != nil {
			valid = valid && b.Anton.Epoch() == e.Attempt
		}
		participant := false
		for _, id := range e.Participants {
			participant = participant || id == session.selectedCharacterID
		}
		valid = valid && participant
		if valid {
			if b.Participation == nil {
				b.Participation = map[uint16]currentRaidParticipation{}
			}
			entry := currentRaidParticipation{e.RunID, uint32(r.Dungeon.ID), e.Phase, e.Attempt, session.characterGeneration, r, session}
			if previous, exists := b.Participation[session.selectedCharacterID]; exists && previous == entry {
				valid = false
			} else {
				b.Participation[session.selectedCharacterID] = entry
				b.ProgressVersion++
			}
		}
	}
	var snapshot runtimeRaidState
	if valid {
		snapshot = cloneRuntimeRaidState(*state)
	}
	s.raidMu.Unlock()
	session.dungeon.mu.Unlock()
	if valid {
		s.queueCurrentRaidDungeonProgress(snapshot)
	}
}

// Every legitimate runtime detach reaches the existing shared-instance
// release boundary, including solo runs. An old detach cannot remove a new run.
func (s *Service) releaseCurrentRaidParticipation(r *runtimeDungeonState, characterID uint16) {
	if s == nil || r == nil || r.raidEntry == nil || characterID == 0 {
		return
	}
	s.raidMu.Lock()
	e := r.raidEntry
	state := s.raids[e.RaidKey]
	changed := false
	if state != nil && state.Battle != nil && state.Battle.InstanceID == e.InstanceID {
		if old, ok := state.Battle.Participation[characterID]; ok && old.Runtime == r && old.RunID == e.RunID && old.Phase == e.Phase && old.Attempt == e.Attempt {
			delete(state.Battle.Participation, characterID)
			state.Battle.ProgressVersion++
			changed = true
		}
	}
	var snapshot runtimeRaidState
	if changed {
		snapshot = cloneRuntimeRaidState(*state)
	}
	s.raidMu.Unlock()
	if changed {
		s.queueCurrentRaidDungeonProgress(snapshot)
	}
}

// A reversible retry may temporarily detach runtime while retaining its old
// return owner. Closing that exact socket still retires its occupancy. Session
// identity is stronger than a per-session generation that a new socket repeats.
func (s *Service) releaseCurrentRaidParticipationSession(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	changed := false
	if state != nil && state.Battle != nil {
		if entry, ok := state.Battle.Participation[session.selectedCharacterID]; ok && entry.Session == session {
			delete(state.Battle.Participation, session.selectedCharacterID)
			state.Battle.ProgressVersion++
			changed = true
		}
	}
	var snapshot runtimeRaidState
	if changed {
		snapshot = cloneRuntimeRaidState(*state)
	}
	s.raidMu.Unlock()
	if changed {
		s.queueCurrentRaidDungeonProgress(snapshot)
	}
}

func currentRaidParticipationPositions(b *runtimeRaidBattle, members []runtimeRaidMemberState) (map[uint16]currentRaidParticipationPosition, error) {
	positions := map[uint16]currentRaidParticipationPosition{}
	if b == nil || b.TimedOut {
		return positions, nil
	}
	if len(b.Participation) > runtimeRaidMaxGroups*runtimeRaidGroupSize {
		return nil, fmt.Errorf("participation exceeds frozen raid member capacity")
	}
	liveGroups := map[uint16]byte{}
	for _, member := range members {
		if member.CharID == 0 || member.GroupIndex > runtimeRaidMaxGroups {
			return nil, fmt.Errorf("participation live squad owner invalid")
		}
		if _, duplicate := liveGroups[member.CharID]; duplicate {
			return nil, fmt.Errorf("participation live roster repeats owner")
		}
		liveGroups[member.CharID] = member.GroupIndex
	}
	for id, entry := range b.Participation {
		groupIndex, stillMember := liveGroups[id]
		if !stillMember {
			continue
		}
		if entry.Phase != b.Phase || (b.Anton != nil && entry.Attempt != b.Anton.Epoch()) {
			continue
		}
		if id == 0 || entry.RunID == "" || entry.DungeonID == 0 || b.MemberAccounts[id] == "" {
			return nil, fmt.Errorf("participation frozen identity unavailable")
		}
		target := entry.DungeonID
		if b.Single && b.Mode == 1 {
			var ok bool
			target, ok = currentSingleAntonSelectorAlias(target)
			if !ok {
				return nil, fmt.Errorf("single participation alias unavailable")
			}
		}
		cleared := false
		if run, ok := b.ClearedRuns[entry.RunID]; ok {
			if run.DungeonID != entry.DungeonID || run.Phase != entry.Phase || run.Attempt != entry.Attempt {
				return nil, fmt.Errorf("participation clear owner changed")
			}
			for _, member := range run.Participants {
				cleared = cleared || member == id
			}
		}
		positions[id] = currentRaidParticipationPosition{DungeonID: target, Cleared: cleared, GroupIndex: groupIndex, RunID: entry.RunID}
	}
	return positions, nil
}

func currentRaidParticipationBody(target uint32, operation byte, ids []uint16) []byte {
	body := make([]byte, 7+len(ids)*2)
	body[0] = 1
	binary.LittleEndian.PutUint32(body[1:], target)
	body[5] = operation
	body[6] = byte(len(ids))
	for i, id := range ids {
		binary.LittleEndian.PutUint16(body[7+i*2:], id)
	}
	return body
}

func currentRaidParticipationProjection(previous, next map[uint16]currentRaidParticipationPosition, fresh bool) []currentRaidProjectionPacket {
	equal := len(previous) == len(next)
	if equal {
		for id, entry := range next {
			if old, ok := previous[id]; !ok || old != entry {
				equal = false
				break
			}
		}
	}
	if equal && !fresh {
		return nil
	}
	// Native 1A64E80 appends one squad row per vector. A dungeon may have
	// multiple admitted runs; the reward-frozen MemberGroups is not live UI.
	type rowKey struct {
		DungeonID  uint32
		GroupIndex byte
		RunID      string
		Unassigned uint16
	}
	group := func(positions map[uint16]currentRaidParticipationPosition, clearedOnly bool) map[rowKey][]uint16 {
		groups := map[rowKey][]uint16{}
		for id, entry := range positions {
			if clearedOnly && !entry.Cleared {
				continue
			}
			key := rowKey{DungeonID: entry.DungeonID, GroupIndex: entry.GroupIndex, RunID: entry.RunID}
			if entry.GroupIndex == 0 {
				key.Unassigned = id
			}
			groups[key] = append(groups[key], id)
		}
		for key := range groups {
			sort.Slice(groups[key], func(i, j int) bool { return groups[key][i] < groups[key][j] })
		}
		return groups
	}
	var packets []currentRaidProjectionPacket
	appendGroups := func(groups map[rowKey][]uint16, op byte) {
		var keys []rowKey
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.DungeonID != b.DungeonID {
				return a.DungeonID < b.DungeonID
			}
			if a.GroupIndex != b.GroupIndex {
				return a.GroupIndex < b.GroupIndex
			}
			if a.RunID != b.RunID {
				return a.RunID < b.RunID
			}
			return a.Unassigned < b.Unassigned
		})
		for _, key := range keys {
			packets = append(packets, currentRaidProjectionPacket{opcode: 584, body: currentRaidParticipationBody(key.DungeonID, op, groups[key])})
		}
	}
	appendGroups(group(previous, false), 0)
	appendGroups(group(next, false), 0)
	appendGroups(group(next, false), 2)
	appendGroups(group(next, true), 4)
	return packets
}
