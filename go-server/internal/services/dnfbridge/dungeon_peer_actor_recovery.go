package dnfbridge

import (
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// A completed write is not a native actor/registry acknowledgement. Retry the
// peer snapshot in six bounded opportunities after both finish tails, without restarting a map,
// replaying placement/monsters, resetting local state or re-handshaking UDP.
var dungeonPeerRecoveryDelays = [...]time.Duration{750 * time.Millisecond, 1250 * time.Millisecond, 3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second}

const dungeonPeerRecoveryBudget = 15 * time.Second

type dungeonPeerRecoveryState byte

const (
	dungeonPeerRecoveryStale dungeonPeerRecoveryState = iota
	dungeonPeerRecoveryWaiting
	dungeonPeerRecoveryReady
)

type dungeonPeerActorRecovery struct {
	receiver                  boundGameSessionCharacter
	actor                     boundGameSessionCharacter
	key                       dungeonPeerActorResendKey
	instance                  *dungeonPartyInstance
	round                     int // guarded by receiver.dungeonPartyRunMu; duplicate callbacks are inert
	deadline                  time.Time
	receiverEpoch, actorEpoch uint64
	nativeQuery               [64]byte // receiver.dungeonPartyRunMu
	nativeRegistered          bool     // actual client registry receipt, not a rendering proof
	membershipCurrent         func() bool
}

func (s *Service) scheduleDungeonPeerActorRecovery(receiver, actor *gameSession, key dungeonPartyRunKey) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || receiver == actor {
		return
	}
	ri, rok := s.boundGameSessionCharacterSnapshot(receiver)
	ai, aok := s.boundGameSessionCharacterSnapshot(actor)
	if !rok || !aok {
		return
	}
	receiver.dungeon.mu.Lock()
	var instance *dungeonPartyInstance
	if r := receiver.dungeon.runtime; r != nil {
		instance = r.partyInstance
	}
	receiver.dungeon.mu.Unlock()
	if instance == nil {
		return
	}
	marker := dungeonPeerActorResendKey{Run: key, Peer: ai}
	p := &dungeonPeerActorRecovery{receiver: ri, actor: ai, key: marker, instance: instance,
		deadline: s.ensureGameplayTimeQueue().Now().Add(dungeonPeerRecoveryBudget), receiverEpoch: receiver.dungeonPlacementEpoch.Load(), actorEpoch: actor.dungeonPlacementEpoch.Load()}
	s.bindDungeonPeerNativeMembership(p)
	receiver.dungeonPartyRunMu.Lock()
	if !receiver.dungeonPartyRunValid || receiver.dungeonPartyRun != key {
		receiver.dungeonPartyRunMu.Unlock()
		return
	}
	if receiver.dungeonPeerRecovery == nil {
		receiver.dungeonPeerRecovery = make(map[dungeonPeerActorResendKey]*dungeonPeerActorRecovery, 4)
	}
	if receiver.dungeonPeerRecovery[marker] != nil {
		receiver.dungeonPartyRunMu.Unlock()
		return
	}
	for prior := range receiver.dungeonPeerRecovery {
		if prior.Peer.character == ai.character {
			delete(receiver.dungeonPeerRecovery, prior)
		}
	}
	receiver.dungeonPeerRecovery[marker] = p
	receiver.dungeonPartyRunMu.Unlock()
	s.armDungeonPeerActorRecovery(p, 0)
}

func (s *Service) armDungeonPeerActorRecovery(p *dungeonPeerActorRecovery, round int) {
	if round >= len(dungeonPeerRecoveryDelays) || !s.ensureGameplayTimeQueue().Now().Before(p.deadline) {
		return
	}
	clock := s.ensureGameplayTimeQueue()
	name := fmt.Sprintf("dungeon-peer-recovery:%p:%d", p, round)
	err := clock.ScheduleAfter(name, dungeonPeerRecoveryDelays[round], func(time.Time) {
		receiver := p.receiver.session
		receiver.dungeonPartyRunMu.Lock()
		current := receiver.dungeonPeerRecovery[p.key] == p && p.round == round
		if current {
			p.round++
		}
		receiver.dungeonPartyRunMu.Unlock()
		if !current {
			return
		}
		err := s.tryPostGameSessionCharacterEvent(receiver, "party-peer-actor-recovery", p.receiver.character, p.receiver.generation, func() error {
			state := s.dungeonPeerActorRecoveryStatus(p)
			if state == dungeonPeerRecoveryStale {
				return nil
			}
			if state == dungeonPeerRecoveryWaiting {
				s.logGameEvent(receiver, "game-dungeon-party-peer-actor-recovery-wait", "round", round+1, "actor_char_id", p.actor.character)
				s.armDungeonPeerActorRecovery(p, round+1)
				return nil
			}
			delivered, err := s.sendDungeonPartyPeerActorStateObserved(receiver, p.actor.session, "bounded_peer_actor_recovery", func() bool {
				// Profile preparation performs repository IO. Fence a return/
				// reentry ABA that reused the same numeric room/run key while
				// this profile was being prepared, just before the wire write.
				// Already inside the wire lock: fence identity using the small
				// marker lock/atomic epochs, never invert dungeon -> wire order.
				receiver.dungeonPartyRunMu.Lock()
				current := receiver.dungeonPeerRecovery[p.key] == p && !p.nativeRegistered
				receiver.dungeonPartyRunMu.Unlock()
				return current && receiver.dungeonPlacementEpoch.Load() == p.receiverEpoch &&
					p.actor.session.dungeonPlacementEpoch.Load() == p.actorEpoch && s.ensureGameplayTimeQueue().Now().Before(p.deadline)
			}, &p.key.Run)
			if delivered && err == nil && s.dungeonPeerActorRecoveryStatus(p) == dungeonPeerRecoveryReady {
				err = s.sendDungeonPeerNativeRegistrationQuery(p)
			}
			s.logGameEvent(receiver, "game-dungeon-party-peer-actor-recovery", "round", round+1,
				"receiver_char_id", p.receiver.character, "actor_char_id", p.actor.character,
				"map_id", p.key.Run.MapID, "room", dungeonRoomCoordinate(p.key.Run.RoomX, p.key.Run.RoomY),
				"send_completed", delivered, "client_native_result", "unknown", "error", err)
			if s.dungeonPeerActorRecoveryStatus(p) != dungeonPeerRecoveryStale {
				s.armDungeonPeerActorRecovery(p, round+1)
			}
			// Send failures were logged and own their one bounded followup.
			// Returning them through a synchronous owner fallback would schedule
			// that followup twice; only queue admission errors belong below.
			return nil
		})
		if err != nil && s.dungeonPeerActorRecoveryStatus(p) != dungeonPeerRecoveryStale {
			// A full owner queue consumes this bounded opportunity; no blocking
			// timer IO and no unbounded retry loop can starve gameplay.
			s.armDungeonPeerActorRecovery(p, round+1)
		}
	})
	if err != nil {
		s.logGameEvent(p.receiver.session, "game-dungeon-party-peer-actor-recovery-schedule-failed", "error", err)
	}
}

func (s *Service) dungeonPeerActorRecoveryCurrent(p *dungeonPeerActorRecovery) bool {
	return s.dungeonPeerActorRecoveryStatus(p) == dungeonPeerRecoveryReady
}

func (s *Service) dungeonPeerActorRecoveryStatus(p *dungeonPeerActorRecovery) dungeonPeerRecoveryState {
	if p == nil || !s.ensureGameplayTimeQueue().Now().Before(p.deadline) {
		return dungeonPeerRecoveryStale
	}
	if p.receiver.session.gameTransportContext().Err() != nil || p.actor.session.gameTransportContext().Err() != nil {
		return dungeonPeerRecoveryStale
	}
	if !s.boundGameSessionCharacterCurrent(p.receiver) || !s.boundGameSessionCharacterCurrent(p.actor) {
		return dungeonPeerRecoveryStale
	}
	receiver, actor := p.receiver.session, p.actor.session
	receiver.dungeonPartyRunMu.Lock()
	current := receiver.dungeonPeerRecovery[p.key] == p && receiver.dungeonPartyRunValid && receiver.dungeonPartyRun == p.key.Run && !p.nativeRegistered
	receiver.dungeonPartyRunMu.Unlock()
	if !current {
		return dungeonPeerRecoveryStale
	}
	if receiver.dungeonPlacementEpoch.Load() != p.receiverEpoch || actor.dungeonPlacementEpoch.Load() != p.actorEpoch || p.membershipCurrent != nil && !p.membershipCurrent() {
		return dungeonPeerRecoveryStale
	}
	key, ready := s.currentDungeonPartyRunKey(actor)
	if !ready || key != p.key.Run {
		return dungeonPeerRecoveryStale
	}
	receiver.dungeon.mu.Lock()
	r := receiver.dungeon.runtime
	valid := r != nil && r.partyInstance == p.instance && !r.townReturnPending && !r.deathReturnWaiting && r.Session != nil && r.Session.Snapshot().Run.Status == worldmap.DungeonRunActive
	receiver.dungeon.mu.Unlock()
	if !valid {
		return dungeonPeerRecoveryStale
	}
	p.instance.mu.Lock()
	valid = p.instance.sessions[p.receiver.character] == receiver && p.instance.sessions[p.actor.character] == actor &&
		!p.instance.deadMembers[p.receiver.character] && !p.instance.deadMembers[p.actor.character]
	p.instance.mu.Unlock()
	if !valid {
		return dungeonPeerRecoveryStale
	}
	if !peerSceneStableForPeerActorLandingResend(receiver) || !peerSceneStableForPeerActorLandingResend(actor) {
		return dungeonPeerRecoveryWaiting
	}
	return dungeonPeerRecoveryReady
}
