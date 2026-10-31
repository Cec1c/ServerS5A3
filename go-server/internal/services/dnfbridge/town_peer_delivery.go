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
		Data: gameSessionEventTask{source: source, characterID: identity.character, characterGeneration: identity.generation, async: true, run: func() error {
			ctx, cancel := context.WithTimeout(p.gameTransportContext(), createWriteTimeout)
			defer cancel()
			if err := lockCurrentCrossSceneProfile(ctx, &p.townMu); err != nil {
				if !errors.Is(err, context.Canceled) {
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
				if !errors.Is(err, context.Canceled) {
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
				p.closeGameTransport()
			}
			return nil
		}},
	})
	if err != nil {
		s.logGameEvent(p, "game-town-peer-delivery-unavailable", "source", source, "reason", "recipient_queue_rejected", "error", err)
		p.closeGameTransport()
	}
}

func (s *Service) queueTownPeerPacket(target onlinePlayerInfo, actor *onlinePlayerInfo, op uint16, body []byte, source string) {
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
		return s.sendCurrentSceneFixedClass0Packet(target.Session, op, body, source)
	})
}
