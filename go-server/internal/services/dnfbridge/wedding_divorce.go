package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func (s *Service) handleCurrentWeddingDivorceRequest(session *gameSession, request currentWeddingRequest) error {
	fail := func(e error) error {
		code := int32(-1)
		if errors.Is(e, wedding.ErrGold) {
			code = -7
		}
		return s.sendGameUpperRawClass(session, 1032, buildCurrentWeddingRequestRecord(request, code, true), 1)
	}
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || (request.Operation != 6 && request.Operation != 7) || request.Kind != 0 || request.Duration != 0 || s.validateUpgradeNPCScene(actor, 20013) != nil {
		return fail(wedding.ErrState)
	}
	var immediate []currentWeddingClearTransport
	var published error
	e := func() error {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		req, actors, e := s.currentWeddingPartnerRequest(ctx, actor)
		if e != nil {
			return e
		}
		owner, e := s.currentWeddingOwner()
		if e != nil {
			return e
		}
		a, e := s.currentWeddingCharacter(ctx, actor)
		if e != nil {
			return e
		}
		state, e := wedding.Read(a)
		if e != nil || state.Phase != wedding.Married {
			return wedding.ErrState
		}
		b, found, e := owner.Repositories.Character.Load(ctx, req.Partner.CharacterID)
		if e != nil {
			return e
		}
		if !found {
			return wedding.ErrState
		}
		if request.Operation == 7 {
			if request.Target != uint32(state.PartnerID) {
				return wedding.ErrOwnership
			}
			to, online := s.onlineGameSessionCharacterSnapshot(uint16(state.PartnerID))
			if !online || !s.currentWeddingPriestScene(actor, to) || s.validateUpgradeNPCScene(to, 20013) != nil {
				return wedding.ErrState
			}
			if s.wedding.invitations == nil {
				s.wedding.invitations = map[uint16]*currentWeddingInvitation{}
			}
			for key, inv := range s.wedding.invitations {
				if !s.boundGameSessionCharacterCurrent(inv.to) {
					delete(s.wedding.invitations, key)
					continue
				}
				if inv.to.character == to.character || s.gameplayNow().Before(inv.expires) && (inv.from.character == actor.character || inv.from.character == to.character || inv.to.character == actor.character) {
					return wedding.ErrState
				}
			}
			inv := &currentWeddingInvitation{from: actor, to: to, request: request, expires: s.gameplayNow().Add(30 * time.Second)}
			s.wedding.invitations[to.character] = inv
			if e = s.sendGameUpperRawClass(session, 1032, buildCurrentWeddingRequestRecord(request, 0, true), 1); e != nil {
				delete(s.wedding.invitations, to.character)
				return e
			}
			notice := request
			notice.Target = uint32(actor.character)
			if e = s.sendGameUpperRawClass(to.session, 997, buildCurrentWeddingRequestRecord(notice, 0, false), 0); e != nil {
				delete(s.wedding.invitations, to.character)
			}
			return e
		}
		if request.Target != 0 {
			return wedding.ErrOwnership
		}
		release, e := s.reserveCurrentGuildParticipants(ctx, actors)
		if e != nil {
			return e
		}
		defer release()
		original := req.Revalidate
		req.Revalidate = func() bool { return original() && s.validateUpgradeNPCScene(actor, 20013) == nil }
		outcome := &currentWeddingCancellationOutcome{done: make(chan struct{})}
		defer outcome.resolve(false)
		immediate, e = s.reserveCurrentWeddingDivorceDeliveries(session, outcome, 6, a, b)
		if e != nil {
			return e
		}
		rules, e := s.currentWeddingExtensionRules()
		if e != nil {
			return e
		}
		_, e = owner.Divorce(ctx, req, rules, 6)
		if e != nil {
			return e
		}
		published = s.sendGameUpperRawClass(session, 1032, buildCurrentWeddingRequestRecord(request, 0, true), 1)
		outcome.resolve(true)
		s.retireCurrentWeddingDivorceInvitationsLocked(actor.character, uint16(state.PartnerID))
		for _, pin := range immediate {
			published = errors.Join(published, s.deliverCurrentWeddingDivorceLocked(pin, 6))
		}
		return nil
	}()
	if e != nil {
		s.logGameEvent(session, "game-wedding-divorce-rejected", "operation", request.Operation, "error", e)
		return fail(e)
	}
	for _, pin := range immediate {
		published = errors.Join(published, s.deliverCurrentWeddingDivorceProjections(pin))
	}
	return published
}

func (s *Service) handleCurrentWeddingDivorceResponse(session *gameSession, response currentWeddingResponse) error {
	fail := func(e error) error {
		code := int32(-1)
		if errors.Is(e, wedding.ErrGold) {
			code = -7
		}
		return s.sendGameUpperRawClass(session, 1033, buildCurrentWeddingResponseRecord(response, code, true), 1)
	}
	to, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || response.Operation != 7 {
		return fail(wedding.ErrState)
	}
	var immediate []currentWeddingClearTransport
	var published error
	var matched *currentWeddingInvitation
	e := func() error {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		inv := s.wedding.invitations[to.character]
		if inv == nil || inv.to != to || inv.request.Operation != 7 || (response.Target != uint32(inv.from.character) && (response.Target != 0 || response.Answer == 0)) {
			return wedding.ErrState
		}
		matched = inv
		delete(s.wedding.invitations, to.character)
		response.Target = uint32(inv.from.character)
		response.Kind, response.Duration = 0, 0
		if !s.gameplayNow().Before(inv.expires) || !s.boundGameSessionCharacterCurrent(inv.from) {
			response.Answer = 2
		}
		if response.Answer != 0 {
			published = s.sendGameUpperRawClass(session, 1033, buildCurrentWeddingResponseRecord(response, 0, true), 1)
			notice := response
			notice.Target = uint32(to.character)
			if s.boundGameSessionCharacterCurrent(inv.from) {
				published = errors.Join(published, s.sendGameUpperRawClass(inv.from.session, 998, buildCurrentWeddingResponseRecord(notice, 0, false), 0))
			}
			return nil
		}
		if !s.currentWeddingPriestScene(inv.from, to) || s.validateUpgradeNPCScene(inv.from, 20013) != nil || s.validateUpgradeNPCScene(to, 20013) != nil {
			return wedding.ErrState
		}
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		release, e := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{inv.from, to})
		if e != nil {
			return e
		}
		defer release()
		owner, e := s.currentWeddingOwner()
		if e != nil {
			return e
		}
		a, e := s.currentWeddingCharacter(ctx, inv.from)
		if e != nil {
			return e
		}
		b, e := s.currentWeddingCharacter(ctx, to)
		if e != nil {
			return e
		}
		req := s.weddingPairRequest(inv.from, to)
		previous := req.Revalidate
		req.Revalidate = func() bool {
			return previous() && s.currentWeddingPriestScene(inv.from, to) && s.validateUpgradeNPCScene(inv.from, 20013) == nil && s.validateUpgradeNPCScene(to, 20013) == nil
		}
		outcome := &currentWeddingCancellationOutcome{done: make(chan struct{})}
		defer outcome.resolve(false)
		immediate, e = s.reserveCurrentWeddingDivorceDeliveries(session, outcome, 7, a, b)
		if e != nil {
			return e
		}
		rules, e := s.currentWeddingExtensionRules()
		if e != nil {
			return e
		}
		_, e = owner.Divorce(ctx, req, rules, 7)
		if e != nil {
			return e
		}
		published = s.sendGameUpperRawClass(session, 1033, buildCurrentWeddingResponseRecord(response, 0, true), 1)
		notice := response
		notice.Target = uint32(to.character)
		if s.boundGameSessionCharacterCurrent(inv.from) {
			published = errors.Join(published, s.sendGameUpperRawClass(inv.from.session, 998, buildCurrentWeddingResponseRecord(notice, 0, false), 0))
		}
		outcome.resolve(true)
		s.retireCurrentWeddingDivorceInvitationsLocked(inv.from.character, to.character)
		for _, pin := range immediate {
			published = errors.Join(published, s.deliverCurrentWeddingDivorceLocked(pin, 7))
		}
		return nil
	}()
	if e != nil {
		s.logGameEvent(session, "game-wedding-divorce-consent-rejected", "error", e)
		if matched != nil {
			return s.publishCurrentWeddingConsentFailure(matched, response, e)
		}
		return fail(e)
	}
	for _, pin := range immediate {
		published = errors.Join(published, s.deliverCurrentWeddingDivorceProjections(pin))
	}
	return published
}

func (s *Service) retireCurrentWeddingDivorceInvitationsLocked(a, b uint16) {
	for _, inv := range s.wedding.invitations {
		if inv.from.character == a || inv.from.character == b || inv.to.character == a || inv.to.character == b {
			inv.expires = s.gameplayNow()
		}
	}
}

func (s *Service) reserveCurrentWeddingDivorceDeliveries(source *gameSession, outcome *currentWeddingCancellationOutcome, op byte, characters ...r.CharacterRecord) ([]currentWeddingClearTransport, error) {
	var immediate []currentWeddingClearTransport
	for _, character := range characters {
		id, e := wedding.NumericID(character.CharacterID)
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		candidates := map[*gameSession]bool{}
		if current := s.gameSessions[uint16(id)]; current != nil {
			candidates[current] = true
		}
		for _, session := range s.liveGameSessions {
			if session != nil && session.selectedCharacterID == uint16(id) {
				candidates[session] = true
			}
		}
		s.mu.Unlock()
		for session := range candidates {
			pin, valid := s.currentWeddingClearTransport(session, character.AccountID)
			if !valid || pin.transport.character != uint16(id) {
				continue
			}
			if session == source || session.events == nil || session.events.loop == nil {
				immediate = append(immediate, pin)
				continue
			}
			if e = s.tryPostGameSessionCharacterEvent(session, "game-wedding-divorce-committed", pin.transport.character, pin.transport.generation, func() error {
				<-outcome.done
				if !outcome.committed {
					return nil
				}
				return s.deliverCurrentWeddingDivorce(pin, op)
			}); e != nil {
				return nil, e
			}
		}
	}
	return immediate, nil
}

func (s *Service) deliverCurrentWeddingDivorceLocked(pin currentWeddingClearTransport, op byte) error {
	if !s.clearCurrentWeddingPreparationReplayForTransport(pin) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	fresh, e := s.currentWeddingCharacter(ctx, pin.canonical)
	if e != nil {
		return e
	}
	state, e := wedding.Read(fresh)
	if e != nil {
		return e
	}
	var published error
	if state.Phase == wedding.Single {
		published = errors.Join(published, s.sendGameUpperRawClass(pin.transport.session, 1002, []byte{op}, 0), s.writeCurrentWeddingRoom(pin.transport.session, wedding.Room{}), s.sendGameUpperRawClass(pin.transport.session, 1015, make([]byte, 7), 0))
	}
	published = errors.Join(published, s.sendCurrentWeddingInfoAndPartnerLocked(pin.transport.session, fresh), s.sendCarryGoldWallet(pin.transport.session, fresh.Stats["gold"]))
	s.logGameEvent(pin.transport.session, "game-wedding-divorce-state-published", "operation", op, "character", strconv.Itoa(int(pin.transport.character)))
	return published
}
func (s *Service) deliverCurrentWeddingDivorce(pin currentWeddingClearTransport, op byte) error {
	e := func() error {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		return s.deliverCurrentWeddingDivorceLocked(pin, op)
	}()
	return errors.Join(e, s.deliverCurrentWeddingDivorceProjections(pin))
}

func (s *Service) deliverCurrentWeddingDivorceProjections(pin currentWeddingClearTransport) error {
	published := s.deliverCurrentWeddingCancellationProjections(pin)
	if pin.transport != pin.canonical || !s.currentWeddingClearTransportCurrent(pin) {
		return published
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	c, e := s.currentWeddingCharacter(ctx, pin.canonical)
	if e != nil {
		return errors.Join(published, e)
	}
	state, e := wedding.Read(c)
	if e != nil {
		return errors.Join(published, e)
	}
	if state.Phase == wedding.Single && c.Stats["emotion_index"] == 0 {
		// Reuse the established guarded appearance refresh after all wedding,
		// asset and dungeon locks have been released. It reads the fresh mood.
		invalidateCurrentCrossSceneAppearance(pin.transport.session)
		published = errors.Join(published, s.refreshCurrentEmotionActors(ctx, pin.canonical))
	}
	return published
}
