package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentWeddingRuntime struct {
	catalogMu           sync.Mutex
	rules               *wedding.Rules
	loadErr             error
	extensionRules      *wedding.ExtensionRules
	extensionLoadErr    error
	guestRules          *wedding.GuestRules
	guestRulesErr       error
	guestBegins         map[uint16]currentWeddingGuestBeginStamp
	movieEnds           map[uint16]*currentWeddingMovieEnd
	avatarSetups        map[uint16]*currentWeddingAvatarStamp
	churchScenes        map[uint16]currentWeddingChurchLoadingProof
	partnerMoves        map[uint16]*currentWeddingPartnerMovePending
	guestSettlements    map[uint32]*currentWeddingGuestSettlement
	recoveryTimer       *time.Timer
	extensionRequests   map[uint16]*currentWeddingExtensionRequestState
	mu                  sync.Mutex
	invitations         map[uint16]*currentWeddingInvitation
	reservationTimeouts map[uint16]*currentWeddingInvitation
	roomEditors         map[string]boundGameSessionCharacter
	lovePointUses       map[uint16]*currentWeddingLovePointUseState
	ceremonies          map[string]*currentWeddingCeremony
	ceremonyLogins      map[uint16]currentWeddingCeremonyLoginRecovery
	loadingDiagnostics  map[uint16]string
	ceremoniesStopped   bool
}

type currentWeddingInvitation struct {
	from, to boundGameSessionCharacter
	request  currentWeddingRequest
	expires  time.Time
	timer    *time.Timer
}

func (s *Service) currentWeddingRules() (*wedding.Rules, error) {
	s.wedding.catalogMu.Lock()
	defer s.wedding.catalogMu.Unlock()
	if s.wedding.rules != nil || s.wedding.loadErr != nil {
		return s.wedding.rules, s.wedding.loadErr
	}
	s.initialEquipmentMu.Lock()
	a, e := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if e == nil {
		s.wedding.rules, e = wedding.Load(a)
	}
	s.wedding.loadErr = e
	return s.wedding.rules, e
}

func (s *Service) currentWeddingOwner() (wedding.Owner, error) {
	rules, e := s.currentWeddingRules()
	if e != nil {
		return wedding.Owner{}, e
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return wedding.Owner{}, wedding.ErrUnavailable
	}
	return wedding.Owner{Repositories: repos, Rules: rules}, nil
}

func (s *Service) currentWeddingCharacter(ctx context.Context, identity boundGameSessionCharacter) (dnfrepo.CharacterRecord, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil || !s.boundGameSessionCharacterCurrent(identity) {
		return dnfrepo.CharacterRecord{}, wedding.ErrOwnership
	}
	c, found, e := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if e != nil {
		return c, e
	}
	if !found || c.CharacterID != strconv.Itoa(int(identity.character)) || c.AccountID != s.accountIDForSession(identity.session) {
		return dnfrepo.CharacterRecord{}, wedding.ErrOwnership
	}
	return c, nil
}

// Both actors must inhabit the same current scene containing the PVF priest.
// Mouse-opened NPC windows do not imply the keyboard-nearby radius; use the
// actual map/NPC ownership instead of inventing a new distance constant.
func (s *Service) currentWeddingPriestScene(a, b boundGameSessionCharacter) bool {
	if s.onlinePlayers == nil || !s.boundGameSessionCharacterCurrent(a) || !s.boundGameSessionCharacterCurrent(b) {
		return false
	}
	x, ok := s.onlinePlayers.PlayerForCharacter(a.character)
	if !ok || x.Session != a.session {
		return false
	}
	y, ok := s.onlinePlayers.PlayerForCharacter(b.character)
	if !ok || y.Session != b.session {
		return false
	}
	if x.TownID != y.TownID || x.AreaID != y.AreaID || x.ChannelID != y.ChannelID || x.Instance != y.Instance || x.Instance != 0 {
		return false
	}
	area, ok := s.townArea(int64(x.TownID), int64(x.AreaID))
	if !ok {
		return false
	}
	catalog, e := s.currentPVFItemCatalog()
	if e != nil {
		return false
	}
	mapDef, e := worldmap.LoadTownPlacementMap(catalog.source, area.MapPath)
	if e != nil {
		return false
	}
	for _, npc := range mapDef.NPCs {
		if npc.NPCID == 20013 {
			return true
		}
	}
	return false
}

func (s *Service) weddingPairRequest(a, b boundGameSessionCharacter) wedding.PairRequest {
	return wedding.PairRequest{Actor: wedding.Actor{AccountID: s.accountIDForSession(a.session), CharacterID: strconv.Itoa(int(a.character))}, Partner: wedding.Actor{AccountID: s.accountIDForSession(b.session), CharacterID: strconv.Itoa(int(b.character))}, Now: s.gameplayNow(), Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(a) && s.boundGameSessionCharacterCurrent(b) }}
}

func (s *Service) handleCurrentWeddingRequest(session *gameSession, body []byte) error {
	r, ok := decodeCurrentWeddingRequest(body)
	s.logCurrentWeddingRequestDiagnostic(session, len(body), r, ok)
	if !ok {
		return s.sendGameUpperFailure(session, currentWeddingRequestOpcode, 1)
	}
	if r.Operation == 6 || r.Operation == 7 {
		return s.handleCurrentWeddingDivorceRequest(session, r)
	}
	fail := func(result int32) error {
		return s.sendGameUpperRawClass(session, currentWeddingRequestOpcode, buildCurrentWeddingRequestRecord(r, result, true), 1)
	}
	if r.Operation == 5 {
		return s.handleCurrentWeddingCancellation(session, r)
	}
	if r.Operation != 1 && r.Operation != 2 {
		return fail(-1)
	}
	from, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || r.Target == 0 || r.Target == uint32(from.character) {
		return fail(-1)
	}
	to, online := s.onlineGameSessionCharacterSnapshot(uint16(r.Target))
	if !online {
		return fail(-1)
	}
	if s.validateUpgradeNPCScene(from, 20013) != nil || s.validateUpgradeNPCScene(to, 20013) != nil {
		return fail(-1)
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	release, e := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{from, to})
	if e != nil {
		return fail(-1)
	}
	defer release()
	if !s.currentWeddingPriestScene(from, to) {
		return fail(-1)
	}
	owner, e := s.currentWeddingOwner()
	if e != nil {
		return e
	}
	a, e := s.currentWeddingCharacter(ctx, from)
	if e != nil {
		return fail(-1)
	}
	b, e := s.currentWeddingCharacter(ctx, to)
	if e != nil {
		return fail(-1)
	}
	x, e := wedding.Read(a)
	if e != nil {
		return fail(-1)
	}
	y, e := wedding.Read(b)
	if e != nil {
		return fail(-1)
	}
	if r.Operation == 1 {
		if x.Phase != wedding.Single || y.Phase != wedding.Single || int64(a.Level) < owner.Rules.MinimumLevel || int64(b.Level) < owner.Rules.MinimumLevel || a.AccountID == b.AccountID {
			return fail(-1)
		}
	} else {
		if _, _, e = wedding.ValidatePair(a, b); e != nil || x.Phase != wedding.Engaged || int(r.Duration) >= len(owner.Rules.Durations) {
			return fail(-1)
		}
		if _, ok = owner.Rules.Ceremonies[r.Kind]; !ok {
			return fail(-1)
		}
		if x.LastWeddingDay >= currentWeddingDay(s.gameplayNow()) || y.LastWeddingDay >= currentWeddingDay(s.gameplayNow()) {
			return s.sendCurrentWeddingReservationDailyLimit(session, r)
		}
	}
	if s.wedding.invitations == nil {
		s.wedding.invitations = make(map[uint16]*currentWeddingInvitation)
	}
	for key, inv := range s.wedding.invitations {
		if inv.request.Operation == 2 && !s.gameplayNow().Before(inv.expires) {
			_ = s.expireCurrentWeddingReservationInvitationLocked(inv)
			continue
		}
		if !s.boundGameSessionCharacterCurrent(inv.to) {
			if inv.timer != nil {
				inv.timer.Stop()
			}
			delete(s.wedding.invitations, key)
			continue
		}
		// Keep expired incoming dialogs fenced until their response drains.
		if inv.to.character == to.character || s.gameplayNow().Before(inv.expires) && (inv.from.character == from.character || inv.from.character == to.character || inv.to.character == from.character) {
			if r.Operation == 2 {
				return s.sendCurrentWeddingRequestHint(session, r, "仍有一份婚礼申请等待答复，请先处理或等待其超时。")
			}
			return fail(-1)
		}
	}
	if r.Operation == 2 {
		if e := s.primeCurrentWeddingReservationProfiles(ctx, from, to, a, b); e != nil {
			s.logInfo("dnfbridge wedding peer profile deferred", "event", "game-wedding-invitation-profile-deferred", "error", e)
			return s.sendCurrentWeddingRequestHint(session, r, "角色资料同步未完成，请重新打开婚礼窗口后再试。")
		}
	}
	inv := &currentWeddingInvitation{from: from, to: to, request: r, expires: s.gameplayNow().Add(30 * time.Second)}
	s.wedding.invitations[to.character] = inv
	if e = s.sendGameUpperRawClass(session, currentWeddingRequestOpcode, buildCurrentWeddingRequestRecord(r, 0, true), 1); e != nil {
		delete(s.wedding.invitations, to.character)
		return e
	}
	notice := r
	notice.Target = uint32(from.character)
	if e = s.sendGameUpperRawClass(to.session, currentWeddingRequestNotice, buildCurrentWeddingRequestRecord(notice, 0, false), 0); e != nil {
		delete(s.wedding.invitations, to.character)
		return e
	}
	s.logGameEvent(session, "game-wedding-consent-requested", "target", to.character, "operation", r.Operation)
	if r.Operation == 2 {
		inv.expires = s.gameplayNow().Add(currentWeddingReservationReplyTimeout)
		s.armCurrentWeddingReservationInvitationLocked(inv)
	}
	return nil
}

func (s *Service) handleCurrentWeddingResponse(session *gameSession, body []byte) error {
	r, ok := decodeCurrentWeddingResponse(body)
	if !ok {
		return s.sendGameUpperFailure(session, currentWeddingResponseOpcode, 1)
	}
	if r.Operation == 7 {
		return s.handleCurrentWeddingDivorceResponse(session, r)
	}
	fail := func(result int32) error {
		return s.sendGameUpperRawClass(session, currentWeddingResponseOpcode, buildCurrentWeddingResponseRecord(r, result, true), 1)
	}
	to, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return fail(-1)
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	inv := s.wedding.invitations[to.character]
	if r.Operation == 2 {
		// The current native state2 dialog does not emit the state4 timeout
		// branch. A server timeout owns closure; a delayed timeout reply must
		// never consume a replacement invitation that is still within time.
		if old := s.wedding.reservationTimeouts[to.character]; old != nil && old.to == to &&
			s.gameplayNow().Before(old.expires.Add(time.Minute)) && r.Answer == 2 &&
			(r.Target == 0 || r.Target == uint32(old.from.character)) && (inv == nil || s.gameplayNow().Before(inv.expires)) {
			return s.sendGameUpperRawClass(session, currentWeddingResponseOpcode, buildCurrentWeddingResponseRecord(r, 0, true), 1)
		}
	}
	// Native 1acec77/1aced31 send Target=0 on refusal/timeout. Resolve those
	// replies only against this bound recipient's sole pending dialog; consent
	// still needs the exact inviter. Keep expired dialogs fenced until this
	// reply drains so a late consent cannot consume a replacement invitation.
	if inv == nil || inv.to != to || r.Operation != inv.request.Operation ||
		(r.Target != uint32(inv.from.character) && (r.Target != 0 || r.Answer == 0)) {
		return fail(-1)
	}
	delete(s.wedding.invitations, to.character)
	if inv.timer != nil {
		inv.timer.Stop()
	}
	r.Target = uint32(inv.from.character)
	r.Kind, r.Duration = inv.request.Kind, inv.request.Duration
	reject := func(err error) error { return s.publishCurrentWeddingConsentFailure(inv, r, err) }
	if !s.gameplayNow().Before(inv.expires) || !s.boundGameSessionCharacterCurrent(inv.from) {
		r.Answer = 2
	}
	var committed *wedding.PairResult
	if r.Answer == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		release, e := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{inv.from, to})
		if e != nil {
			return reject(e)
		}
		if !s.currentWeddingPriestScene(inv.from, to) {
			release()
			return reject(wedding.ErrState)
		}
		if s.validateUpgradeNPCScene(inv.from, 20013) != nil || s.validateUpgradeNPCScene(to, 20013) != nil {
			release()
			return reject(wedding.ErrState)
		}
		owner, e := s.currentWeddingOwner()
		if e != nil {
			release()
			return reject(e)
		}
		req := s.weddingPairRequest(inv.from, to)
		boundCurrent := req.Revalidate
		req.Revalidate = func() bool { return boundCurrent() && s.currentWeddingPriestScene(inv.from, to) }
		var result wedding.PairResult
		if r.Operation == 1 {
			result, e = owner.Engage(ctx, req)
		} else if r.Operation == 2 {
			req.CeremonyRoute, e = s.prepareCurrentWeddingCeremonyRoute(session)
			if e == nil {
				var rules *wedding.GuestRules
				rules, e = s.currentWeddingGuestRules()
				if e == nil {
					result, e = owner.ReserveWithGuests(ctx, req, r.Kind, r.Duration, currentWeddingDay(req.Now), rules)
				}
			}
		} else {
			e = wedding.ErrState
		}
		release()
		if e != nil {
			s.logGameEvent(session, "game-wedding-consent-rejected", "error", e)
			return reject(e)
		}
		committed = &result
	}
	var published error
	if committed != nil && r.Operation == 2 {
		// Current 90 native 1004 refresh clears the congratulations body. Publish
		// the committed token/profile/deadline first; successful 1033/998 then
		// opens the original dialog and fills dstr 72975 last. Even a failed
		// projection must not suppress either recipient's consent response.
		published = s.afterCurrentWeddingReservation(inv.from, to, *committed)
	}
	published = errors.Join(published, s.sendGameUpperRawClass(session, currentWeddingResponseOpcode, buildCurrentWeddingResponseRecord(r, 0, true), 1))
	notice := r
	notice.Target = uint32(to.character)
	if s.boundGameSessionCharacterCurrent(inv.from) {
		published = errors.Join(published, s.sendGameUpperRawClass(inv.from.session, currentWeddingResponseNotice, buildCurrentWeddingResponseRecord(notice, 0, false), 0))
	}
	if committed != nil {
		if r.Operation == 2 {
			return published
		}
		return errors.Join(published, s.publishCurrentWeddingPair(*committed), s.refreshCurrentWeddingQuests(inv.from, to))
	}
	return published
}

func currentWeddingDay(now time.Time) int64 {
	// PVF specifies once per day, without a reset hour. Reuse the server's
	// existing 06:00 UTC+8 service day for both wedding and daily love points.
	return premium.ServiceDayID(now)
}

func (s *Service) sendCurrentWeddingInfoRecord(session *gameSession, c dnfrepo.CharacterRecord) error {
	if session == nil || c.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) || c.AccountID != s.accountIDForSession(session) {
		return wedding.ErrOwnership
	}
	body, err := s.currentWeddingInfoBody(c)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, currentWeddingInfoNotice, body, 0)
}

func (s *Service) currentWeddingInfoBody(c dnfrepo.CharacterRecord) ([]byte, error) {
	state, e := wedding.Read(c)
	if e != nil {
		return nil, e
	}
	ring := byte(255)
	if state.Phase == wedding.Married {
		rules, e := s.currentWeddingRules()
		if e != nil {
			return nil, e
		}
		if state.Ring >= len(rules.Rings) {
			return nil, wedding.ErrState
		}
		ring = byte(state.Ring)
	}
	return buildCurrentWeddingInfo(currentWeddingRelationToken(state), state.Phase, state.Kind, ring, uint32(state.MarriedAt), uint32(state.LovePoints)), nil
}

// Legacy installed records are upgraded at selection before publication. The
// fallback preserves compatibility of older isolated fixtures and snapshots.
func currentWeddingRelationToken(state wedding.State) uint32 {
	if state.Phase == wedding.Single {
		return 0
	}
	if state.RelationToken != 0 {
		return uint32(state.RelationToken)
	}
	return uint32(state.PartnerID)
}

func (s *Service) sendCurrentInitialWeddingState(session *gameSession) error {
	if s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	c, e := s.currentWeddingCharacter(ctx, identity)
	if e != nil {
		return e
	}
	state, err := wedding.Read(c)
	if err != nil {
		return err
	}
	if state.Phase != wedding.Single && (state.RelationToken == 0 || state.Phase == wedding.Reserved || state.Phase == wedding.InCeremony) {
		repos, _ := s.repositoryGroup()
		partner, found, e := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if e != nil {
			return e
		}
		if !found {
			return wedding.ErrState
		}
		owner, e := s.currentWeddingOwner()
		if e != nil {
			return e
		}
		req := wedding.PairRequest{Actor: wedding.Actor{AccountID: c.AccountID, CharacterID: c.CharacterID}, Partner: wedding.Actor{AccountID: partner.AccountID, CharacterID: partner.CharacterID}, Now: s.gameplayNow(), Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) }}
		var result wedding.PairResult
		if state.Phase == wedding.Reserved || state.Phase == wedding.InCeremony {
			// Bootstrap legacy run metadata before the first native 1000. A
			// later run-key allocation must not invalidate an already-published
			// token; this migration never invents a payment or an invitation.
			var rules *wedding.GuestRules
			rules, e = s.currentWeddingGuestRules()
			if e == nil {
				_, e = owner.EnsureGuestWedding(ctx, req, rules)
			}
			if e == nil {
				result.Actor, _, e = owner.Repositories.Character.Load(ctx, c.CharacterID)
			}
		} else {
			result, e = owner.EnsureRelationshipToken(ctx, req)
		}
		if e != nil {
			return e
		}
		c = result.Actor
		state, e = wedding.Read(c)
		if e != nil {
			return e
		}
	}
	if state.Phase == wedding.Married {
		// Selection owns its own asset reservation. Do not acquire that same
		// session gate recursively; the pair transaction locks both rows.
		repos, _ := s.repositoryGroup()
		partner, found, err := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if err != nil {
			return err
		}
		if !found {
			return wedding.ErrState
		}
		owner, err := s.currentWeddingOwner()
		if err != nil {
			return err
		}
		req := wedding.PairRequest{Actor: wedding.Actor{AccountID: c.AccountID, CharacterID: c.CharacterID}, Partner: wedding.Actor{AccountID: partner.AccountID, CharacterID: partner.CharacterID}, Now: s.gameplayNow(), Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) }}
		result, err := owner.DailyLovePoints(ctx, req, currentWeddingDay(req.Now))
		if err != nil {
			return err
		}
		c = result.Actor
	}
	if err := s.sendCurrentWeddingInfoRecord(session, c); err != nil {
		return err
	}
	if err := s.queueCurrentWeddingCeremonyRecovery(session, state); err != nil {
		return err
	}
	if err := s.sendInitialCurrentWeddingGuestLetters(session); err != nil {
		return err
	}
	if err := s.queueCurrentWeddingGuestRecovery(session); err != nil {
		return err
	}
	state, err = wedding.Read(c)
	if err != nil || state.Phase != wedding.Married {
		if err != nil {
			return err
		}
		// A previous selected character may have populated the native room
		// table. Unmarried/engaged characters must keep its contribution zero.
		return s.writeCurrentWeddingRoom(session, wedding.Room{})
	}
	return errors.Join(s.sendCurrentWeddingRoomSnapshot(session, c), s.sendCurrentWeddingCapsuleState(session, c))
}

func weddingGameplayModule() gameplayModuleDefinition {
	legacy := map[uint16]gameplayHandler{}
	upper := map[uint16]gameplayHandler{}
	for op, handler := range map[uint16]func(*Service, *gameSession, []byte) error{currentWeddingRequestOpcode: (*Service).handleCurrentWeddingRequest, currentWeddingResponseOpcode: (*Service).handleCurrentWeddingResponse, currentWeddingCharacterOpcode: (*Service).handleCurrentWeddingCharacter, currentWeddingRingOpcode: (*Service).handleCurrentWeddingRing, currentWeddingEnterCeremonyOpcode: (*Service).handleCurrentWeddingCeremonyEntry, 1034: (*Service).handleCurrentWeddingMoneyGift, 1038: (*Service).handleCurrentWeddingSendLetter, 1045: (*Service).handleCurrentWeddingRoomOpen, 1046: (*Service).handleCurrentWeddingRoomMove} {
		run := handler
		legacy[op] = func(s *Service, g *gameSession, r gameplayRequest) error { return run(s, g, r.Body) }
		upper[op] = defaultClassGameplayHandler("game-wedding-class-rejected", "command_class_mismatch", run)
	}
	legacy[1037] = currentWeddingLovePointItemHandler
	for op, handler := range map[uint16]func(*Service, *gameSession, []byte) error{1040: (*Service).handleCurrentWeddingUpdateGuestComment, 1041: (*Service).handleCurrentWeddingDeleteGuestComment, 1042: (*Service).handleCurrentWeddingLoadGuestComment, 1043: (*Service).handleCurrentWeddingPartnerMoveCheck, 1044: (*Service).handleCurrentWeddingPartnerMove} {
		run := handler
		legacy[op] = func(s *Service, g *gameSession, r gameplayRequest) error { return run(s, g, r.Body) }
		upper[op] = defaultClassGameplayHandler("game-wedding-class-rejected", "command_class_mismatch", run)
	}
	upper[1037] = currentWeddingLovePointItemHandler
	for op := uint16(1047); op <= 1054; op++ {
		legacy[op] = currentWeddingExtensionHandler
		upper[op] = currentWeddingExtensionHandler
	}
	return gameplayModuleDefinition{Name: "wedding", LegacyHandlers: legacy, UpperHandlers: upper}
}
