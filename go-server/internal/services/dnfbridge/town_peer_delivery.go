package dnfbridge

import (
	"context"
	"errors"

	"longheng.io/server/internal/platform/eventloop"
)

// A presence generation changes even when the same character reenters the
// same area. This prevents delayed enter/move work from recreating old actors.
func (m *OnlinePlayerManager) presenceCurrent(snapshot onlinePlayerInfo) bool {
	if m == nil || snapshot.Session == nil {
		return false
	}
	current, ok := m.PlayerForCharacter(snapshot.CharacterID)
	return ok && current.Session == snapshot.Session && current.presenceGeneration == snapshot.presenceGeneration &&
		current.TownID == snapshot.TownID && current.AreaID == snapshot.AreaID && current.ChannelID == snapshot.ChannelID &&
		current.Instance == snapshot.Instance && current.RaidBattleInstance == snapshot.RaidBattleInstance
}

// Never run inline or wait for capacity: the producer can hold its own town
// lock. Required scene traffic that cannot be queued fails only this recipient.
func (s *Service) queueTownPeerDelivery(target onlinePlayerInfo, source string, valid func() bool, run func() error) {
	s.queueTownPeerDeliveryPolicy(target, source, valid, run, false, nil)
}

func (s *Service) queueTownPeerDeliveryPolicy(target onlinePlayerInfo, source string, valid func() bool, run func() error, replaceable bool, discard func()) {
	accepted := false
	defer func() {
		if !accepted && discard != nil {
			discard()
		}
	}()
	p := target.Session
	if p == nil || p.conn == nil || p.loginRevoked.Load() || p.gameTransportContext().Err() != nil {
		return
	}
	if target.presenceGeneration == 0 {
		current, ok := s.onlinePlayers.PlayerForCharacter(target.CharacterID)
		if !ok || current.Session != p {
			return
		}
		target = current
	}
	if p.events == nil || p.events.loop == nil || !p.events.active.Load() {
		s.logGameEvent(p, "game-town-peer-delivery-unavailable", "source", source, "reason", "recipient_queue_inactive")
		return
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok || identity.character != target.CharacterID {
		return
	}
	err := p.events.loop.Submit(eventloop.Event{
		Type: source, Priority: eventloop.PriorityMedium, Context: p.gameTransportContext(),
		Data: gameSessionEventTask{source: source, characterID: identity.character, characterGeneration: identity.generation, async: true, discard: discard, run: func() error {
			if discard != nil {
				defer discard()
			}
			ctx, cancel := context.WithTimeout(p.gameTransportContext(), createWriteTimeout)
			defer cancel()
			if err := lockCurrentCrossSceneProfile(ctx, &p.townMu); err != nil {
				if !replaceable && !errors.Is(err, context.Canceled) {
					p.closeGameTransport()
				}
				return nil
			}
			defer p.townMu.Unlock()
			if !s.boundGameSessionCharacterCurrent(identity) || !s.onlinePlayers.presenceCurrent(target) || (valid != nil && !valid()) {
				return nil
			}
			// Presence can outlive a dungeon transition until its owner finishes
			// cleanup. Never replay queued town traffic into a newer dungeon run.
			if err := lockCurrentCrossSceneProfile(ctx, &p.dungeon.mu); err != nil {
				if !replaceable && !errors.Is(err, context.Canceled) {
					p.closeGameTransport()
				}
				return nil
			}
			activeDungeon := p.dungeon.runtime != nil && p.dungeon.runtime.Session != nil
			p.dungeon.mu.Unlock()
			if activeDungeon {
				return nil
			}
			if err := run(); err != nil {
				s.logGameEvent(p, "game-town-peer-delivery-failed", "source", source, "error", err)
				// A twice-changing membership invalidates a body before its first
				// byte. Its current formation/leave projection owns the replacement;
				// unlike a failed write, this does not corrupt the transport.
				if !errors.Is(err, errTownDisplayEligibilityChanged) {
					p.closeGameTransport()
				}
			}
			return nil
		}},
	})
	if err != nil {
		s.logGameEvent(p, "game-town-peer-delivery-unavailable", "source", source, "reason", "recipient_queue_rejected", "error", err)
		if !replaceable {
			p.closeGameTransport()
		}
	}
	accepted = err == nil
}

type townMoveKey struct {
	actor                   uint16
	actorEpoch, targetEpoch uint64
}
type townMovePending struct{ body []byte }

func (s *Service) queueTownPeerMovement(target onlinePlayerInfo, actor onlinePlayerInfo, op uint16, body []byte, source string) {
	p := target.Session
	if p == nil || actor.Session == nil {
		return
	}
	currentTarget, ok := s.onlinePlayers.PlayerForCharacter(target.CharacterID)
	if !ok || currentTarget.Session != p || (target.presenceGeneration != 0 && target.presenceGeneration != currentTarget.presenceGeneration) {
		return
	}
	currentActor, ok := s.onlinePlayers.PlayerForCharacter(actor.CharacterID)
	if !ok || currentActor.Session != actor.Session || (actor.presenceGeneration != 0 && actor.presenceGeneration != currentActor.presenceGeneration) {
		return
	}
	target, actor = currentTarget, currentActor
	key := townMoveKey{actor.CharacterID, actor.presenceGeneration, target.presenceGeneration}
	p.townMoveMu.Lock()
	if pending := p.townMoves[key]; pending != nil {
		pending.body = append(pending.body[:0], body...)
		p.townMoveMu.Unlock()
		return
	}
	pending := &townMovePending{body: append([]byte(nil), body...)}
	if p.townMoves == nil {
		p.townMoves = make(map[townMoveKey]*townMovePending)
	}
	p.townMoves[key] = pending
	p.townMoveMu.Unlock()
	discard := func() {
		p.townMoveMu.Lock()
		if p.townMoves[key] == pending {
			delete(p.townMoves, key)
		}
		p.townMoveMu.Unlock()
	}
	s.queueTownPeerDeliveryPolicy(target, source, func() bool { return s.onlinePlayers.presenceCurrent(actor) }, func() error {
		p.townMoveMu.Lock()
		latest := pending.body
		if p.townMoves[key] == pending {
			delete(p.townMoves, key)
		}
		p.townMoveMu.Unlock()
		return s.sendCurrentSceneFixedClass0Packet(p, op, latest, source)
	}, true, discard)
}

func (s *Service) queueTownPeerPacket(target onlinePlayerInfo, actor *onlinePlayerInfo, op uint16, body []byte, source string) {
	// op22 is replaceable position state. Enter/leave/control keep their FIFO
	// delivery contract; a failed socket write still retires a corrupted stream.
	if op == 22 && actor != nil {
		s.queueTownPeerMovement(target, *actor, op, body, source)
		return
	}
	body = append([]byte(nil), body...)
	var valid func() bool
	if actor != nil {
		snapshot := *actor
		// Position notifications are constructed from the incoming coordinates;
		// capture their authoritative presence epoch without replacing the body.
		if snapshot.presenceGeneration == 0 {
			current, ok := s.onlinePlayers.PlayerForCharacter(snapshot.CharacterID)
			if !ok || current.Session != snapshot.Session {
				return
			}
			snapshot = current
		}
		valid = func() bool { return s.onlinePlayers.presenceCurrent(snapshot) }
	}
	if op == currentTownUserLeaveMsgID && len(body) == 2 {
		departed := uint16(body[0]) | uint16(body[1])<<8
		// An old disconnect may enqueue after the same character has rebound.
		// The new area's enter owns that actor; never destroy it with old leave.
		valid = func() bool { return !s.onlinePlayers.PeerInSameArea(departed, target.CharacterID) }
	}
	s.queueTownPeerDelivery(target, source, valid, func() error {
		if op == currentTownUserLeaveMsgID && len(body) == 2 {
			target.Session.forgetTownDisplayPeer(uint16(body[0]) | uint16(body[1])<<8)
		}
		return s.sendCurrentSceneFixedClass0Packet(target.Session, op, body, source)
	})
}
