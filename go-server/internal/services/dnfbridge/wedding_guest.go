package dnfbridge

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"sort"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func (s *Service) currentWeddingGuestRules() (*wedding.GuestRules, error) {
	s.wedding.catalogMu.Lock()
	defer s.wedding.catalogMu.Unlock()
	if s.wedding.guestRules != nil || s.wedding.guestRulesErr != nil {
		return s.wedding.guestRules, s.wedding.guestRulesErr
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err == nil {
		s.wedding.guestRules, err = wedding.LoadGuestRules(archive)
	}
	s.wedding.guestRulesErr = err
	return s.wedding.guestRules, err
}

func currentWeddingGuestResult(err error, fallback int32) int32 {
	var code wedding.GuestError
	if errors.As(err, &code) {
		return int32(code)
	}
	return fallback
}

func (s *Service) currentWeddingGuestCandidates(identity boundGameSessionCharacter, ledger wedding.GuestLedger) []boundGameSessionCharacter {
	s.mu.Lock()
	var candidates []boundGameSessionCharacter
	for id, p := range s.gameSessions {
		if p != nil && p.selectedCharacterID == id && id != identity.character && strconv.Itoa(int(id)) != ledger.Hosts[0] && strconv.Itoa(int(id)) != ledger.Hosts[1] && !p.loginRevoked.Load() && currentTownPresenceChannelID(p) == int(ledger.Channel) {
			if _, invited := ledger.Invitations[strconv.Itoa(int(id))]; !invited {
				candidates = append(candidates, boundGameSessionCharacter{session: p, character: id, generation: p.characterGeneration})
			}
		}
	}
	s.mu.Unlock()
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].character < candidates[j].character })
	return candidates
}

func (s *Service) sendCurrentWeddingGuestTargets(session *gameSession, ledger wedding.GuestLedger) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || (strconv.Itoa(int(identity.character)) != ledger.Hosts[0] && strconv.Itoa(int(identity.character)) != ledger.Hosts[1]) {
		return wedding.ErrOwnership
	}
	var ids []uint32
	for id := range ledger.Invitations {
		n, err := strconv.ParseUint(id, 10, 16)
		if err != nil {
			return wedding.ErrState
		}
		ids = append(ids, uint32(n))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	body, valid := buildCurrentWeddingGuestTargets(ids)
	if !valid {
		return wedding.ErrState
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return wedding.ErrOwnership
	}
	return s.sendGameUpperRawClass(session, currentWeddingGuestTargetsNotice, body, 0)
}

func (s *Service) sendCurrentWeddingGuestLetter(session *gameSession, ledger wedding.GuestLedger, hosts [2]r.CharacterRecord, guestID string) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || strconv.Itoa(int(identity.character)) != guestID {
		return wedding.ErrOwnership
	}
	inv, invited := ledger.Invitations[guestID]
	if !invited {
		return wedding.ErrOwnership
	}
	first, second, err := currentWeddingCeremonyNames(hosts[0], hosts[1])
	if err != nil {
		return err
	}
	body := buildCurrentWeddingGuestLetter(ledger.Key, uint32(ledger.Area), uint32(ledger.At), uint32(ledger.Channel), ledger.Kind, first, second, inv.Gold > 0)
	if !s.boundGameSessionCharacterCurrent(identity) {
		return wedding.ErrOwnership
	}
	return s.sendGameUpperRawClass(session, currentWeddingGuestLetterNotice, body, 0)
}

func (s *Service) handleCurrentWeddingSendLetter(session *gameSession, body []byte) error {
	q, valid := decodeCurrentWeddingGuestLetterRequest(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	ack := func(code int32, left uint16) error {
		return s.sendGameUpperRawClass(session, currentWeddingSendLetterOpcode, buildCurrentWeddingGuestLetterACK(1, code, left), 1)
	}
	if !valid || !bound {
		return ack(-1, 0)
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return ack(-1, 0)
	}
	rules, err := s.currentWeddingGuestRules()
	if err != nil {
		return ack(-1, 0)
	}
	req, _, err := s.currentWeddingPartnerRequest(ctx, actor)
	if err != nil {
		return ack(-1, 0)
	}
	before, err := s.currentWeddingCharacter(ctx, actor)
	if err != nil {
		return ack(-1, 0)
	}
	ledger, err := owner.EnsureGuestWedding(ctx, req, rules)
	if err != nil {
		return ack(currentWeddingGuestResult(err, -1), 0)
	}
	if before.Stats["wedding_v1_relation_token"] != int64(ledger.Key) {
		// A legacy reserved row may have projected a general relation token.
		// Rebuild both owners before any notice carrying the newly allocated
		// run key; otherwise native 1004 rejects the mismatched model token.
		first, foundFirst, e1 := owner.Repositories.Character.Load(ctx, req.Actor.CharacterID)
		second, foundSecond, e2 := owner.Repositories.Character.Load(ctx, req.Partner.CharacterID)
		if e1 != nil || e2 != nil || !foundFirst || !foundSecond {
			return ack(-1, 0)
		}
		if err := s.publishCurrentWeddingPair(wedding.PairResult{Actor: first, Partner: second}); err != nil {
			return err
		}
		if current, online := s.onlineGameSessionCharacterSnapshot(actor.character); online && s.boundGameSessionCharacterCurrent(current) {
			if err := s.writeCurrentWeddingCeremony(current.session, first, second); err != nil {
				return err
			}
		}
		peerID, _ := wedding.NumericID(req.Partner.CharacterID)
		if peer, online := s.onlineGameSessionCharacterSnapshot(uint16(peerID)); online && s.boundGameSessionCharacterCurrent(peer) {
			if err := s.writeCurrentWeddingCeremony(peer.session, second, first); err != nil {
				return err
			}
		}
	}
	remaining := ledger.RemainingFor(req.Actor.CharacterID)
	if remaining <= 0 {
		return ack(-4, 0)
	}
	var ids []string
	var selected []boundGameSessionCharacter
	if q.Random {
		pool := s.currentWeddingGuestCandidates(actor, ledger)
		if len(pool) == 0 {
			s.logInfo("dnfbridge wedding random invitation empty", "event", "game-wedding-random-insufficient", "char_id", actor.character, "wedding_key", ledger.Key, "available", 0, "remaining", remaining)
			return ack(-7, uint16(remaining))
		}
		// Remaining letters are an upper limit, not a minimum population.
		// Sample only real uninvited peers; the transaction debits the actual
		// number sent and leaves the unused quota available for later guests.
		for i, count := 0, min(remaining, len(pool)); i < count; i++ {
			choice, e := rand.Int(rand.Reader, big.NewInt(int64(len(pool)-i)))
			if e != nil {
				return ack(-1, uint16(remaining))
			}
			j := i + int(choice.Int64())
			pool[i], pool[j] = pool[j], pool[i]
			selected = append(selected, pool[i])
			ids = append(ids, strconv.Itoa(int(pool[i].character)))
		}
	} else {
		id := strconv.FormatUint(uint64(q.Target), 10)
		if id == req.Actor.CharacterID || id == req.Partner.CharacterID {
			return ack(-6, uint16(remaining))
		}
		source, loadErr := s.currentWeddingCharacter(ctx, actor)
		if loadErr != nil || !s.currentWeddingNamedGuestAllowed(ctx, source, id) {
			return ack(-2, uint16(remaining))
		}
		ids = []string{id} // A real offline friend is allowed and replays on login.
	}
	baseRevalidate := req.Revalidate
	req.Revalidate = func() bool {
		if !baseRevalidate() || currentTownPresenceChannelID(session) != int(ledger.Channel) {
			return false
		}
		for _, p := range selected {
			if !s.boundGameSessionCharacterCurrent(p) || currentTownPresenceChannelID(p.session) != int(ledger.Channel) {
				return false
			}
		}
		return true
	}
	result, err := owner.InviteWeddingGuests(ctx, req, rules, ids)
	if err != nil {
		code := currentWeddingGuestResult(err, -1)
		err = ack(code, uint16(remaining))
		if code == -5 {
			err = errors.Join(err, s.sendCurrentWeddingGuestTargets(session, ledger))
		}
		return err
	}
	published := ack(0, result.Remaining)
	published = errors.Join(published, s.sendCurrentWeddingGuestTargets(session, result.Ledger))
	var hosts [2]r.CharacterRecord
	for i, id := range result.Ledger.Hosts {
		hosts[i], _, err = owner.Repositories.Character.Load(ctx, id)
		if err != nil {
			return errors.Join(published, err)
		}
	}
	for _, guest := range result.Guests {
		id, _ := wedding.NumericID(guest.CharacterID)
		if target, online := s.onlineGameSessionCharacterSnapshot(uint16(id)); online && s.accountIDForSession(target.session) == guest.AccountID && s.boundGameSessionCharacterCurrent(target) {
			published = errors.Join(published, s.sendCurrentWeddingGuestLetter(target.session, result.Ledger, hosts, guest.CharacterID))
		}
	}
	for _, host := range hosts {
		id, _ := wedding.NumericID(host.CharacterID)
		if target, online := s.onlineGameSessionCharacterSnapshot(uint16(id)); online && s.accountIDForSession(target.session) == host.AccountID && s.boundGameSessionCharacterCurrent(target) {
			// 1038 success updates the sender's own remaining quota. The
			// spouse's quota is unchanged; only its shared target list changed.
			// Replaying1004 here clears an open congratulations dialog again.
			if target.session != session {
				published = errors.Join(published, s.sendCurrentWeddingGuestTargets(target.session, result.Ledger))
			}
		}
	}
	s.logInfo("dnfbridge wedding invitation committed", "event", "game-wedding-guests-invited", "char_id", actor.character, "wedding_key", result.Ledger.Key, "guest_count", len(result.Guests), "remaining", result.Remaining, "notification_sent", published == nil)
	return published
}

func (s *Service) handleCurrentWeddingMoneyGift(session *gameSession, body []byte) error {
	q, valid := decodeCurrentWeddingMoneyGiftRequest(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	s.logInfo("dnfbridge wedding gift request", "event", "game-wedding-gift-received", "char_id", actor.character, "generation", actor.generation, "body_len", len(body), "decoded", valid, "bound", bound, "wedding_key", q.Key, "gold", q.Gold, "wish_bytes", len(q.Wish))
	ack := func(code int32) error {
		return s.sendGameUpperRawClass(session, currentWeddingMoneyGiftOpcode, buildCurrentWeddingMoneyGiftACK(1, code, q.Key), 1)
	}
	fail := func(reason string, cause error, code int32) error {
		sent := ack(code)
		s.logInfo("dnfbridge wedding gift rejected", "event", "game-wedding-gift-rejected", "char_id", actor.character, "wedding_key", q.Key, "reason", reason, "result", code, "error", cause, "notification_sent", sent == nil)
		return sent
	}
	if !valid || !bound {
		// Native -5 intentionally returns without a dialog. Invalid framing or
		// ownership must not look like a successful click with no response.
		return fail("request_or_binding", nil, -1)
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return fail("owner", err, -1)
	}
	rules, err := s.currentWeddingGuestRules()
	if err != nil {
		return fail("rules", err, -1)
	}
	req := wedding.GuestGiftRequest{Guest: wedding.Actor{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(actor.character))}, Key: q.Key, Gold: q.Gold, Wish: q.Wish, Now: s.gameplayNow(), Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(actor) }}
	result, err := owner.GiveWeddingGold(ctx, rules, req)
	if err != nil {
		return fail("payment", err, currentWeddingGuestResult(err, -1))
	}
	published := errors.Join(ack(0), s.sendCarryGoldWallet(session, result.Guest.Stats["gold"]))
	s.logInfo("dnfbridge wedding gift committed", "event", "game-wedding-gift-escrowed", "char_id", actor.character, "wedding_key", q.Key, "gold", q.Gold, "notification_sent", published == nil)
	return published
}
