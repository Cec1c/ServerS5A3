package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// A local completion can follow another group's altar/throne transition that
// hides energy. It cannot clear that closed global map or earn another clear,
// but its authenticated, entered scene must retain a way out of the result UI.
// This receipt authorizes town/other-selector only, never retry or rewards.
type currentLukeClosedEnergyReturnOwner struct {
	Session        *gameSession
	DungeonSession *worldmap.DungeonSession
	Generation     uint64
	CharacterID    uint16
	AccountID      string
	DungeonID      int64
	Entry          currentRaidDungeonEntry
}

// Called inside the authenticated completion reducer with raidMu held, after
// ticking the real graph. No receipt is minted from a client exit request.
func recordCurrentLukeClosedEnergyReturn(b *runtimeRaidBattle, session *gameSession, r *runtimeDungeonState) {
	if b == nil || b.Mode != 2 || b.Single || b.Phase != 1 || b.PhaseCompleted || b.TimedOut ||
		b.Anton == nil || !b.Anton.IsLuke() || r == nil || r.raidEntry == nil || r.Session == nil ||
		(r.Dungeon.ID != 5063 && r.Dungeon.ID != 5066) || r.lukeClosedEnergyReturn != nil {
		return
	}
	e := r.raidEntry
	p, ok := b.Participation[session.selectedCharacterID]
	if !ok || p.Session != session || p.Runtime != r || p.Generation != session.characterGeneration ||
		p.RunID != e.RunID || p.DungeonID != uint32(r.Dungeon.ID) || p.Phase != e.Phase || p.Attempt != e.Attempt ||
		b.Anton.Epoch() != e.Attempt || e.Phase != 1 || b.ChannelID != e.ChannelID || b.ChannelType != e.ChannelType ||
		!(&raid.LukeProgression{AntonProgression: b.Anton}).HasEnteredRun(e.RunID, uint32(r.Dungeon.ID)) {
		return
	}
	closed := false
	for _, d := range b.Dungeons {
		closed = closed || int64(d.ID) == r.Dungeon.ID && d.State != 0
	}
	if !closed {
		return
	}
	copy := *e
	copy.Participants = append([]uint16(nil), e.Participants...)
	r.lukeClosedEnergyReturn = &currentLukeClosedEnergyReturnOwner{session, r.Session, session.characterGeneration, session.selectedCharacterID, r.AccountID, r.Dungeon.ID, copy}
}

// Caller holds dungeon.mu. The global clear journal intentionally stays empty.
func (s *Service) currentLukeClosedEnergyExitReady(session *gameSession, r *runtimeDungeonState) bool {
	if s.options.currentClientVersion != "official-24.9" || session == nil || r == nil ||
		r.lukeClosedEnergyReturn == nil || r.raidEntry == nil || r.Session == nil ||
		session.dungeon.runtime != r || !s.currentRaidAuthenticated(session) ||
		!dungeonRuntimeOwnsCharacter(r, session.selectedCharacterID) ||
		r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		return false
	}
	o, e := r.lukeClosedEnergyReturn, r.raidEntry
	f := &o.Entry
	if o.Session != session || o.DungeonSession != r.Session || o.Generation != session.characterGeneration ||
		o.CharacterID != session.selectedCharacterID || o.AccountID != r.AccountID || r.AccountID != s.accountIDForSession(session) ||
		o.DungeonID != r.Dungeon.ID || r.Session.Snapshot().Run.DungeonID != r.Dungeon.ID ||
		f.RaidKey != e.RaidKey || f.InstanceID != e.InstanceID || f.RunID == "" || f.RunID != e.RunID ||
		f.Phase != e.Phase || f.Attempt != e.Attempt || f.ChannelID != e.ChannelID || f.ChannelType != e.ChannelType ||
		!sameRaidParticipants(f.Participants, e.Participants) {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.RaidKey != e.RaidKey || state.Battle == nil {
		return false
	}
	b := state.Battle
	return b.Mode == 2 && !b.Single && b.InstanceID == e.InstanceID && b.Phase == e.Phase &&
		b.Anton != nil && b.Anton.Epoch() == e.Attempt && b.ChannelID == e.ChannelID && b.ChannelType == e.ChannelType &&
		session.channel.ID == e.ChannelID && session.channel.Type == e.ChannelType && b.MemberAccounts[session.selectedCharacterID] == r.AccountID
}

func (s *Service) currentCompletedDungeonTownActionReady(session *gameSession, r *runtimeDungeonState, retry bool) bool {
	return s.currentCompletedDungeonActionReady(session, r) || !retry && s.currentLukeClosedEnergyExitReady(session, r)
}

// Release only the old run's occupancy once all its admitted members detach.
// Do not execute ON GIVEUP after a completed energy result: it must not mutate
// the currently open throne/energy graph. Caller holds raidMu and has matched
// the participation runtime before invoking this helper.
func releaseCurrentLukeClosedEnergyRunLocked(b *runtimeRaidBattle, r *runtimeDungeonState, character uint16) {
	if b == nil || r == nil || r.lukeClosedEnergyReturn == nil || r.raidEntry == nil || b.Mode != 2 || b.Single || b.Anton == nil {
		return
	}
	e := r.raidEntry
	if b.InstanceID != e.InstanceID || b.Phase != e.Phase || b.Anton.Epoch() != e.Attempt {
		return
	}
	if b.RunDepartures == nil {
		b.RunDepartures = map[string]map[uint16]bool{}
	}
	if b.RunDepartures[e.RunID] == nil {
		b.RunDepartures[e.RunID] = map[uint16]bool{}
	}
	b.RunDepartures[e.RunID][character] = true
	for _, id := range e.Participants {
		if !b.RunDepartures[e.RunID][id] {
			return
		}
	}
	next, err := (&raid.LukeProgression{AntonProgression: b.Anton}).RetireClosedEnergyRun(e.RunID, uint32(r.Dungeon.ID), e.Attempt)
	if err == nil {
		b.Anton = next.AntonProgression
	}
}
