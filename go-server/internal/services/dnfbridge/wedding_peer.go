package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// 1AD6FB0 (NOTI1013) reads runtime UID/partner/phase/ring/room points.
// It skips the selected actor: own abilities are projected by Info1000 and
// Room1008. Both native paths apply their PVF ability tables once.
func buildCurrentWeddingPeerInfo(actor, partner uint32, phase, ring byte, points uint32) []byte {
	// NOTI1013 resolves the nametag animation without checking the phase.
	// Its PVF table has stages 0..5; unlike own Info1000, 255 is not a safe
	// non-married sentinel and reaches a null animation path in the client.
	if phase != wedding.Married {
		ring = 0
	}
	b := make([]byte, 14)
	binary.LittleEndian.PutUint32(b, actor)
	binary.LittleEndian.PutUint32(b[4:], partner)
	b[8], b[9] = phase, ring
	binary.LittleEndian.PutUint32(b[10:], points)
	return b
}

func (s *Service) currentWeddingPeerInfo(ctx context.Context, c r.CharacterRecord) ([]byte, error) {
	if s.options.currentClientVersion != "official-24.9" {
		return nil, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil, wedding.ErrUnavailable
	}
	var state wedding.State
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		// Actor appearance may have been captured before a concurrent wedding
		// commit. Resolve both sides again instead of pairing old ring/room
		// values with a freshly loaded spouse.
		fresh, found, loadErr := repos.Character.Load(ctx, c.CharacterID)
		if loadErr != nil {
			return nil, loadErr
		}
		if !found || fresh.AccountID != c.AccountID || fresh.CharacterID != c.CharacterID {
			return nil, wedding.ErrOwnership
		}
		state, err = wedding.Read(fresh)
		if err != nil {
			return nil, err
		}
		if state.Phase == wedding.Single {
			return nil, nil
		}
		partner, found, loadErr := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if loadErr != nil {
			return nil, loadErr
		}
		if !found {
			return nil, wedding.ErrState
		}
		if _, _, err = wedding.ValidatePair(fresh, partner); err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	id, err := wedding.NumericID(c.CharacterID)
	if err != nil {
		return nil, err
	}
	ring := byte(0)
	if state.Phase == wedding.Married {
		rules, err := s.currentWeddingRules()
		if err != nil {
			return nil, err
		}
		if _, err = rules.Attributes(state.Ring, state.RoomPoints, true, false); err != nil {
			return nil, err
		}
		ring = byte(state.Ring)
	}
	return buildCurrentWeddingPeerInfo(uint32(id), currentWeddingRelationToken(state), state.Phase, ring, uint32(state.RoomPoints)), nil
}

func (s *Service) sendCurrentWeddingPeerInfo(ctx context.Context, target *gameSession, c r.CharacterRecord) error {
	body, err := s.currentWeddingPeerInfo(ctx, c)
	if err != nil {
		// Marriage is supplemental actor state. A changing pair snapshot must
		// not stop the remaining generic mode0/mode1/party-slot projection.
		s.logGameEvent(target, "game-wedding-peer-state-deferred", "actor", c.CharacterID, "error", err)
		return nil
	}
	if len(body) == 0 {
		return nil
	}
	return s.sendGameUpperRawClass(target, 1013, body, 0)
}

// Updating an existing actor only needs NOTI1013. Reuse the exact town or
// dungeon-run visibility scope used by actor creation; never lock dungeon.mu
// while holding the wedding owner lock.
func (s *Service) broadcastCurrentWeddingPeerInfo(actor boundGameSessionCharacter, c r.CharacterRecord) error {
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	body, err := s.currentWeddingPeerInfo(ctx, c)
	if err != nil || len(body) == 0 {
		return err
	}
	return s.broadcastCurrentWeddingPeerBody(actor, body)
}

// A cancellation explicitly projects Single to remove an existing peer's
// couple marker. Actor creation can continue to omit the all-zero state.
func (s *Service) broadcastCurrentWeddingPeerBody(actor boundGameSessionCharacter, body []byte) error {
	if !s.boundGameSessionCharacterCurrent(actor) || len(body) != 14 {
		return nil
	}
	return s.sendCurrentWeddingPeerBody(actor, body, s.currentWeddingPeerReceivers(actor))
}

// Receiver discovery can inspect dungeon scopes. Keep it outside wedding.mu;
// callers that serialize a state refresh may only lock around the final read
// and send after this snapshot has been collected.
func (s *Service) currentWeddingPeerReceivers(actor boundGameSessionCharacter) map[uint16]boundGameSessionCharacter {
	peers := make(map[uint16]boundGameSessionCharacter)
	if !s.boundGameSessionCharacterCurrent(actor) {
		return peers
	}
	key, inDungeon := s.currentDungeonPartyRunKey(actor.session)
	if inDungeon {
		for _, member := range runtimePartyMembers(runtimePartyStateSnapshot(actor.session)) {
			if member.UserID == actor.character {
				continue
			}
			peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
			if !online {
				continue
			}
			if other, ready := s.currentDungeonPartyRunKey(peer.session); ready && other == key {
				peers[peer.character] = peer
			}
		}
	} else if s.onlinePlayers != nil {
		for _, p := range s.onlinePlayers.PeersInSameArea(actor.character) {
			peer, online := s.onlineGameSessionCharacterSnapshot(p.CharacterID)
			if !online || peer.session != p.Session {
				continue
			}
			if _, inDungeon := s.currentDungeonPartyRunKey(peer.session); !inDungeon {
				peers[peer.character] = peer
			}
		}
	}
	return peers
}

func (s *Service) sendCurrentWeddingPeerBody(actor boundGameSessionCharacter, body []byte, peers map[uint16]boundGameSessionCharacter) error {
	if len(body) != 14 {
		return nil
	}
	var err error
	for _, peer := range peers {
		if s.boundGameSessionCharacterCurrent(peer) && s.boundGameSessionCharacterCurrent(actor) {
			err = errors.Join(err, s.sendGameUpperRawClass(peer.session, 1013, body, 0))
		}
	}
	return err
}
