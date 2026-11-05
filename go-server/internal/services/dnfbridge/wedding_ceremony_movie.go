package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"strconv"
	"time"
)

type currentWeddingMovieEnd struct {
	owner         boundGameSessionCharacter
	presence      uint64
	instance      uint32
	token         uint32
	route         wedding.CeremonyRoute
	kind          byte
	first, second []byte
	deadline      int64
	timer         *time.Timer
}

type currentWeddingAvatarStamp struct {
	owner         boundGameSessionCharacter
	token         uint32
	route         wedding.CeremonyRoute
	kind          byte
	first, second []byte
	timer         *time.Timer
}

// Current native NOTI999 consumes 71 bytes. Nonzero removes matching temporary
// visuals and invokes normal Church exit. It does not declare marriage success.
func buildCurrentWeddingCeremonyEnd(kind byte, route wedding.CeremonyRoute, first, second []byte) []byte {
	b := make([]byte, 71)
	b[0], b[1] = 1, kind
	copy(b[2:31], first)
	copy(b[32:61], second)
	binary.LittleEndian.PutUint32(b[62:], uint32(route.Area))
	b[66] = kind
	binary.LittleEndian.PutUint32(b[67:], uint32(route.Channel))
	return b
}

func buildCurrentWeddingAvatarSetup(kind byte, route wedding.CeremonyRoute, first, second []byte) []byte {
	b := buildCurrentWeddingCeremonyEnd(kind, route, first, second)
	b[0] = 0
	return b
}

// The native setup path applies real weddingsystemavatar.etc templates for
// both names. Send only after a proven committed Church scene and within its exact
// server-owned instance, including peers already present before this arrival.
func (s *Service) publishCurrentWeddingAvatarSetupLocked(session *gameSession, proof currentWeddingChurchLoadingProof) error {
	if !s.currentWeddingChurchLoadingCurrent(proof) {
		return wedding.ErrOwnership
	}
	if s.wedding.churchScenes[proof.owner.character] == proof {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	owner, e := s.currentWeddingOwner()
	if e != nil {
		return e
	}
	c, e := s.currentWeddingCharacter(ctx, proof.owner)
	if e != nil {
		return e
	}
	var hosts [2]r.CharacterRecord
	var kind byte
	if proof.guest {
		ledger, pair, e := owner.GuestChurchProof(ctx, c, s.gameplayNow())
		if e != nil {
			return e
		}
		hosts, kind = pair, ledger.Kind
	} else {
		state, e := wedding.Read(c)
		if e != nil || (state.Phase != wedding.Reserved && state.Phase != wedding.InCeremony) {
			return wedding.ErrState
		}
		partner, found, e := owner.Repositories.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if e != nil {
			return e
		}
		if !found {
			return wedding.ErrState
		}
		if _, _, e = wedding.ValidatePair(c, partner); e != nil {
			return e
		}
		hosts, kind = [2]r.CharacterRecord{c, partner}, state.Kind
	}
	first, second, e := currentWeddingCeremonyNames(hosts[0], hosts[1])
	if e != nil {
		return e
	}
	body := buildCurrentWeddingAvatarSetup(kind, proof.route, first, second)
	var published error
	peers := map[uint16]boundGameSessionCharacter{proof.owner.character: proof.owner}
	for _, peer := range s.onlinePlayers.PeersInSameArea(proof.owner.character) {
		if peer.TownID != 39 || peer.AreaID != proof.route.Area || peer.ChannelID != int(proof.route.Channel) || peer.Instance != proof.instance {
			continue
		}
		identity, online := s.onlineGameSessionCharacterSnapshot(peer.CharacterID)
		if !online || identity.session != peer.Session || !s.boundGameSessionCharacterCurrent(identity) || s.accountIDForSession(identity.session) != peer.AccountID {
			continue
		}
		peers[identity.character] = identity
	}
	for _, identity := range peers {
		if s.boundGameSessionCharacterCurrent(identity) {
			err := s.sendGameUpperRawClass(identity.session, 999, body, 0)
			published = errors.Join(published, err)
			if err == nil {
				if s.wedding.avatarSetups == nil {
					s.wedding.avatarSetups = map[uint16]*currentWeddingAvatarStamp{}
				}
				if old := s.wedding.avatarSetups[identity.character]; old != nil && old.timer != nil {
					old.timer.Stop()
				}
				s.wedding.avatarSetups[identity.character] = &currentWeddingAvatarStamp{owner: identity, token: proof.token, route: proof.route, kind: kind, first: append([]byte(nil), first...), second: append([]byte(nil), second...)}
			}
		}
	}
	if published == nil {
		if s.wedding.churchScenes == nil {
			s.wedding.churchScenes = make(map[uint16]currentWeddingChurchLoadingProof)
		}
		s.wedding.churchScenes[proof.owner.character] = proof
	}
	return published
}

func (s *Service) cleanupCurrentWeddingAvatarLocked(stamp *currentWeddingAvatarStamp) error {
	if s.wedding.ceremoniesStopped || s.wedding.avatarSetups[stamp.owner.character] != stamp {
		return nil
	}
	if !s.boundGameSessionCharacterCurrent(stamp.owner) {
		delete(s.wedding.avatarSetups, stamp.owner.character)
		return nil
	}
	e := s.sendGameUpperRawClass(stamp.owner.session, 999, buildCurrentWeddingCeremonyEnd(stamp.kind, stamp.route, stamp.first, stamp.second), 0)
	if e == nil {
		delete(s.wedding.avatarSetups, stamp.owner.character)
	} else {
		if stamp.timer != nil {
			stamp.timer.Stop()
		}
		stamp.timer = time.AfterFunc(5*time.Second, func() {
			s.wedding.mu.Lock()
			defer s.wedding.mu.Unlock()
			_ = s.cleanupCurrentWeddingAvatarLocked(stamp)
		})
	}
	return e
}

// An expired/cancelled reservation also removes temporary visual setup. This
// native cleanup has no congratulation message and does not mutate marriage.
func (s *Service) cleanupCurrentWeddingReservationAvatarsLocked(pair wedding.PairResult) error {
	a, e := wedding.Read(pair.Actor)
	if e != nil {
		return e
	}
	b, e := wedding.Read(pair.Partner)
	if e != nil {
		return e
	}
	var published error
	for _, stamp := range s.wedding.avatarSetups {
		if stamp.token == currentWeddingRelationToken(a) || stamp.token == currentWeddingRelationToken(b) {
			published = errors.Join(published, s.cleanupCurrentWeddingAvatarLocked(stamp))
		}
	}
	return published
}

// Caller owns wedding.mu and no scene/asset lock. Each successful recipient
// gets its own end deadline; a late guest is not cut off by the hosts' timer.
func (s *Service) sendCurrentWeddingMovieBeginLocked(identity boundGameSessionCharacter, token uint32, route wedding.CeremonyRoute, kind byte, first, second []byte) error {
	if !s.boundGameSessionCharacterCurrent(identity) || s.onlinePlayers == nil {
		return wedding.ErrOwnership
	}
	p, found := s.onlinePlayers.PlayerForCharacter(identity.character)
	if !found || p.Session != identity.session || p.TownID != 39 || p.AreaID != route.Area || p.ChannelID != int(route.Channel) || p.presenceGeneration == 0 {
		return wedding.ErrState
	}
	rules, e := s.currentWeddingRules()
	if e != nil || rules.CeremonySeconds <= 0 {
		return wedding.ErrUnavailable
	}
	if e = s.sendGameUpperRawClass(identity.session, 1007, buildCurrentWeddingCeremonyBegin(token, route.Area, first, second), 0); e != nil {
		return e
	}
	if s.wedding.movieEnds == nil {
		s.wedding.movieEnds = map[uint16]*currentWeddingMovieEnd{}
	}
	if old := s.wedding.movieEnds[identity.character]; old != nil && old.timer != nil {
		old.timer.Stop()
	}
	movie := &currentWeddingMovieEnd{owner: identity, presence: p.presenceGeneration, instance: p.Instance, token: token, route: route, kind: kind, first: append([]byte(nil), first...), second: append([]byte(nil), second...), deadline: s.gameplayNow().Unix() + rules.CeremonySeconds}
	s.wedding.movieEnds[identity.character] = movie
	s.armCurrentWeddingMovieEndLocked(movie, time.Unix(movie.deadline, 0).Sub(s.gameplayNow()))
	return nil
}

func (s *Service) armCurrentWeddingMovieEndLocked(movie *currentWeddingMovieEnd, delay time.Duration) {
	if s.wedding.ceremoniesStopped || s.wedding.movieEnds[movie.owner.character] != movie {
		return
	}
	if movie.timer != nil {
		movie.timer.Stop()
	}
	movie.timer = time.AfterFunc(max(delay, 0), func() { s.runCurrentWeddingMovieEnd(movie) })
}

func (s *Service) runCurrentWeddingMovieEnd(movie *currentWeddingMovieEnd) {
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	if s.wedding.ceremoniesStopped || s.wedding.movieEnds[movie.owner.character] != movie {
		return
	}
	movie.timer = nil
	retire := func() { delete(s.wedding.movieEnds, movie.owner.character) }
	if !s.boundGameSessionCharacterCurrent(movie.owner) || s.onlinePlayers == nil {
		retire()
		return
	}
	p, found := s.onlinePlayers.PlayerForCharacter(movie.owner.character)
	if !found || p.Session != movie.owner.session || p.presenceGeneration != movie.presence || p.Instance != movie.instance || p.TownID != 39 || p.AreaID != movie.route.Area || p.ChannelID != int(movie.route.Channel) {
		retire()
		return
	}
	if now := s.gameplayNow(); now.Unix() < movie.deadline {
		s.armCurrentWeddingMovieEndLocked(movie, time.Unix(movie.deadline, 0).Sub(now))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	owner, e := s.currentWeddingOwner()
	if e != nil {
		s.armCurrentWeddingMovieEndLocked(movie, 5*time.Second)
		return
	}
	var hosts [2]r.CharacterRecord
	if ref, e := owner.ResolveGuestWedding(ctx, movie.token); e == nil {
		for i, id := range ref.Hosts {
			c, found, e := owner.Repositories.Character.Load(ctx, id)
			if e != nil || !found {
				s.armCurrentWeddingMovieEndLocked(movie, 5*time.Second)
				return
			}
			hosts[i] = c
		}
	} else {
		if !errors.Is(e, wedding.ErrState) {
			s.armCurrentWeddingMovieEndLocked(movie, 5*time.Second)
			return
		}
		// Legacy isolated reservations can retain their old token. Its owner
		// is still read from the current reciprocal pair, never from token bits.
		c, e := s.currentWeddingCharacter(ctx, movie.owner)
		if e != nil {
			retire()
			return
		}
		state, e := wedding.Read(c)
		if e != nil || currentWeddingRelationToken(state) != movie.token {
			retire()
			return
		}
		partner, found, e := owner.Repositories.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if e != nil || !found {
			s.armCurrentWeddingMovieEndLocked(movie, 5*time.Second)
			return
		}
		hosts = [2]r.CharacterRecord{c, partner}
	}
	state, _, e := wedding.ValidatePair(hosts[0], hosts[1])
	if e != nil || currentWeddingRelationToken(state) != movie.token {
		retire()
		return
	}
	if state.Phase == wedding.Reserved || state.Phase == wedding.InCeremony {
		s.armCurrentWeddingMovieEndLocked(movie, 5*time.Second)
		return
	}
	if state.Phase != wedding.Married {
		retire()
		return
	}
	if e = s.sendGameUpperRawClass(movie.owner.session, 999, buildCurrentWeddingCeremonyEnd(movie.kind, movie.route, movie.first, movie.second), 0); e != nil {
		s.armCurrentWeddingMovieEndLocked(movie, 5*time.Second)
		return
	}
	if stamp := s.wedding.avatarSetups[movie.owner.character]; stamp != nil && stamp.token == movie.token && stamp.owner == movie.owner {
		if stamp.timer != nil {
			stamp.timer.Stop()
		}
		delete(s.wedding.avatarSetups, movie.owner.character)
	}
	retire()
}
