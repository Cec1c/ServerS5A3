package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func decodeCurrentWeddingRing(body []byte) (uint32, int, bool) {
	if len(body) != 18 {
		return 0, 0, false
	}
	slot, stage := binary.LittleEndian.Uint32(body[13:17]), int(body[17])
	return slot, stage, slot >= 3 && slot <= 288 && stage > 0
}

func weddingRingACK(result int32, stage byte) []byte {
	b := []byte{1, 0, 0, 0, 0, stage}
	binary.LittleEndian.PutUint32(b[1:], uint32(result))
	return b
}

// The caller owns wedding.mu. The partner may be offline; both persisted
// character rows are still loaded and revalidated inside the pair transaction.
func (s *Service) currentWeddingPartnerRequest(ctx context.Context, actor boundGameSessionCharacter) (wedding.PairRequest, []boundGameSessionCharacter, error) {
	c, err := s.currentWeddingCharacter(ctx, actor)
	if err != nil {
		return wedding.PairRequest{}, nil, err
	}
	state, err := wedding.Read(c)
	if err != nil || state.PartnerID == 0 {
		return wedding.PairRequest{}, nil, wedding.ErrState
	}
	repos, _ := s.repositoryGroup()
	id := strconv.FormatInt(state.PartnerID, 10)
	partner, found, err := repos.Character.Load(ctx, id)
	if err != nil {
		return wedding.PairRequest{}, nil, err
	}
	if !found || partner.CharacterID != id {
		return wedding.PairRequest{}, nil, wedding.ErrState
	}
	if _, _, err = wedding.ValidatePair(c, partner); err != nil {
		return wedding.PairRequest{}, nil, err
	}
	actors := []boundGameSessionCharacter{actor}
	if peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(state.PartnerID)); ok {
		actors = append(actors, peer)
	}
	req := wedding.PairRequest{Actor: wedding.Actor{AccountID: c.AccountID, CharacterID: c.CharacterID}, Partner: wedding.Actor{AccountID: partner.AccountID, CharacterID: id}, Now: s.gameplayNow(), Revalidate: func() bool {
		for _, identity := range actors {
			if !s.boundGameSessionCharacterCurrent(identity) {
				return false
			}
		}
		return true
	}}
	return req, actors, nil
}

func (s *Service) handleCurrentWeddingRing(session *gameSession, body []byte) error {
	fail := func() error {
		return s.sendGameUpperRawClass(session, currentWeddingRingOpcode, weddingRingACK(-2, 255), 1)
	}
	slot, stage, ok := decodeCurrentWeddingRing(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !ok || !bound || !s.currentWeddingCeremonyEntryReady(actor) {
		return fail()
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	req, actors, err := s.currentWeddingPartnerRequest(ctx, actor)
	if err != nil {
		return fail()
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, actors)
	if err != nil {
		return fail()
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		release()
		return err
	}
	result, err := owner.UpgradeRing(ctx, req, stage, strconv.Itoa(int(r.MainInventoryListType))+":"+strconv.FormatUint(uint64(slot), 10))
	release()
	if err != nil {
		s.logGameEvent(session, "game-wedding-ring-rejected", "error", err)
		return fail()
	}
	// Persisted pair state is the recovery source if either socket write fails.
	published := errors.Join(s.sendGameUpperRawClass(session, currentWeddingRingOpcode, weddingRingACK(0, byte(stage)), 1), s.publishCurrentWeddingPair(result))
	// The inventory projection above may be the last main-bag update for
	// either spouse. Replay both fresh relationship/room states after it.
	published = errors.Join(published, s.restoreCurrentWeddingRingBenefits(ctx, actors))
	// The initiating CMD already displays its result. Notify only the bound
	// spouse, once, using the native one-byte level consumer (C0/1001).
	for _, peer := range actors {
		if peer.character != actor.character && s.boundGameSessionCharacterCurrent(peer) {
			published = errors.Join(published, s.sendGameUpperRawClass(peer.session, currentWeddingRingNotice, []byte{byte(stage)}, 0))
		}
	}
	return published
}

// Caller owns wedding.mu. Keep the actual participants' asset leases through
// the final replay so an older projection cannot clear a newer bag's benefits.
func (s *Service) restoreCurrentWeddingRingBenefits(ctx context.Context, actors []boundGameSessionCharacter) error {
	current := make([]boundGameSessionCharacter, 0, len(actors))
	for _, identity := range actors {
		if s.boundGameSessionCharacterCurrent(identity) {
			current = append(current, identity)
		}
	}
	actors = current
	if len(actors) == 0 {
		return nil
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, actors)
	if err != nil {
		return err
	}
	defer release()
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return err
	}
	var published error
	for _, identity := range actors {
		if !s.boundGameSessionCharacterCurrent(identity) {
			continue
		}
		fresh, e := s.currentWeddingCharacter(ctx, identity)
		if e != nil {
			published = errors.Join(published, e)
			continue
		}
		state, e := wedding.Read(fresh)
		if e != nil || state.Phase != wedding.Married {
			published = errors.Join(published, e)
			continue
		}
		partner, found, e := owner.Repositories.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if e != nil || !found {
			published = errors.Join(published, e, wedding.ErrState)
			continue
		}
		room, e := owner.ReadRoom(ctx, fresh, partner)
		if e != nil {
			published = errors.Join(published, e)
			continue
		}
		if !s.boundGameSessionCharacterCurrent(identity) {
			continue
		}
		e = s.sendCurrentWeddingInfoAndPartnerLocked(identity.session, fresh)
		if e == nil && s.boundGameSessionCharacterCurrent(identity) {
			e = s.writeCurrentWeddingRoom(identity.session, room)
		}
		published = errors.Join(published, e)
	}
	return published
}
