package dnfbridge

import (
	"context"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentPartyMonsterExperienceReceipt struct {
	room      *runtimeDungeonRoom
	objectKey uint32
}

// Only the accepted combat death fans out. Peer death replays cannot award the
// original killer again. Each recipient uses its own progression transaction.
func (s *Service) dispatchCurrentDungeonMonsterExperienceToPeersLocked(source *gameSession, runtime *runtimeDungeonState, monster runtimeDungeonMonster) {
	if runtime == nil || runtime.partyInstance == nil || runtime.Room == nil || !runtime.Room.HasCombatDeath(monster.ObjectKey) {
		return
	}
	instance, room := runtime.partyInstance, runtime.Room
	partyID := instance.key.PartyID
	sourceIdentity, ok := s.boundGameSessionCharacterSnapshot(source)
	if !ok {
		return
	}
	manager := s.runtimePartyManagerForService()
	snapshot, ok := manager.SnapshotByUser(sourceIdentity.character, sourceIdentity.generation)
	if !ok || snapshot.ID != partyID {
		return
	}
	for _, peer := range s.partyDungeonInstancePeers(runtime, sourceIdentity.character) {
		identity, bound := s.boundGameSessionCharacterSnapshot(peer.Session)
		if !bound || identity.character != peer.CharacterID {
			continue
		}
		eligible := false
		for _, member := range snapshot.Members {
			if member.UserID == identity.character && member.SessionGeneration == identity.generation {
				eligible = true
			}
		}
		if !eligible {
			continue
		}
		run := func() error {
			peer.Session.dungeon.mu.Lock()
			defer peer.Session.dungeon.mu.Unlock()
			if !s.boundGameSessionCharacterCurrent(identity) {
				return nil
			}
			current := peer.Session.dungeon.runtime
			if current == nil || current.Session != peer.Scene || current.Room != room || current.partyInstance != instance || current.townReturnPending {
				return nil
			}
			live, found := manager.SnapshotByUser(identity.character, identity.generation)
			if !found || live.ID != partyID {
				return nil
			}
			instance.mu.Lock()
			dead := instance.deadMembers[identity.character]
			instance.mu.Unlock()
			if dead {
				return nil
			}
			key := currentPartyMonsterExperienceReceipt{room: room, objectKey: monster.ObjectKey}
			if current.partyExperienceReceipts[key] {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			result, err := s.awardCurrentDungeonMonsterExperience(ctx, peer.Session, current, monster)
			cancel()
			if err != nil {
				return err
			}
			if current.partyExperienceReceipts == nil {
				current.partyExperienceReceipts = make(map[currentPartyMonsterExperienceReceipt]bool)
			}
			// Commit receipt before network output: a failed write must not grant twice.
			current.partyExperienceReceipts[key] = true
			recordCurrentDungeonMonsterExperience(current, monster, result)
			if result.Award.Gain == 0 {
				return nil
			}
			current.Character = dnfrepo.CloneCharacter(result.Character)
			body := buildCurrentFinishLoadingCharacterStateBodyWithPresentation(result.Character, result.Skill.Points,
				&currentFinishLoadingExperiencePresentation{GrowthContractBonus: result.GrowthContractBonus})
			if err := s.sendCurrentDungeonMonsterExperienceState(peer.Session, current, body, result); err != nil {
				return err
			}
			return s.flushCurrentMonsterAdventureInfo(peer.Session, current)
		}
		if err := s.tryPostGameSessionCharacterEvent(peer.Session, "party-monster-experience", identity.character, identity.generation, run); err != nil {
			s.logGameEvent(source, "game-party-monster-experience-deferred", "peer_char_id", identity.character, "monster_object_key", monster.ObjectKey, "error", err)
		}
	}
}
