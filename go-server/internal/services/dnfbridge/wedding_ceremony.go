package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type currentWeddingCeremony struct {
	first, second  wedding.Actor
	at             int64
	route          wedding.CeremonyRoute
	timer          *time.Timer
	lastFailure    string
	lastDiagnostic string
}

func townMapPathLooksLikeWeddingChurch(path string) bool {
	p := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"))
	p = strings.TrimPrefix(p, "map/")
	return p == "church/church.map"
}

func (s *Service) prepareCurrentWeddingCeremonyRoute(session *gameSession) (*wedding.CeremonyRoute, error) {
	if session == nil || isPVPChannel(session.channel) || isCurrentFiendWarChannel(session) {
		return nil, wedding.ErrState
	}
	channel := currentTownPresenceChannelID(session)
	area, found := s.townArea(39, 8)
	if channel <= 0 || channel > 255 || !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) {
		return nil, wedding.ErrUnavailable
	}
	return &wedding.CeremonyRoute{Channel: byte(channel), Area: 8}, nil
}

func currentWeddingChurchMoveAllowed(c r.CharacterRecord, town, area byte) bool {
	state, err := wedding.Read(c)
	if err != nil || town != 39 || int64(area) != state.CeremonyArea {
		return false
	}
	return state.Phase == wedding.Reserved || state.Phase == wedding.InCeremony ||
		(state.Phase == wedding.Married && c.Stats["town_id"] == 39 && c.Stats["area_id"] == state.CeremonyArea)
}

func (s *Service) currentWeddingChurchInstance(ctx context.Context, session *gameSession, c r.CharacterRecord) (uint32, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || c.CharacterID != strconv.Itoa(int(identity.character)) || c.AccountID != s.accountIDForSession(session) {
		return 0, wedding.ErrOwnership
	}
	if instance, handled, e := s.currentWeddingGuestChurchInstance(ctx, session, c); handled && e == nil {
		return instance, nil
	}
	state, err := wedding.Read(c)
	if err != nil || !currentWeddingChurchMoveAllowed(c, 39, byte(state.CeremonyArea)) || currentTownPresenceChannelID(session) != int(state.CeremonyChannel) {
		if instance, handled, e := s.currentWeddingGuestChurchInstance(ctx, session, c); handled {
			return instance, e
		}
		return 0, wedding.ErrState
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return 0, wedding.ErrUnavailable
	}
	partner, found, err := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, wedding.ErrState
	}
	if _, _, err = wedding.ValidatePair(c, partner); err != nil || !s.boundGameSessionCharacterCurrent(identity) {
		return 0, wedding.ErrState
	}
	return weddingPairInstance(identity.character, uint16(state.PartnerID)), nil
}

func (s *Service) currentWeddingChurchPresence(a, b boundGameSessionCharacter, route wedding.CeremonyRoute) bool {
	if !route.Valid() || s.onlinePlayers == nil || !s.boundGameSessionCharacterCurrent(a) || !s.boundGameSessionCharacterCurrent(b) {
		return false
	}
	instance := weddingPairInstance(a.character, b.character)
	for _, identity := range []boundGameSessionCharacter{a, b} {
		p, found := s.onlinePlayers.PlayerForCharacter(identity.character)
		if !found || p.Session != identity.session || p.TownID != 39 || p.AreaID != route.Area || p.ChannelID != int(route.Channel) || p.Instance != instance {
			return false
		}
		// Church is an ordinary native town op24 transition. Like the 90
		// reference's weddingCeremonyPeers, admission uses the committed town
		// scene, not dungeon op37/op30 gates (which op24 clears and never arms).
		if !s.currentWeddingChurchTownSceneCurrent(identity, p, route) {
			return false
		}
	}
	area, found := s.townArea(39, int64(route.Area))
	return found && townMapPathLooksLikeWeddingChurch(area.MapPath)
}

func currentWeddingCeremonyKey(a, b r.CharacterRecord) (string, error) {
	return r.WeddingPairScope([]string{a.CharacterID, b.CharacterID})
}

// Caller holds wedding.mu. The timer captures a durable reservation version,
// not the current socket: reconnects are resolved again when it fires.
func (s *Service) scheduleCurrentWeddingCeremonyLocked(result wedding.PairResult) error {
	if s.wedding.ceremoniesStopped {
		return wedding.ErrUnavailable
	}
	state, _, err := wedding.ValidatePair(result.Actor, result.Partner)
	if err != nil || (state.Phase != wedding.Reserved && state.Phase != wedding.InCeremony) {
		return wedding.ErrState
	}
	key, err := currentWeddingCeremonyKey(result.Actor, result.Partner)
	if err != nil {
		return err
	}
	nextAt := state.CeremonyAt
	if state.Phase == wedding.InCeremony && state.CeremonyReadyAt != 0 {
		rules, err := s.currentWeddingRules()
		if err != nil || rules.CeremonySeconds <= 0 {
			return errors.Join(err, wedding.ErrUnavailable)
		}
		nextAt = state.CeremonyReadyAt + rules.CeremonySeconds
	}
	if s.wedding.ceremonies == nil {
		s.wedding.ceremonies = make(map[string]*currentWeddingCeremony)
	}
	if old := s.wedding.ceremonies[key]; old != nil {
		if old.at == state.CeremonyAt && old.timer != nil {
			if s.gameplayNow().Unix() < nextAt {
				return nil
			}
			old.timer.Stop()
			s.armCurrentWeddingCeremonyLocked(key, old, 0)
			return nil
		}
		if old.timer != nil {
			old.timer.Stop()
		}
	}
	c := &currentWeddingCeremony{first: wedding.Actor{AccountID: result.Actor.AccountID, CharacterID: result.Actor.CharacterID}, second: wedding.Actor{AccountID: result.Partner.AccountID, CharacterID: result.Partner.CharacterID}, at: state.CeremonyAt, route: wedding.CeremonyRoute{Channel: byte(state.CeremonyChannel), Area: byte(state.CeremonyArea)}}
	s.wedding.ceremonies[key] = c
	delay := time.Unix(nextAt, 0).Sub(s.gameplayNow())
	if delay < 0 {
		delay = 0
	}
	c.timer = time.AfterFunc(delay, func() { s.runCurrentWeddingCeremony(key, c) })
	return nil
}

func (s *Service) writeCurrentWeddingCeremony(session *gameSession, a, b r.CharacterRecord) error {
	state, _, err := wedding.ValidatePair(a, b)
	if err != nil || (state.Phase != wedding.Reserved && state.Phase != wedding.InCeremony) {
		return wedding.ErrState
	}
	if a.AccountID != s.accountIDForSession(session) || a.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) {
		return wedding.ErrOwnership
	}
	route := wedding.CeremonyRoute{Channel: byte(state.CeremonyChannel), Area: byte(state.CeremonyArea)}
	groom, bride, err := currentWeddingCeremonyNames(a, b)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	letters, err := s.currentWeddingGuestRemaining(ctx, a, b)
	if err != nil {
		return err
	}
	body := buildCurrentWeddingCeremony(currentWeddingRelationToken(state), route, uint32(state.CeremonyAt), state.Kind, groom, bride, letters)
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return wedding.ErrOwnership
	}
	return s.withGameWire(session, func() error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return wedding.ErrOwnership
		}
		// Native 1004 clears a visible congratulations body even when its
		// reservation is identical. The global ceremony model survives normal
		// town moves; only a new binding or changed reservation needs replay.
		// Quota is maintained by CMD1038, not by repeated 1004 notifications.
		stamp := currentWeddingCeremonyProjection{owner: identity, record: string(body[:len(body)-2])}
		if session.weddingCeremonyProjection == stamp {
			return nil
		}
		if err := s.sendGameUpperRawClassCodecLocked(session, currentWeddingCeremonyNotice, body, 0, true); err != nil {
			return err
		}
		session.weddingCeremonyProjection = stamp
		return nil
	})
}

// Protected by the owning session's wireMu, like the packet sequence.
type currentWeddingCeremonyProjection struct {
	owner  boundGameSessionCharacter
	record string
}

// Native movie: 1ace3bf copies CeremonyInfo at object+1704. Its groom lookup
// (1accf40(true), vow72771 at1acd3c4) uses+1734=record+30=raw first name;
// bride lookup(false, vow72773 at1acd4a5) uses+1714=record+10=raw second name.
func currentWeddingCeremonyNames(a, b r.CharacterRecord) ([]byte, []byte, error) {
	female := func(job string) (bool, bool) {
		switch job {
		case "0", "2", "4", "7", "8", "9", "13", "15":
			return false, true
		case "1", "3", "5", "6", "10", "11", "12", "14":
			return true, true
		}
		return false, false
	}
	af, ak := female(a.Job)
	bf, bk := female(b.Job)
	if !ak || !bk || af == bf {
		return nil, nil, wedding.ErrEligibility
	}
	if af {
		a, b = b, a
	}
	return rosterNameBytes(a.Name), rosterNameBytes(b.Name), nil
}

func currentWeddingCeremonyPosition(rules *wedding.Rules, c r.CharacterRecord) (wedding.CeremonyPosition, error) {
	if rules == nil {
		return wedding.CeremonyPosition{}, wedding.ErrUnavailable
	}
	switch c.Job {
	case "0", "2", "4", "7", "8", "9", "13", "15":
		return rules.ForceMove[0], nil
	case "1", "3", "5", "6", "10", "11", "12", "14":
		return rules.ForceMove[1], nil
	}
	return wedding.CeremonyPosition{}, wedding.ErrEligibility
}

// The consent owner calls this while holding wedding.mu, after Reserve's one
// durable commit. A failed socket cannot prevent the reservation timer.
func (s *Service) publishCurrentWeddingCeremonyReservationLocked(a, b boundGameSessionCharacter, result wedding.PairResult) error {
	err := s.scheduleCurrentWeddingCeremonyLocked(result)
	if err != nil {
		return err
	}
	if s.boundGameSessionCharacterCurrent(a) {
		err = errors.Join(err, s.writeCurrentWeddingCeremony(a.session, result.Actor, result.Partner))
	}
	if s.boundGameSessionCharacterCurrent(b) {
		err = errors.Join(err, s.writeCurrentWeddingCeremony(b.session, result.Partner, result.Actor))
	}
	return err
}

func (s *Service) currentWeddingGiftGrant(ctx context.Context, owner wedding.Owner, kind byte, now time.Time) (wedding.AssetGrant, error) {
	gift, known := owner.Rules.CeremonyGifts[kind]
	if !known || gift.ItemID <= 0 || gift.ItemID > 0xffffffff || gift.Count <= 0 || gift.Count > 0xffffffff {
		return nil, wedding.ErrUnavailable
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	definition, err := catalog.ResolveItem(uint32(gift.ItemID))
	if err != nil {
		return nil, err
	}
	definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
	if err != nil {
		return nil, err
	}
	return func(inventory *r.InventoryRecord, id, count int64, at time.Time) error {
		if id != gift.ItemID || count != gift.Count || !at.Equal(now) {
			return wedding.ErrItem
		}
		if err := r.BindInventoryContainerState(ctx, owner.Repositories.Settings, inventory); err != nil {
			return err
		}
		_, err := grantCurrentCeraShopProductAt(inventory, definition, uint32(count), at)
		return err
	}, nil
}

func (s *Service) runCurrentWeddingCeremony(key string, ceremony *currentWeddingCeremony) {
	// State notifications share the wedding mutation order. Releasing this
	// lock between commit and publication could overwrite a later ring upgrade
	// on the client with this ceremony's older ring-zero snapshot.
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	expired := false
	result, a, b, err := func() (wedding.PairResult, boundGameSessionCharacter, boundGameSessionCharacter, error) {
		if s.wedding.ceremoniesStopped || s.wedding.ceremonies[key] != ceremony {
			return wedding.PairResult{}, boundGameSessionCharacter{}, boundGameSessionCharacter{}, nil
		}
		ceremony.timer = nil
		firstID, e1 := wedding.NumericID(ceremony.first.CharacterID)
		secondID, e2 := wedding.NumericID(ceremony.second.CharacterID)
		if e1 != nil || e2 != nil {
			return wedding.PairResult{}, boundGameSessionCharacter{}, boundGameSessionCharacter{}, wedding.ErrState
		}
		a, foundA := s.onlineGameSessionCharacterSnapshot(uint16(firstID))
		b, foundB := s.onlineGameSessionCharacterSnapshot(uint16(secondID))
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		owner, err := s.currentWeddingOwner()
		if err != nil {
			return wedding.PairResult{}, a, b, err
		}
		actor, found, err := owner.Repositories.Character.Load(ctx, ceremony.first.CharacterID)
		if err != nil || !found || actor.AccountID != ceremony.first.AccountID {
			s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, "actor_unavailable", "deferred", false, false)
			return wedding.PairResult{}, a, b, err
		}
		state, err := wedding.Read(actor)
		if err != nil || (state.Phase != wedding.Reserved && state.Phase != wedding.InCeremony) || state.CeremonyAt != ceremony.at || state.CeremonyChannel != int64(ceremony.route.Channel) || state.CeremonyArea != int64(ceremony.route.Area) {
			delete(s.wedding.ceremonies, key)
			return wedding.PairResult{}, a, b, nil
		}
		partner, found, err := owner.Repositories.Character.Load(ctx, ceremony.second.CharacterID)
		if err != nil || !found || partner.AccountID != ceremony.second.AccountID {
			s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, "partner_unavailable", "deferred", false, false)
			return wedding.PairResult{}, a, b, err
		}
		if _, _, err = wedding.ValidatePair(actor, partner); err != nil {
			delete(s.wedding.ceremonies, key)
			return wedding.PairResult{}, a, b, err
		}
		if wedding.ReservationExpired(state, s.gameplayNow().Unix()) {
			result, err := s.expireCurrentWeddingReservationLocked(ctx, owner, ceremony, wedding.PairResult{Actor: actor, Partner: partner})
			if err == nil {
				delete(s.wedding.ceremonies, key)
				expired = true
			}
			return result, a, b, err
		}
		now := s.gameplayNow()
		if state.Phase == wedding.InCeremony && state.CeremonyReadyAt != 0 {
			if owner.Rules.CeremonySeconds <= 0 {
				return wedding.PairResult{}, a, b, wedding.ErrUnavailable
			}
			finishAt := state.CeremonyReadyAt + owner.Rules.CeremonySeconds
			if now.Unix() < finishAt {
				s.armCurrentWeddingCeremonyLocked(key, ceremony, time.Unix(finishAt, 0).Sub(now))
				return wedding.PairResult{}, a, b, nil
			}
			var online []boundGameSessionCharacter
			if foundA {
				online = append(online, a)
			}
			if foundB {
				online = append(online, b)
			}
			release, err := s.reserveCurrentGuildParticipants(ctx, online)
			if err != nil {
				return wedding.PairResult{}, a, b, err
			}
			defer release()
			req := wedding.PairRequest{Actor: ceremony.first, Partner: ceremony.second, Now: now, Revalidate: func() bool {
				for _, identity := range online {
					if !s.boundGameSessionCharacterCurrent(identity) {
						return false
					}
				}
				return true
			}}
			deliver, err := s.currentWeddingCeremonyDelivery(owner, state.Kind, now)
			if err != nil {
				return wedding.PairResult{}, a, b, err
			}
			result, err := owner.FinishAdmittedCeremonyDelivered(ctx, req, ceremony.at, deliver)
			if err == nil {
				delete(s.wedding.ceremonies, key)
			}
			return result, a, b, err
		}
		if now.Unix() < ceremony.at {
			s.armCurrentWeddingCeremonyLocked(key, ceremony, time.Unix(ceremony.at, 0).Sub(now))
			s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, "not_due", "waiting", false, false)
			return wedding.PairResult{}, a, b, nil
		}
		if !foundA || !foundB || !s.currentWeddingChurchPresence(a, b, ceremony.route) {
			if state.CeremonyReadyAt == 0 {
				s.armCurrentWeddingCeremonyLocked(key, ceremony, time.Unix(ceremony.at+wedding.CeremonyAdmissionGraceSeconds+1, 0).Sub(now))
			}
			s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, s.currentWeddingChurchPresenceFailure(a, b, ceremony.route), "waiting", false, false)
			return wedding.PairResult{}, a, b, nil
		}
		req := s.weddingPairRequest(a, b)
		release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{a, b})
		if err != nil {
			s.armCurrentWeddingCeremonyLocked(key, ceremony, 5*time.Second)
			return wedding.PairResult{}, a, b, err
		}
		defer release()
		revalidate := req.Revalidate
		req.Revalidate = func() bool { return revalidate() && s.currentWeddingChurchPresence(a, b, ceremony.route) }
		if state.CeremonyReadyAt == 0 {
			if _, err = owner.MarkCeremonyReady(ctx, req, ceremony.at); err != nil {
				s.armCurrentWeddingCeremonyLocked(key, ceremony, 5*time.Second)
				return wedding.PairResult{}, a, b, err
			}
		}
		result, err := owner.BeginAdmittedCeremony(ctx, req, ceremony.at)
		if err != nil {
			if s.currentWeddingChurchPresence(a, b, ceremony.route) {
				s.armCurrentWeddingCeremonyLocked(key, ceremony, 5*time.Second)
			}
			return wedding.PairResult{}, a, b, err
		}
		started, _ := wedding.Read(result.Actor)
		s.armCurrentWeddingCeremonyLocked(key, ceremony, time.Unix(started.CeremonyReadyAt+owner.Rules.CeremonySeconds, 0).Sub(now))
		return result, a, b, nil
	}()
	if err != nil {
		if s.wedding.ceremonies[key] == ceremony && !s.wedding.ceremoniesStopped {
			s.armCurrentWeddingCeremonyLocked(key, ceremony, 5*time.Second)
		}
		s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, currentWeddingCeremonyFailureReason(err), "deferred", false, false)
		if a.session != nil && ceremony.lastFailure != err.Error() {
			s.logGameEvent(a.session, "game-wedding-ceremony-start-deferred", "error", err)
			ceremony.lastFailure = err.Error()
		}
		return
	}
	if result.Actor.CharacterID == "" {
		return
	}
	state, _ := wedding.Read(result.Actor)
	err = s.publishCurrentWeddingPair(result)
	if expired {
		err = errors.Join(err, s.cleanupCurrentWeddingReservationAvatarsLocked(result))
		if debtErr := s.queueCurrentWeddingGuestSettlementLocked(result, ceremony.at); debtErr != nil {
			err = errors.Join(err, debtErr)
			go s.recoverCurrentWeddingRuns()
		}
		reason := "admission_expired"
		if err != nil {
			reason = "expired_projection_deferred"
		}
		s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, reason, "expired", false, false)
		if err != nil && a.session != nil {
			s.logGameEvent(a.session, "game-wedding-expired-projection-deferred", "error", err)
		}
		return
	}
	if state.Phase == wedding.Married {
		if debtErr := s.queueCurrentWeddingGuestSettlementLocked(result, ceremony.at); debtErr != nil {
			err = errors.Join(err, debtErr)
			go s.recoverCurrentWeddingRuns()
		}
		for _, c := range []r.CharacterRecord{result.Actor, result.Partner} {
			id, _ := wedding.NumericID(c.CharacterID)
			if identity, online := s.onlineGameSessionCharacterSnapshot(uint16(id)); online {
				err = errors.Join(err, s.sendCurrentWeddingRoomSnapshot(identity.session, c), s.sendCurrentWeddingCapsuleState(identity.session, c), s.sendMailboxAlarmForSession(identity.session, uint16(id), "wedding_ceremony_gift"))
			}
		}
		s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, "completed", "committed", false, false)
		if err != nil && a.session != nil {
			s.logGameEvent(a.session, "game-wedding-ceremony-completed-projection-deferred", "error", err)
		}
		return
	}
	groom, bride, namesErr := currentWeddingCeremonyNames(result.Actor, result.Partner)
	err = errors.Join(err, namesErr)
	var beginSent [2]bool
	for index, pair := range []struct {
		identity      boundGameSessionCharacter
		self, partner r.CharacterRecord
	}{{a, result.Actor, result.Partner}, {b, result.Partner, result.Actor}} {
		if namesErr != nil || !s.boundGameSessionCharacterCurrent(pair.identity) {
			continue
		}
		x, _ := wedding.Read(pair.self)
		sendErr := s.sendCurrentWeddingMovieBeginLocked(pair.identity, currentWeddingRelationToken(x), ceremony.route, state.Kind, groom, bride)
		beginSent[index] = sendErr == nil
		err = errors.Join(err, sendErr)
	}
	err = errors.Join(err, s.publishCurrentWeddingGuestBegin(result))
	reason := "committed"
	if err != nil {
		reason = "committed_projection_deferred"
	}
	s.logCurrentWeddingCeremonyDiagnosticLocked(ceremony, a, b, reason, "committed", beginSent[0], beginSent[1])
	if err != nil {
		s.logGameEvent(a.session, "game-wedding-ceremony-committed-projection-deferred", "error", err)
	}
}

func (s *Service) armCurrentWeddingCeremonyLocked(key string, ceremony *currentWeddingCeremony, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	if !s.wedding.ceremoniesStopped && s.wedding.ceremonies[key] == ceremony {
		if ceremony.timer != nil {
			ceremony.timer.Stop()
		}
		ceremony.timer = time.AfterFunc(delay, func() { s.runCurrentWeddingCeremony(key, ceremony) })
	}
}

// Queue after the current scene event. Town and login owners may still hold a
// scene or asset lock, so they must not wait for wedding.mu from inside it.
func (s *Service) queueCurrentWeddingCeremonyRecovery(session *gameSession, initial ...wedding.State) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || session.events == nil || session.events.loop == nil {
		return nil
	}
	var login *wedding.State
	if len(initial) != 0 {
		// Capture admission at initial projection, before the deferred scene
		// callback. Readiness earned later in this login cannot arm recovery.
		snapshot := initial[0]
		login = &snapshot
	}
	return s.tryPostGameSessionCharacterEvent(session, "game-wedding-ceremony-recovery", identity.character, identity.generation, func() error {
		return s.runCurrentWeddingCeremonyRecovery(identity, login)
	})
}

type currentWeddingCeremonyLoginRecovery struct {
	owner       boundGameSessionCharacter
	pending     bool
	reservation wedding.State
}

func (s *Service) runCurrentWeddingCeremonyRecovery(identity boundGameSessionCharacter, initial *wedding.State) error {
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	pair, active, err := func() (wedding.PairResult, bool, error) {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		if s.wedding.ceremoniesStopped {
			return wedding.PairResult{}, false, wedding.ErrUnavailable
		}
		if guest, err := s.replayCurrentWeddingChurchTownSceneLocked(ctx, identity); guest || err != nil {
			return wedding.PairResult{}, false, err
		}
		c, err := s.currentWeddingCharacter(ctx, identity)
		if err != nil {
			return wedding.PairResult{}, false, err
		}
		state, err := wedding.Read(c)
		if err != nil {
			return wedding.PairResult{}, false, err
		}
		login, recorded := s.wedding.ceremonyLogins[identity.character]
		if initial != nil && (!recorded || login.owner != identity) {
			if s.wedding.ceremonyLogins == nil {
				s.wedding.ceremonyLogins = make(map[uint16]currentWeddingCeremonyLoginRecovery)
			}
			login = currentWeddingCeremonyLoginRecovery{owner: identity, pending: initial.CeremonyReadyAt != 0 && initial.Phase == wedding.Reserved, reservation: *initial}
			s.wedding.ceremonyLogins[identity.character] = login
		}
		if login.owner == identity && login.pending {
			v := login.reservation
			if v.PartnerID != state.PartnerID || v.Kind != state.Kind || v.CeremonyAt != state.CeremonyAt || v.CeremonyReadyAt != state.CeremonyReadyAt || v.CeremonyArea != state.CeremonyArea || v.CeremonyChannel != state.CeremonyChannel {
				login.pending = false
				s.wedding.ceremonyLogins[identity.character] = login
			}
		}
		if state.Phase != wedding.Reserved && state.Phase != wedding.InCeremony {
			if login.owner == identity && login.pending {
				login.pending = false
				s.wedding.ceremonyLogins[identity.character] = login
			}
			return wedding.PairResult{}, false, nil
		}
		owner, err := s.currentWeddingOwner()
		if err != nil {
			return wedding.PairResult{}, false, err
		}
		partner, found, err := owner.Repositories.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if err != nil || !found {
			return wedding.PairResult{}, false, wedding.ErrState
		}
		pair := wedding.PairResult{Actor: c, Partner: partner}
		if login.owner == identity && login.pending {
			var consumed bool
			pair, consumed, err = s.recoverCurrentWeddingReadyChurchLocked(ctx, identity, owner, pair)
			if err != nil {
				return wedding.PairResult{}, false, err
			}
			if consumed {
				// Keep the consumed record: repeated initialization in this binding
				// must not rearm a teleport after the player deliberately leaves.
				login.pending = false
				s.wedding.ceremonyLogins[identity.character] = login
			}
		}
		return pair, true, s.scheduleCurrentWeddingCeremonyLocked(pair)
	}()
	if err != nil || !active {
		return err
	}
	return s.writeCurrentWeddingCeremony(identity.session, pair.Actor, pair.Partner)
}

func (s *Service) stopCurrentWeddingCeremonyTimers() {
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	s.wedding.ceremoniesStopped = true
	for key, inv := range s.wedding.invitations {
		if inv.timer != nil {
			inv.timer.Stop()
		}
		delete(s.wedding.invitations, key)
	}
	s.wedding.reservationTimeouts = nil
	s.wedding.ceremonyLogins = nil
	s.wedding.churchScenes = nil
	for id, stamp := range s.wedding.avatarSetups {
		if stamp.timer != nil {
			stamp.timer.Stop()
		}
		delete(s.wedding.avatarSetups, id)
	}
	if s.wedding.recoveryTimer != nil {
		s.wedding.recoveryTimer.Stop()
		s.wedding.recoveryTimer = nil
	}
	for key, job := range s.wedding.guestSettlements {
		if job.timer != nil {
			job.timer.Stop()
		}
		delete(s.wedding.guestSettlements, key)
	}
	for id, movie := range s.wedding.movieEnds {
		if movie.timer != nil {
			movie.timer.Stop()
		}
		delete(s.wedding.movieEnds, id)
	}
	for key, ceremony := range s.wedding.ceremonies {
		if ceremony.timer != nil {
			ceremony.timer.Stop()
		}
		delete(s.wedding.ceremonies, key)
	}
}

func (s *Service) startCurrentWeddingCeremonyTimers() {
	s.wedding.mu.Lock()
	s.wedding.ceremoniesStopped = false
	s.wedding.mu.Unlock()
	if s.options.currentClientVersion == "official-24.9" {
		go s.recoverCurrentWeddingRuns()
	}
}

func (s *Service) handleCurrentWeddingCeremonyEntry(session *gameSession, body []byte) error {
	fail := func() error {
		return s.sendGameUpperRawClass(session, currentWeddingEnterCeremonyOpcode, weddingResultACK(-10), 1)
	}
	q, valid := decodeCurrentWeddingCeremonyEntry(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if valid && q.Guest {
		return s.handleCurrentWeddingGuestCeremonyEntry(session, body)
	}
	if !valid || !bound || s.onlinePlayers == nil || !s.currentWeddingCeremonyEntryReady(actor) {
		return fail()
	}
	var result wedding.PairResult
	var location guild.TeleportLocation
	err := func() error {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		req, actors, err := s.currentWeddingPartnerRequest(ctx, actor)
		if err != nil {
			return err
		}
		c, err := s.currentWeddingCharacter(ctx, actor)
		if err != nil {
			return err
		}
		state, err := wedding.Read(c)
		if err != nil || currentWeddingRelationToken(state) != q.Partner || uint32(state.CeremonyArea) != q.Area || currentTownPresenceChannelID(session) != int(state.CeremonyChannel) {
			return wedding.ErrState
		}
		area, found := s.townArea(39, state.CeremonyArea)
		if !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) {
			return wedding.ErrUnavailable
		}
		owner, err := s.currentWeddingOwner()
		if err != nil {
			return err
		}
		position, err := currentWeddingCeremonyPosition(owner.Rules, c)
		if err != nil {
			return err
		}
		release, err := s.reserveCurrentGuildParticipants(ctx, actors)
		if err != nil {
			return err
		}
		defer release()
		originalRevalidate := req.Revalidate
		req.Revalidate = func() bool { return originalRevalidate() && s.currentWeddingCeremonyEntryPresence(actor) }
		result, err = owner.EnterChurch(ctx, req, state.CeremonyAt, position)
		if err != nil {
			return err
		}
		location = guild.CharacterTeleportLocation(result.Actor)
		return s.scheduleCurrentWeddingCeremonyLocked(result)
	}()
	if err != nil {
		s.logGameEvent(session, "game-wedding-enter-church-rejected", "error", err)
		return fail()
	}
	move := buildCurrentSoloTeleportMoveBody(currentSoloTeleportRequest{TownID: byte(location.Town), AreaID: byte(location.Area), PositionX: int16(location.X), PositionY: int16(location.Y), Direction: byte(location.Direction)}, uint16(location.Town))
	if err = s.handleTownSetUserAreaWithCommit(session, move, false, &location); err != nil {
		return err
	}
	p, found := s.onlinePlayers.PlayerForCharacter(actor.character)
	state, _ := wedding.Read(result.Actor)
	if !found || p.Session != session || p.TownID != 39 || int64(p.AreaID) != state.CeremonyArea || p.Instance != weddingPairInstance(actor.character, uint16(state.PartnerID)) || !s.boundGameSessionCharacterCurrent(actor) {
		return fail()
	}
	// The native success reader closes its letter window only after the client
	// is in a real Church area, so transport must precede this ACK.
	if err = s.sendGameUpperRawClass(session, currentWeddingEnterCeremonyOpcode, weddingResultACK(0), 1); err != nil {
		return err
	}
	if err = s.queueCurrentWeddingCeremonyRecovery(session); err != nil {
		s.logGameEvent(session, "game-wedding-ceremony-recovery-deferred", "error", err)
	}
	return nil
}

func (s *Service) currentWeddingCeremonyEntryPresence(actor boundGameSessionCharacter) bool {
	if s.onlinePlayers == nil || !s.boundGameSessionCharacterCurrent(actor) {
		return false
	}
	player, found := s.onlinePlayers.PlayerForCharacter(actor.character)
	if !found || player.Session != actor.session || player.ChannelID != currentTownPresenceChannelID(actor.session) {
		return false
	}
	if !actor.session.dungeon.mu.TryLock() {
		return false
	}
	inDungeon := actor.session.dungeon.runtime != nil
	actor.session.dungeon.mu.Unlock()
	return !inDungeon
}

// Only the preflight reads town readiness. The transaction revalidator uses
// immutable presence plus a nonblocking dungeon check, never the town lock.
func (s *Service) currentWeddingCeremonyEntryReady(actor boundGameSessionCharacter) bool {
	if !s.currentWeddingCeremonyEntryPresence(actor) || !actor.session.townMu.TryLock() {
		return false
	}
	ready := actor.session.townSceneReadyCharacterID == actor.character
	actor.session.townMu.Unlock()
	return ready
}
