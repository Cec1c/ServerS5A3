package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/party"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildTeleportWindow = 30 * time.Second

type currentGuildTeleportRequest struct {
	town, area      byte
	x, y            uint16
	direction, flag byte
}

type currentGuildTeleportPeer struct {
	identity boundGameSessionCharacter
	owner    r.GuildOwner
	slot     byte
	serial   uint64
}

type currentGuildTeleport struct {
	request     currentGuildTeleportRequest
	wireRequest currentGuildTeleportRequest
	id          string
	guildID     uint32
	joinedAt    time.Time
	leader      boundGameSessionCharacter
	party       party.RuntimePartySnapshot
	peers       []currentGuildTeleportPeer
	answers     map[uint16]byte
	timer       *time.Timer
	deadline    time.Time
	phase       byte
}

func parseCurrentGuildTeleportRequest(body []byte) (currentGuildTeleportRequest, bool) {
	if len(body) != 8 || body[6] != 5 || body[7] > 1 {
		return currentGuildTeleportRequest{}, false
	}
	q := currentGuildTeleportRequest{body[0], body[1], binary.LittleEndian.Uint16(body[2:4]), binary.LittleEndian.Uint16(body[4:6]), body[6], body[7]}
	return q, q.x <= 32767 && q.y <= 32767
}

func guildPartyTeleportGameplayModule() gameplayModuleDefinition {
	legacy := map[uint16]gameplayHandler{434: (*Service).handleCurrentGuildTeleport, 435: (*Service).handleCurrentGuildTeleportConfirm}
	upper := map[uint16]gameplayHandler{}
	for op, handler := range legacy {
		op, handler := op, handler
		upper[op] = func(s *Service, session *gameSession, request gameplayRequest) error {
			if !requireDefaultGameplayClassification(s, session, request, "game-guild-teleport-class-rejected", "command_class_mismatch") {
				return nil
			}
			return handler(s, session, request)
		}
	}
	return gameplayModuleDefinition{Name: "guild-party-teleport", LegacyHandlers: legacy, UpperHandlers: upper}
}

func (s *Service) guildTeleportFailure(session *gameSession, town, code byte) error {
	// 434's registered callback consumes u8/u32/u8 even for generic failures.
	body := []byte{0, code, town, 0, 0, 0, 0, 0}
	return s.sendGameUpperRawClass(session, 434, body, 1)
}

func (s *Service) handleCurrentGuildTeleport(session *gameSession, request gameplayRequest) error {
	q, valid := parseCurrentGuildTeleportRequest(request.Body)
	if !valid || request.Sequence == nil {
		return s.guildTeleportFailure(session, q.town, 7)
	}
	wireRequest := q
	source, valid := s.boundGameSessionCharacterSnapshot(session)
	if !valid {
		return nil
	}
	id, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return err
	}
	s.guildTeleportMu.Lock()
	defer s.guildTeleportMu.Unlock()
	if last := session.guild.teleportLast; last != nil && last.leader == source {
		if last.id == id {
			if last.wireRequest != q {
				return s.guildTeleportFailure(session, q.town, 7)
			}
			if last.phase == 1 {
				return s.sendGameUpperRawClass(session, 434, []byte{1}, 1)
			}
			return s.publishGuildTeleport(last, last.phase)
		}
		if q.town == 0 && q.area == 0 && q.x == 0 && q.y == 0 {
			q = last.request
		}
	}
	if session.guild.teleport != nil {
		return s.guildTeleportFailure(session, q.town, 7)
	}
	p := &currentGuildTeleport{request: q, wireRequest: wireRequest, id: id, leader: source, answers: map[uint16]byte{}, deadline: s.gameplayNow().Add(currentGuildTeleportWindow)}
	if snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(source.character, source.generation); found {
		if snapshot.Leader != source.character {
			return s.guildTeleportFailure(session, q.town, 7)
		}
		p.party = snapshot
		for _, member := range snapshot.Members {
			peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
			if !online || peer.generation != member.SessionGeneration || peer.session.guild.teleport != nil {
				return s.guildTeleportFailure(session, q.town, 7)
			}
			serial, _ := peer.session.guild.purchaseSequence.currentHigh()
			p.peers = append(p.peers, currentGuildTeleportPeer{peer, r.GuildOwner{AccountID: s.accountIDForSession(peer.session), CharacterID: strconv.Itoa(int(peer.character))}, member.Slot, serial})
		}
	} else {
		// A stale projected party must not turn a group request into solo travel.
		if runtimePartyStateSnapshot(session).PartyID != 0 {
			return s.guildTeleportFailure(session, q.town, 7)
		}
		p.peers = []currentGuildTeleportPeer{{source, r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(source.character))}, 0, *request.Sequence}}
	}
	sort.Slice(p.peers, func(i, j int) bool { return p.peers[i].identity.character < p.peers[j].identity.character })
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return s.guildTeleportFailure(session, q.town, 7)
	}
	g, membership, member := reg.GuildForOwner(r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(source.character))})
	if !member || !g.Benefits[7].Active(s.gameplayNow()) {
		return s.guildTeleportFailure(session, q.town, 7)
	}
	p.guildID = g.ID
	p.joinedAt = membership.JoinedAt
	if _, err := s.prepareGuildTeleport(ctx, p); err != nil {
		return s.guildTeleportFailure(session, q.town, 7)
	}
	p.answers[source.character] = 1
	for _, peer := range p.peers {
		peer.identity.session.guild.teleport = p
	}
	session.guild.teleportLast = p
	if err := s.sendGameUpperRawClass(session, 434, []byte{1}, 1); err != nil {
		s.clearGuildTeleport(p, 2)
		return err
	}
	if len(p.peers) == 1 {
		return s.commitGuildTeleport(p)
	}
	if err := s.publishGuildTeleport(p, 0); err != nil {
		s.clearGuildTeleport(p, 2)
		_ = s.publishGuildTeleport(p, 2)
		return err
	}
	p.timer = time.AfterFunc(currentGuildTeleportWindow, func() {
		s.guildTeleportMu.Lock()
		defer s.guildTeleportMu.Unlock()
		if p.leader.session.guild.teleport == p {
			s.clearGuildTeleport(p, 3)
			_ = s.publishGuildTeleport(p, 3)
		}
	})
	return nil
}

func (s *Service) handleCurrentGuildTeleportConfirm(session *gameSession, request gameplayRequest) error {
	if len(request.Body) != 1 || request.Sequence == nil || (request.Body[0] != 0 && request.Body[0] != 1 && request.Body[0] != 3) {
		return nil
	}
	s.guildTeleportMu.Lock()
	defer s.guildTeleportMu.Unlock()
	p := session.guild.teleport
	if p == nil {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	matched := false
	for _, peer := range p.peers {
		if peer.identity == identity && *request.Sequence > peer.serial {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	if !s.gameplayNow().Before(p.deadline) || request.Body[0] == 3 {
		s.clearGuildTeleport(p, 3)
		return s.publishGuildTeleport(p, 3)
	}
	if request.Body[0] == 0 {
		p.answers[identity.character] = 2
		s.clearGuildTeleport(p, 2)
		return s.publishGuildTeleport(p, 2)
	}
	if identity == p.leader {
		return nil
	}
	p.answers[identity.character] = 1
	if len(p.answers) < len(p.peers) {
		return s.publishGuildTeleport(p, 0)
	}
	return s.commitGuildTeleport(p)
}

func (s *Service) clearGuildTeleport(p *currentGuildTeleport, phase byte) {
	p.phase = phase
	if p.timer != nil {
		p.timer.Stop()
	}
	for _, peer := range p.peers {
		if peer.identity.session.guild.teleport == p {
			peer.identity.session.guild.teleport = nil
		}
	}
}

func (s *Service) abortGuildTeleport(p *currentGuildTeleport) error {
	s.clearGuildTeleport(p, 2)
	if len(p.peers) == 1 && s.boundGameSessionCharacterCurrent(p.leader) {
		return s.guildTeleportFailure(p.leader.session, p.request.town, 7)
	}
	return s.publishGuildTeleport(p, 2)
}

func buildGuildTeleportNotification(p *currentGuildTeleport, phase byte) []byte {
	body := make([]byte, 30)
	body[0], body[1] = p.request.town, phase
	for slot := 0; slot < 8; slot++ {
		binary.LittleEndian.PutUint16(body[5+3*slot:], 0xffff)
	}
	for _, peer := range p.peers {
		if peer.slot >= 8 {
			continue
		}
		if peer.identity == p.leader {
			binary.LittleEndian.PutUint16(body[3:], uint16(peer.slot))
		}
		at := 5 + 3*int(peer.slot)
		binary.LittleEndian.PutUint16(body[at:], peer.identity.character)
		body[at+2] = p.answers[peer.identity.character]
	}
	return body
}

func (s *Service) publishGuildTeleport(p *currentGuildTeleport, phase byte) error {
	if len(p.peers) < 2 {
		return nil
	}
	body := buildGuildTeleportNotification(p, phase)
	var failures []error
	for _, peer := range p.peers {
		if s.boundGameSessionCharacterCurrent(peer.identity) {
			failures = append(failures, s.sendGameUpperRawClass(peer.identity.session, 377, body, 0))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) prepareGuildTeleport(ctx context.Context, p *currentGuildTeleport) (guild.TeleportQuery, error) {
	q := guild.TeleportQuery{RequestID: p.id, GuildID: p.guildID, Initiator: r.GuildOwner{AccountID: s.accountIDForSession(p.leader.session), CharacterID: strconv.Itoa(int(p.leader.character))}, Destination: guild.TeleportLocation{Town: int64(p.request.town), Area: int64(p.request.area), X: int64(p.request.x), Y: int64(p.request.y), Direction: int64(p.request.direction)}}
	area, found := s.townArea(q.Destination.Town, q.Destination.Area)
	if !found || townMapPathLooksLikeSeria(area.MapPath) || townMapPathLooksLikeGuildHall(area.MapPath) || p.request.town == 10 {
		return q, guild.ErrTeleportUnavailable
	}
	q.MinimumLevel = int(area.MinLevel)
	q.JoinedAt = p.joinedAt
	repos, ok := s.repositoryGroup()
	if !ok {
		return q, guild.ErrRepositoryUnavailable
	}
	for _, peer := range p.peers {
		i := peer.identity
		if !s.tradeMu.TryLock() {
			return q, guild.ErrTeleportUnavailable
		}
		trading := s.itemTrades[i.character] != nil
		s.tradeMu.Unlock()
		if trading {
			return q, guild.ErrTeleportUnavailable
		}
		if !s.boundGameSessionCharacterCurrent(i) || currentTownPresenceChannelID(i.session) != currentTownPresenceChannelID(p.leader.session) || isPVPChannel(i.session.channel) || isCurrentFiendWarChannel(i.session) {
			return q, guild.ErrTeleportUnavailable
		}
		i.session.dungeon.mu.Lock()
		active := i.session.dungeon.runtime != nil
		i.session.dungeon.mu.Unlock()
		i.session.townMu.Lock()
		ready := i.session.townSceneReadyCharacterID == i.character
		i.session.townMu.Unlock()
		if active || !ready {
			return q, guild.ErrTeleportUnavailable
		}
		instance, _, admitted, release := s.lockCurrentAntonWaitingAreaScope(i.session, p.request.town, p.request.area)
		release()
		if !admitted || instance != 0 {
			return q, guild.ErrTeleportUnavailable
		}
		if s.options.currentClientVersion == "official-24.9" && p.request.town == 50 && !s.currentEpicRoadActivityAvailable() {
			return q, guild.ErrTeleportUnavailable
		}
		actor, exists, err := repos.Character.Load(ctx, peer.owner.CharacterID)
		if err != nil {
			return q, err
		}
		if !exists || actor.AccountID != peer.owner.AccountID || actor.Level < q.MinimumLevel || actor.Stats["area_state"] < 0 || actor.Stats["area_state"] > 255 {
			return q, guild.ErrTeleportUnavailable
		}
		if _, exists := actor.Stats["area_state"]; !exists {
			return q, guild.ErrTeleportUnavailable
		}
		completed, err := s.townAreaNeedQuestsCompleted(ctx, repos, peer.owner.CharacterID, area.NeedQuests)
		if err != nil {
			return q, err
		}
		if !completed {
			return q, guild.ErrTeleportUnavailable
		}
		q.Participants = append(q.Participants, guild.TeleportParticipant{Owner: peer.owner, From: guild.CharacterTeleportLocation(actor)})
	}
	q.Revalidate = func() bool {
		for _, peer := range p.peers {
			if !s.boundGameSessionCharacterCurrent(peer.identity) {
				return false
			}
		}
		return true
	}
	return q, nil
}

// The consent mutex is held. Asset bindings, each scene and the authoritative
// party snapshot are held across the one durable commit; sends happen afterward.
func (s *Service) commitGuildTeleport(p *currentGuildTeleport) error {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	ids := make([]boundGameSessionCharacter, 0, len(p.peers))
	for _, peer := range p.peers {
		ids = append(ids, peer.identity)
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, ids)
	if err != nil {
		return s.abortGuildTeleport(p)
	}
	defer func() { release() }()
	q, err := s.prepareGuildTeleport(ctx, p)
	if err != nil {
		return s.abortGuildTeleport(p)
	}
	manager := s.runtimePartyManagerForService()
	for _, peer := range p.peers {
		peer.identity.session.dungeon.mu.Lock()
		peer.identity.session.townMu.Lock()
	}
	commit := func() error {
		if !s.gameplayNow().Before(p.deadline) {
			return guild.ErrTeleportUnavailable
		}
		for _, peer := range p.peers {
			if peer.identity.session.dungeon.runtime != nil || peer.identity.session.townSceneReadyCharacterID != peer.identity.character {
				return guild.ErrTeleportUnavailable
			}
		}
		repos, ok := s.repositoryGroup()
		if !ok {
			return guild.ErrRepositoryUnavailable
		}
		_, e := guild.CommitTeleport(ctx, repos.Guild, q, s.gameplayNow())
		return e
	}
	if p.party.ID != 0 {
		err = manager.WithSnapshot(p.party, commit)
	} else {
		err = manager.WithSoloMember(p.leader.character, commit)
	}
	for i := len(p.peers) - 1; i >= 0; i-- {
		p.peers[i].identity.session.townMu.Unlock()
		p.peers[i].identity.session.dungeon.mu.Unlock()
	}
	if err != nil {
		s.logGameEvent(p.leader.session, "game-guild-teleport-commit-rejected", "error", err)
		return s.abortGuildTeleport(p)
	}
	s.clearGuildTeleport(p, 1)
	result := s.publishGuildTeleport(p, 1)
	for _, peer := range p.peers {
		body := buildCurrentSoloTeleportMoveBody(currentSoloTeleportRequest{TownID: p.request.town, AreaID: p.request.area, PositionX: int16(p.request.x), PositionY: int16(p.request.y), Direction: p.request.direction}, uint16(p.request.town))
		result = errors.Join(result, s.handleTownSetUserAreaAfterGuildTeleport(peer.identity.session, body, q.Destination))
	}
	// Release bindings before the shared guild projection acquires its own.
	release()
	release = func() {}
	s.refreshCurrentGuildText(ctx, p.guildID, p.leader, false)
	return result
}
