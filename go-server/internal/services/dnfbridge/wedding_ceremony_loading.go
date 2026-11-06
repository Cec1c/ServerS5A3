package dnfbridge

import (
	"context"
	"strconv"

	"longheng.io/server/internal/modules/dnf/wedding"
)

// The area's existing monotonic presence generation identifies the actual
// committed op24 scene, independently of the retired initial-login route.
type currentWeddingChurchLoadingProof struct {
	owner              boundGameSessionCharacter
	account            string
	presenceGeneration uint64
	partner            uint16
	instance           uint32
	guest              bool
	token              uint32
	at                 int64
	route              wedding.CeremonyRoute
}

// The town transition registers presence only after op24 has been written and
// its durable scene committed. It retires the login route and clears dungeon
// loading gates. Requiring those gates here deadlocks a normal Church entry.
func (s *Service) currentWeddingChurchTownSceneCurrent(owner boundGameSessionCharacter, p onlinePlayerInfo, route wedding.CeremonyRoute) bool {
	return s.currentWeddingChurchLoadingCurrent(currentWeddingChurchLoadingProof{
		owner: owner, account: p.AccountID, presenceGeneration: p.presenceGeneration,
		instance: p.Instance, route: route,
	})
}

// Ordinary town entry has no C2S finish-loading request. Its committed scene
// event owns the same visual projection for hosts, guests and reconnects.
// Caller holds wedding.mu; a guest's own marriage is not the attended wedding.
func (s *Service) replayCurrentWeddingChurchTownSceneLocked(ctx context.Context, identity boundGameSessionCharacter) (bool, error) {
	if s.onlinePlayers == nil {
		return false, nil
	}
	p, found := s.onlinePlayers.PlayerForCharacter(identity.character)
	if !found || p.TownID != 39 {
		return false, nil
	}
	area, found := s.townArea(39, int64(p.AreaID))
	if !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) {
		return false, nil
	}
	proof, reason := s.currentWeddingChurchLoadingProof(ctx, identity.session)
	if reason != "owned_church_scene" {
		return false, nil
	}
	if err := s.publishCurrentWeddingAvatarSetupLocked(identity.session, proof); err != nil {
		return proof.guest, err
	}
	if proof.guest {
		return true, s.sendCurrentWeddingGuestBeginForSession(identity.session)
	}
	return false, nil
}

// A native finish request witnesses loading; the server still sends the real
// op37/op30/post-finish tail. Never manufacture delivered gates or replay
// mode0/mode1 to restore an initial-login predicate retired by the town move.
func (s *Service) handleCurrentWeddingChurchFinishLoading(session *gameSession) (bool, error) {
	if session == nil || s.onlinePlayers == nil {
		return false, nil
	}
	s.mu.Lock()
	selected := session.selectedCharacterID
	s.mu.Unlock()
	presence, found := s.onlinePlayers.PlayerForCharacter(selected)
	if !found || presence.TownID != 39 {
		return false, nil
	}
	area, found := s.townArea(39, int64(presence.AreaID))
	if !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	s.wedding.mu.Lock()
	proof, reason := s.currentWeddingChurchLoadingProof(ctx, session)
	retired := false
	if reason == "owned_church_scene" {
		// Pending returns and selector owners retain their original lifecycle.
		if !session.dungeon.mu.TryLock() {
			reason = "dungeon_busy"
		} else {
			if session.dungeon.runtime != nil || session.confirmedDungeonReturnStatePending || session.selectorReturnPresence != nil {
				reason = "pending_return_or_dungeon"
			} else {
				retired = session.returnTownFinishLoadingAckOnly
				session.returnTownFinishLoadingAckOnly = false
			}
			session.dungeon.mu.Unlock()
		}
	}
	if reason != "owned_church_scene" {
		s.logCurrentWeddingChurchLoadingLocked(session, proof, reason, "rejected", false)
	}
	s.wedding.mu.Unlock()
	cancel()
	if reason != "owned_church_scene" {
		// No strict host claim was established. In particular, a real pending
		// return must continue through its original ACK-only/asset owner path.
		return false, nil
	}
	if !s.currentWeddingChurchLoadingCurrent(proof) {
		s.wedding.mu.Lock()
		s.logCurrentWeddingChurchLoadingLocked(session, proof, "scene_changed", "rejected", retired)
		s.wedding.mu.Unlock()
		return true, nil
	}
	// The existing tail inspects dungeon scopes and queues wedding recovery.
	// Never retain wedding, town or dungeon locks across those operations.
	err := s.sendCurrentFinishLoadingReleaseTail(session, "current_exe_owned_wedding_church_finish_loading")
	ctx, cancel = context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	s.wedding.mu.Lock()
	after, afterReason := s.currentWeddingChurchLoadingProof(ctx, session)
	if afterReason == "owned_church_scene" && after != proof {
		afterReason = "scene_changed"
	}
	outcome := "delivered"
	if err != nil {
		outcome, afterReason = "deferred", "state_tail_failed"
	} else if afterReason != "owned_church_scene" {
		outcome = "superseded"
	}
	s.logCurrentWeddingChurchLoadingLocked(session, proof, afterReason, outcome, retired)
	if err == nil && afterReason == "owned_church_scene" {
		err = s.publishCurrentWeddingAvatarSetupLocked(session, proof)
	}
	if err == nil && afterReason == "owned_church_scene" && proof.guest {
		err = s.sendCurrentWeddingGuestBeginForSession(session)
	}
	s.wedding.mu.Unlock()
	cancel()
	if err != nil || afterReason != "owned_church_scene" {
		return true, err
	}
	return true, s.queueCurrentWeddingCeremonyRecovery(session)
}

// Caller holds wedding.mu, but no scene lock. Cross-lock scene reads are
// nonblocking; no scene lock is retained while loading persisted participants.
func (s *Service) currentWeddingChurchLoadingProof(ctx context.Context, session *gameSession) (currentWeddingChurchLoadingProof, string) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	proof := currentWeddingChurchLoadingProof{owner: identity}
	if !bound || session.loginRevoked.Load() {
		return proof, "not_bound"
	}
	proof.account = s.accountIDForSession(session)
	if proof.account == "" {
		return proof, "ownership"
	}
	p, found := s.onlinePlayers.PlayerForCharacter(identity.character)
	if !found || p.Session != session || p.AccountID != proof.account || p.presenceGeneration == 0 {
		return proof, "presence_owner"
	}
	proof.presenceGeneration = p.presenceGeneration
	c, err := s.currentWeddingCharacter(ctx, identity)
	if err != nil {
		return proof, "character_unavailable"
	}
	if c.Stats["wedding_v1_guest_key"] != 0 {
		if guest, reason := s.currentWeddingGuestChurchLoadingFallback(ctx, session, proof, "guest_inactive"); reason == "owned_church_scene" {
			return guest, reason
		}
	}
	state, err := wedding.Read(c)
	if err != nil || (state.Phase != wedding.Reserved && state.Phase != wedding.InCeremony) || state.CeremonyAt <= 0 || wedding.ReservationExpired(state, s.gameplayNow().Unix()) {
		return s.currentWeddingGuestChurchLoadingFallback(ctx, session, proof, "reservation_inactive")
	}
	proof.partner, proof.at = uint16(state.PartnerID), state.CeremonyAt
	proof.instance, proof.token = weddingPairInstance(identity.character, proof.partner), currentWeddingRelationToken(state)
	proof.route = wedding.CeremonyRoute{Channel: byte(state.CeremonyChannel), Area: byte(state.CeremonyArea)}
	if c.Stats["town_id"] != 39 || c.Stats["area_id"] != state.CeremonyArea {
		return s.currentWeddingGuestChurchLoadingFallback(ctx, session, proof, "persisted_scene")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return proof, "repository_unavailable"
	}
	partner, found, err := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
	if err != nil || !found {
		return proof, "partner_unavailable"
	}
	if _, _, err = wedding.ValidatePair(c, partner); err != nil {
		return proof, "relationship_changed"
	}
	if !s.currentWeddingChurchLoadingCurrent(proof) {
		return proof, "scene_or_return_owner"
	}
	return proof, "owned_church_scene"
}

func (s *Service) currentWeddingChurchLoadingCurrent(proof currentWeddingChurchLoadingProof) bool {
	owner := proof.owner
	if !proof.route.Valid() || owner.session == nil || owner.session.loginRevoked.Load() ||
		!s.boundGameSessionCharacterCurrent(owner) || s.accountIDForSession(owner.session) != proof.account ||
		currentTownPresenceChannelID(owner.session) != int(proof.route.Channel) {
		return false
	}
	p, found := s.onlinePlayers.PlayerForCharacter(owner.character)
	if !found || p.Session != owner.session || p.AccountID != proof.account || p.presenceGeneration != proof.presenceGeneration ||
		p.TownID != 39 || p.AreaID != proof.route.Area || p.ChannelID != int(proof.route.Channel) ||
		p.Instance != proof.instance || p.RaidBattleInstance != "" {
		return false
	}
	area, found := s.townArea(39, int64(proof.route.Area))
	if !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) || !owner.session.dungeon.mu.TryLock() {
		return false
	}
	noReturn := owner.session.dungeon.runtime == nil && !owner.session.confirmedDungeonReturnStatePending && owner.session.selectorReturnPresence == nil
	owner.session.dungeon.mu.Unlock()
	if !noReturn || !owner.session.townMu.TryLock() {
		return false
	}
	position := owner.session.townPositionSnapshot
	ready := owner.session.townSceneReadyCharacterID == owner.character && position.CharacterID == owner.character &&
		position.TownID == 39 && position.AreaID == proof.route.Area
	owner.session.townMu.Unlock()
	after, found := s.onlinePlayers.PlayerForCharacter(owner.character)
	return ready && found && after.presenceGeneration == proof.presenceGeneration && after.Session == owner.session &&
		s.boundGameSessionCharacterCurrent(owner)
}

func (s *Service) currentWeddingGuestChurchLoadingFallback(ctx context.Context, session *gameSession, proof currentWeddingChurchLoadingProof, reason string) (currentWeddingChurchLoadingProof, string) {
	ledger, handled, e := s.currentWeddingGuestLoadingProof(ctx, session)
	if !handled || e != nil {
		return proof, reason
	}
	a, e1 := wedding.NumericID(ledger.Hosts[0])
	b, e2 := wedding.NumericID(ledger.Hosts[1])
	if e1 != nil || e2 != nil {
		return proof, "guest_hosts"
	}
	proof.guest, proof.instance, proof.token = true, weddingPairInstance(uint16(a), uint16(b)), ledger.Key
	proof.at, proof.route = ledger.At, wedding.CeremonyRoute{Channel: ledger.Channel, Area: ledger.Area}
	if !s.currentWeddingChurchLoadingCurrent(proof) {
		return proof, "guest_scene_or_return_owner"
	}
	return proof, "owned_church_scene"
}

func (s *Service) logCurrentWeddingChurchLoadingLocked(session *gameSession, proof currentWeddingChurchLoadingProof, reason, outcome string, retired bool) {
	if s.wedding.loadingDiagnostics == nil {
		s.wedding.loadingDiagnostics = make(map[uint16]string)
	}
	key := strconv.FormatUint(proof.owner.generation, 10) + ":" + strconv.FormatUint(proof.presenceGeneration, 10) + ":" + reason + ":" + outcome
	s.mu.Lock()
	id := session.selectedCharacterID
	s.mu.Unlock()
	if s.wedding.loadingDiagnostics[id] == key {
		return
	}
	s.wedding.loadingDiagnostics[id] = key
	gates := session.finishLoadingGateSnapshot()
	s.logInfo("dnfbridge wedding church loading diagnostic", "event", "game-wedding-church-finish-loading", "conn_id", session.connID,
		"char_id", id, "generation", proof.owner.generation, "presence_generation", proof.presenceGeneration,
		"partner_id", proof.partner, "ceremony_at", proof.at, "channel_id", proof.route.Channel, "area_id", proof.route.Area,
		"reason", reason, "outcome", outcome, "retired_ack_only", retired,
		"state_sent", gates.State, "completion_sent", gates.Completion, "post_finish_sent", gates.PostFinish)
}
