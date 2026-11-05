package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentWeddingGuestBeginStamp struct {
	Owner    boundGameSessionCharacter
	Presence uint64
	Key      uint32
}

func (s *Service) currentWeddingGuestChurchInstance(ctx context.Context, session *gameSession, c r.CharacterRecord) (uint32, bool, error) {
	if c.Stats["wedding_v1_guest_key"] == 0 {
		return 0, false, nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || c.CharacterID != strconv.Itoa(int(identity.character)) || c.AccountID != s.accountIDForSession(session) {
		return 0, true, wedding.ErrOwnership
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return 0, true, err
	}
	ledger, _, err := owner.GuestChurchProof(ctx, c, s.gameplayNow())
	if err != nil || currentTownPresenceChannelID(session) != int(ledger.Channel) {
		return 0, true, wedding.ErrState
	}
	a, e1 := wedding.NumericID(ledger.Hosts[0])
	b, e2 := wedding.NumericID(ledger.Hosts[1])
	if e1 != nil || e2 != nil || !s.boundGameSessionCharacterCurrent(identity) {
		return 0, true, wedding.ErrOwnership
	}
	return weddingPairInstance(uint16(a), uint16(b)), true, nil
}

func (s *Service) currentWeddingGuestLoadingProof(ctx context.Context, session *gameSession) (wedding.GuestLedger, bool, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return wedding.GuestLedger{}, false, nil
	}
	c, err := s.currentWeddingCharacter(ctx, identity)
	if err != nil {
		return wedding.GuestLedger{}, false, err
	}
	if c.Stats["wedding_v1_guest_key"] == 0 {
		return wedding.GuestLedger{}, false, nil
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return wedding.GuestLedger{}, true, err
	}
	ledger, _, err := owner.GuestChurchProof(ctx, c, s.gameplayNow())
	if err != nil {
		return ledger, true, err
	}
	instance, _, err := s.currentWeddingGuestChurchInstance(ctx, session, c)
	if err != nil {
		return ledger, true, err
	}
	p, found := s.onlinePlayers.PlayerForCharacter(identity.character)
	if !found || p.Session != session || p.AccountID != c.AccountID || p.ChannelID != int(ledger.Channel) || p.TownID != 39 || p.AreaID != ledger.Area || p.Instance != instance || !s.boundGameSessionCharacterCurrent(identity) {
		return ledger, true, wedding.ErrState
	}
	return ledger, true, nil
}

func (s *Service) handleCurrentWeddingGuestCeremonyEntry(session *gameSession, body []byte) error {
	q, valid := decodeCurrentWeddingCeremonyEntry(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	s.logInfo("dnfbridge wedding guest entry request", "event", "game-wedding-guest-entry-received", "char_id", actor.character, "generation", actor.generation, "body_len", len(body), "decoded", valid, "bound", bound, "wedding_key", q.Partner, "area", q.Area, "guest", q.Guest)
	fail := func(reason string, cause error) error {
		sent := s.sendGameUpperRawClass(session, 1039, weddingResultACK(-10), 1)
		s.logInfo("dnfbridge wedding guest entry rejected", "event", "game-wedding-guest-entry-rejected", "char_id", actor.character, "wedding_key", q.Partner, "reason", reason, "error", cause, "notification_sent", sent == nil)
		return sent
	}
	if !valid || !q.Guest || !bound || !s.currentWeddingCeremonyEntryReady(actor) {
		return fail("request_or_scene_ready", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return fail("owner", err)
	}
	ref, err := owner.ResolveGuestWedding(ctx, q.Partner)
	if err != nil {
		return fail("wedding_reference", err)
	}
	ledger, found, err := owner.ReadGuestLedger(ctx, ref.Hosts[:], ref.At)
	if err != nil || !found || uint32(ledger.Area) != q.Area || currentTownPresenceChannelID(session) != int(ledger.Channel) {
		return fail("ledger_or_channel", err)
	}
	area, found := s.townArea(39, int64(ledger.Area))
	if !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) {
		return fail("church_area", nil)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail("church_map_source", err)
	}
	church, err := worldmap.LoadTownPlacementMap(catalog.source, area.MapPath)
	if err != nil {
		return fail("church_map", err)
	}
	position, err := currentWeddingGuestLanding(church)
	if err != nil {
		return fail("church_guest_landing", err)
	}
	s.wedding.mu.Lock()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{actor})
	if err != nil {
		s.wedding.mu.Unlock()
		return fail("participant_reservation", err)
	}
	result, err := owner.EnterGuestChurch(ctx, wedding.Actor{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(actor.character))}, q.Partner, s.gameplayNow(), position, func() bool {
		return s.boundGameSessionCharacterCurrent(actor) && s.currentWeddingCeremonyEntryPresence(actor) && currentTownPresenceChannelID(session) == int(ledger.Channel)
	})
	release()
	s.wedding.mu.Unlock()
	if err != nil {
		return fail("entry_transaction", err)
	}
	location := guild.CharacterTeleportLocation(result.Guest)
	move := buildCurrentSoloTeleportMoveBody(currentSoloTeleportRequest{TownID: 39, AreaID: result.Ledger.Area, PositionX: int16(location.X), PositionY: int16(location.Y), Direction: byte(location.Direction)}, 39)
	if err := s.handleTownSetUserAreaWithCommit(session, move, false, &location); err != nil {
		s.logInfo("dnfbridge wedding guest movement failed", "event", "game-wedding-guest-entry-move-failed", "char_id", actor.character, "wedding_key", q.Partner, "error", err)
		return err
	}
	instance, handled, err := s.currentWeddingGuestChurchInstance(ctx, session, result.Guest)
	p, present := s.onlinePlayers.PlayerForCharacter(actor.character)
	if err != nil || !handled || !present || p.Session != session || p.TownID != 39 || p.AreaID != ledger.Area || p.Instance != instance {
		return fail("entry_readback", err)
	}
	if err := s.sendGameUpperRawClass(session, 1039, weddingResultACK(0), 1); err != nil {
		return err
	}
	s.logInfo("dnfbridge wedding guest entry committed", "event", "game-wedding-guest-entry-committed", "char_id", actor.character, "wedding_key", q.Partner, "area", ledger.Area, "channel", ledger.Channel, "position_x", position.X, "position_y", position.Y)
	return s.sendCurrentWeddingGuestLetter(session, result.Ledger, result.Hosts, result.Guest.CharacterID)
}

// Matches the user's fixed 86 WeddingSceneCatalog.GuestLanding: the centre of
// the first PVF movable rectangle. ForceMove is reserved for the two hosts.
func currentWeddingGuestLanding(church worldmap.Map) (wedding.CeremonyPosition, error) {
	r := church.VirtualMovableArea
	if len(r) < 4 || len(r)%4 != 0 || r[0] < 0 || r[1] < 0 || r[2] <= 0 || r[3] <= 0 || r[2] > 65535 || r[3] > 65535 {
		return wedding.CeremonyPosition{}, wedding.ErrUnavailable
	}
	x, y := r[0]+r[2]/2, r[1]+r[3]/2
	if x < 0 || x > 32767 || y < 0 || y > 32767 {
		return wedding.CeremonyPosition{}, wedding.ErrUnavailable
	}
	return wedding.CeremonyPosition{X: int16(x), Y: int16(y)}, nil
}

func (s *Service) sendCurrentWeddingGuestBeginForSession(session *gameSession) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || s.onlinePlayers == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return err
	}
	c, err := s.currentWeddingCharacter(ctx, identity)
	if err != nil {
		return err
	}
	if c.Stats["wedding_v1_guest_key"] == 0 {
		return nil
	}
	ledger, hosts, err := owner.GuestChurchProof(ctx, c, s.gameplayNow())
	if err != nil {
		return err
	}
	state, _, err := wedding.ValidatePair(hosts[0], hosts[1])
	if err != nil || state.Phase != wedding.InCeremony {
		return err
	}
	p, present := s.onlinePlayers.PlayerForCharacter(identity.character)
	a, _ := wedding.NumericID(ledger.Hosts[0])
	b, _ := wedding.NumericID(ledger.Hosts[1])
	if !present || p.Session != session || p.ChannelID != int(ledger.Channel) || p.TownID != 39 || p.AreaID != ledger.Area || p.Instance != weddingPairInstance(uint16(a), uint16(b)) || !s.currentWeddingChurchTownSceneCurrent(identity, p, wedding.CeremonyRoute{Channel: ledger.Channel, Area: ledger.Area}) {
		return nil
	}
	stamp := currentWeddingGuestBeginStamp{Owner: identity, Presence: p.presenceGeneration, Key: ledger.Key}
	// Callers serialize wedding publication under wedding.mu, just as host
	// 1007 publication; the full scene tail always calls this after unlocking.
	if s.wedding.guestBegins[identity.character] == stamp {
		return nil
	}
	first, second, err := currentWeddingCeremonyNames(hosts[0], hosts[1])
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	if err := s.sendCurrentWeddingMovieBeginLocked(identity, ledger.Key, wedding.CeremonyRoute{Channel: ledger.Channel, Area: ledger.Area}, ledger.Kind, first, second); err != nil {
		return err
	}
	if s.wedding.guestBegins == nil {
		s.wedding.guestBegins = map[uint16]currentWeddingGuestBeginStamp{}
	}
	s.wedding.guestBegins[identity.character] = stamp
	return nil
}

// Caller owns wedding.mu, including the host movie's successful start event.
func (s *Service) publishCurrentWeddingGuestBegin(pair wedding.PairResult) error {
	state, _, err := wedding.ValidatePair(pair.Actor, pair.Partner)
	if err != nil {
		return err
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	ledger, found, err := owner.ReadGuestLedger(ctx, []string{pair.Actor.CharacterID, pair.Partner.CharacterID}, state.CeremonyAt)
	if err != nil || !found {
		return err
	}
	var published error
	for id := range ledger.Invitations {
		n, _ := wedding.NumericID(id)
		if guest, online := s.onlineGameSessionCharacterSnapshot(uint16(n)); online {
			published = errors.Join(published, s.sendCurrentWeddingGuestBeginForSession(guest.session))
		}
	}
	return published
}

func (s *Service) sendInitialCurrentWeddingGuestLetters(session *gameSession) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	// Invitation replay only reads durable ledgers and host identities. It
	// does not price or mutate a wedding, so unrelated ordinary logins must
	// not acquire the PVF rule dependency of the mutation owner.
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil || repos.Settings == nil {
		return wedding.ErrUnavailable
	}
	owner := wedding.Owner{Repositories: repos}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	views, err := owner.GuestInvitations(ctx, strconv.Itoa(int(identity.character)), s.gameplayNow())
	if err != nil {
		return err
	}
	var published error
	for _, view := range views {
		if !s.boundGameSessionCharacterCurrent(identity) {
			break
		}
		published = errors.Join(published, s.sendCurrentWeddingGuestLetter(session, view.Ledger, view.Hosts, strconv.Itoa(int(identity.character))))
	}
	return published
}

func (s *Service) currentWeddingGuestRemaining(ctx context.Context, a, b r.CharacterRecord) (uint16, error) {
	state, _, err := wedding.ValidatePair(a, b)
	if err != nil {
		return 0, err
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return 0, err
	}
	ledger, found, err := owner.ReadGuestLedger(ctx, []string{a.CharacterID, b.CharacterID}, state.CeremonyAt)
	if err != nil {
		return 0, err
	}
	if !found {
		quota := owner.Rules.Ceremonies[state.Kind].Invitations
		if quota < 0 || quota > 65535 {
			return 0, wedding.ErrState
		}
		return uint16(quota), nil
	}
	return uint16(ledger.RemainingFor(a.CharacterID)), nil
}
