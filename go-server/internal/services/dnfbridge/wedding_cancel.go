package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/wedding"
)

// Native 1acf94f sends op5 with Target/Kind/Duration all zero. The actual
// partner must come from the requesting character, including an offline one.
func (s *Service) handleCurrentWeddingCancellation(session *gameSession, request currentWeddingRequest) error {
	fail := func(reason string, result int32) error {
		err := s.sendGameUpperRawClass(session, currentWeddingRequestOpcode, buildCurrentWeddingRequestRecord(request, result, true), 1)
		s.logCurrentWeddingCancellationDiagnostic(session, request, reason, result, err == nil)
		return err
	}
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return fail("not_bound", -1)
	}
	if request.Target != 0 || request.Kind != 0 || request.Duration != 0 {
		return fail("malformed_fields", -1)
	}
	if s.validateUpgradeNPCScene(actor, 20013) != nil {
		return fail("npc_scene", -1)
	}
	var result wedding.PairResult
	var published error
	var immediate []currentWeddingClearTransport
	reason := "domain_error"
	err := func() error {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		release, e := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{actor})
		if e != nil {
			reason = "participant_reservation"
			return e
		}
		defer release()
		character, e := s.currentWeddingCharacter(ctx, actor)
		if e != nil {
			reason = currentWeddingCancellationErrorReason(e)
			return e
		}
		state, e := wedding.Read(character)
		if e != nil || state.Phase != wedding.Engaged {
			reason = "state"
			return wedding.ErrState
		}
		repos, available := s.repositoryGroup()
		if !available || repos.Character == nil {
			reason = "repository_unavailable"
			return wedding.ErrUnavailable
		}
		partner, found, e := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if e != nil {
			reason = "partner_load"
			return e
		}
		if !found || partner.CharacterID != strconv.FormatInt(state.PartnerID, 10) {
			reason = "partner_state"
			return wedding.ErrState
		}
		owner, e := s.currentWeddingOwner()
		if e != nil {
			reason = "owner_unavailable"
			return e
		}
		outcome := &currentWeddingCancellationOutcome{done: make(chan struct{})}
		defer outcome.resolve(false) // Every rejection and panic releases reserved events.
		immediate, e = s.reserveCurrentWeddingCancellationDeliveries(session, outcome, character, partner)
		if e != nil {
			reason = "delivery_reservation"
			return e
		}
		result, e = owner.CancelEngagement(ctx, wedding.PairRequest{
			Actor:   wedding.Actor{AccountID: character.AccountID, CharacterID: character.CharacterID},
			Partner: wedding.Actor{AccountID: partner.AccountID, CharacterID: partner.CharacterID}, Now: s.gameplayNow(),
			Revalidate: func() bool {
				return s.boundGameSessionCharacterCurrent(actor) && s.validateUpgradeNPCScene(actor, 20013) == nil
			},
		})
		if e != nil {
			reason = currentWeddingCancellationErrorReason(e)
			return e
		}
		outcome.resolve(true)
		// Keep incoming dialogs fenced until their native reply drains, but an
		// already offered reservation can no longer commit after cancellation.
		for _, invitation := range s.wedding.invitations {
			if invitation.from.character == actor.character || invitation.to.character == actor.character || int64(invitation.from.character) == state.PartnerID || int64(invitation.to.character) == state.PartnerID {
				invitation.expires = s.gameplayNow()
			}
		}
		// ACK5 clears the requester's local couple UI. Follow with both owned
		// durable snapshots, continuing the other socket if one write fails.
		published = s.sendGameUpperRawClass(session, currentWeddingRequestOpcode, buildCurrentWeddingRequestRecord(request, 0, true), 1)
		s.logCurrentWeddingCancellationDiagnostic(session, request, "committed", 0, published == nil)
		published = errors.Join(published, s.sendCarryGoldWallet(session, result.Actor.Stats["gold"]))
		for _, pin := range immediate {
			published = errors.Join(published, s.deliverCurrentWeddingCancellationLocked(pin))
		}
		return nil
	}()
	if err != nil {
		s.logGameEvent(session, "game-wedding-cancellation-rejected", "error", err)
		// Current ACK1032 switch 1ad906c maps -7 to 1ad90af/str1660,
		// the native insufficient-gold prompt. -1 is its generic failure path.
		if errors.Is(err, wedding.ErrGold) {
			return fail(reason, -7)
		}
		return fail(reason, -1)
	}
	// These projections may acquire a dungeon lock, so release wedding.mu
	// and participant reservations first. Read fresh state before publishing.
	for _, pin := range immediate {
		published = errors.Join(published, s.deliverCurrentWeddingCancellationProjections(pin))
	}
	return published
}

func currentWeddingCancellationErrorReason(err error) string {
	switch {
	case errors.Is(err, wedding.ErrGold):
		return "gold"
	case errors.Is(err, wedding.ErrOwnership):
		return "ownership"
	case errors.Is(err, wedding.ErrState):
		return "state"
	case errors.Is(err, wedding.ErrUnavailable):
		return "unavailable"
	default:
		return "domain_error"
	}
}

// Receiver collection has already released any dungeon locks. Serialize the
// fresh relationship read and packet write with a possible new engagement, so
// a delayed cancellation cannot overwrite its newer peer projection.
func (s *Service) publishCurrentWeddingCancellationPeerState(actor boundGameSessionCharacter, receivers map[uint16]boundGameSessionCharacter) error {
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	fresh, err := s.currentWeddingCharacter(ctx, actor)
	if err != nil {
		return err
	}
	state, err := wedding.Read(fresh)
	if err != nil {
		return err
	}
	var body []byte
	if state.Phase == wedding.Single {
		body = buildCurrentWeddingPeerInfo(uint32(actor.character), 0, wedding.Single, 0, 0)
	} else {
		body, err = s.currentWeddingPeerInfo(ctx, fresh)
		if err != nil {
			return err
		}
	}
	return s.sendCurrentWeddingPeerBody(actor, body, receivers)
}
