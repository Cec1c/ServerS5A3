package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type partyDungeonDeathDelivery struct {
	identity  boundGameSessionCharacter
	scene     *worldmap.DungeonSession
	room      *runtimeDungeonRoom
	instance  *dungeonPartyInstance
	from      uint16
	objectKey uint32
	body      []byte
	source    string
	phase     partyDeathDeliveryPhase
}

type partyDeathDeliveryPhase byte

const (
	partyDeathFull partyDeathDeliveryPhase = iota
	partyDeathVisualOnly
	partyDeathStateOnly
)

func (s *Service) deliverPartyDungeonDeath(d partyDungeonDeathDelivery) error {
	peer := d.identity.session
	peer.dungeon.mu.Lock()
	current := peer.dungeon.runtime
	if !s.boundGameSessionCharacterCurrent(d.identity) || peer.loginRevoked.Load() ||
		peer.gameTransportContext().Err() != nil || current == nil || current.townReturnPending ||
		current.Session != d.scene || current.Room != d.room || current.partyInstance != d.instance {
		peer.dungeon.mu.Unlock()
		return nil
	}
	// The instance may retain a stale scene registration until departure
	// cleanup. Check its current member, session and scene under its own lock.
	d.instance.mu.Lock()
	_, member := d.instance.members[d.identity.character]
	currentMember := member && d.instance.sessions[d.identity.character] == peer && d.instance.scenes[d.identity.character] == d.scene
	d.instance.mu.Unlock()
	if !currentMember {
		peer.dungeon.mu.Unlock()
		return nil
	}
	cleared, err := s.applyPartyDungeonDeath(d)
	peer.dungeon.mu.Unlock()
	if err != nil {
		s.logGameEvent(peer, "game-dungeon-party-instance-death-broadcast-failed",
			"object_key", d.objectKey, "from_char_id", d.from, "phase", "write", "error", err)
		return err
	}
	if d.phase == partyDeathVisualOnly {
		return nil // no quest, scene-clear or reward transition before experience
	}
	s.dispatchQuestMaterialPeer(peer)
	if cleared {
		// Keep completion after this recipient's successful death frame and
		// outside its dungeon lock (inline fixtures have no event loop).
		s.dispatchPartyDungeonPeerFinalRoomCompletionChecked(peer, d.instance.id, d.objectKey,
			func(runtime *runtimeDungeonState, _ worldmap.DungeonRoomScene) bool {
				return runtime.Room == d.room && runtime.Session == d.scene && runtime.partyInstance == d.instance
			})
	}
	return nil
}
