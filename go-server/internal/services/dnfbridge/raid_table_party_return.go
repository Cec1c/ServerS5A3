package dnfbridge

import "context"

const currentRaidTablePartyReturnSource = "current_exe_raid_battle_table_party_return"

// Immutable authority captured from an admitted leader before sending op24.
// Each runtime owns its own pending transition and still waits for its own
// positive town-scene evidence. No clear, reward or member ACK is fabricated.
type currentRaidTablePartyReturn struct {
	leader   boundGameSessionCharacter
	instance *dungeonPartyInstance
	entry    currentRaidDungeonEntry
}

// Caller holds only the leader's dungeon.mu.
func (s *Service) prepareCurrentRaidTablePartyReturnLocked(session *gameSession, r *runtimeDungeonState) *currentRaidTablePartyReturn {
	if r.raidTablePartyReturn != nil {
		return r.raidTablePartyReturn
	}
	if r.townReturnPending || r.partyInstance == nil || !s.currentRaidTableActiveReturnReady(session, r) {
		return nil
	}
	leader, bound := s.boundGameSessionCharacterSnapshot(session)
	party, found := s.runtimePartyManagerForService().SnapshotByUser(leader.character, leader.generation)
	if !bound || !found || party.Leader != leader.character || party.ID != instancePartyIDOfPartyDungeonInstance(r.partyInstance) {
		return nil
	}
	return &currentRaidTablePartyReturn{leader: leader, instance: r.partyInstance, entry: *r.raidEntry}
}

func (s *Service) currentRaidTablePartyReturnMatches(session *gameSession, r *runtimeDungeonState, owner *currentRaidTablePartyReturn) bool {
	if owner == nil || r == nil || r.partyInstance != owner.instance || r.raidEntry == nil ||
		!s.boundGameSessionCharacterCurrent(owner.leader) {
		return false
	}
	e, want := r.raidEntry, &owner.entry
	if e.RaidKey != want.RaidKey || e.InstanceID != want.InstanceID || e.Phase != want.Phase ||
		e.Attempt != want.Attempt || e.RunID != want.RunID || e.ChannelID != want.ChannelID || e.ChannelType != want.ChannelType {
		return false
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	party, found := s.runtimePartyManagerForService().SnapshotByUser(identity.character, identity.generation)
	return bound && found && party.ID == instancePartyIDOfPartyDungeonInstance(owner.instance) && party.Leader == owner.leader.character
}

// Runs without a dungeon lock. Never wait for another player's owner loop.
func (s *Service) queueCurrentRaidTablePartyReturn(owner *currentRaidTablePartyReturn) {
	if owner == nil {
		return
	}
	for _, peer := range dungeonPartyInstanceMemberSessions(owner.instance, owner.leader.character) {
		identity, bound := s.boundGameSessionCharacterSnapshot(peer.Session)
		if !bound || identity.character != peer.CharacterID {
			continue
		}
		member := identity.session
		// Snapshot the admitted owner without acquiring another player's
		// dungeon lock. A stuck member must not block the leader's event loop.
		var expected *runtimeDungeonState
		s.raidMu.Lock()
		if state := s.runtimeRaidByMemberLocked(identity.character); state != nil && state.Battle != nil {
			p := state.Battle.Participation[identity.character]
			if p.Session == member && p.Generation == identity.generation {
				expected = p.Runtime
			}
		}
		s.raidMu.Unlock()
		if expected == nil {
			continue
		}
		deliver := func() error { return s.sendCurrentRaidTablePartyReturnOnOwner(identity, expected, owner) }
		var err error
		if member.events == nil || member.events.loop == nil {
			err = deliver()
		} else {
			err = s.tryPostGameSessionCharacterEvent(member, "raid-table-party-return", identity.character, identity.generation, deliver)
		}
		if err != nil {
			s.logGameEvent(owner.leader.session, "game-raid-table-party-return-queue-failed", "member_char_id", identity.character, "error", err)
		}
	}
}

func (s *Service) sendCurrentRaidTablePartyReturnOnOwner(identity boundGameSessionCharacter, expected *runtimeDungeonState, owner *currentRaidTablePartyReturn) error {
	if expected == nil || !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	member := identity.session
	member.dungeon.mu.Lock()
	valid := member.dungeon.runtime == expected && !expected.townReturnPending &&
		s.currentRaidTablePartyReturnMatches(member, expected, owner) && s.currentRaidTableActiveReturnReady(member, expected)
	member.dungeon.mu.Unlock()
	if !valid {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, member, identity.character)
	cancel()
	if err != nil {
		return err
	}
	member.dungeon.mu.Lock()
	defer member.dungeon.mu.Unlock()
	if member.dungeon.runtime != expected || expected.townReturnPending ||
		!s.currentRaidTablePartyReturnMatches(member, expected, owner) || !s.currentRaidTableActiveReturnReady(member, expected) {
		return nil
	}
	expected.raidTablePartyReturn = owner
	err = s.sendCurrentDungeonReturnToTownLocked(member, expected, transition, 0, currentRaidTablePartyReturnSource)
	if !expected.townReturnPending {
		expected.raidTablePartyReturn = nil
	}
	s.logGameEvent(member, "game-raid-table-party-return-send", "leader_char_id", owner.leader.character,
		"char_id", identity.character, "pending", expected.townReturnPending, "op24_sent", expected.townReturnOp24Sent, "error", err)
	return err
}
