package dnfbridge

import (
	"context"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentGuildForSelectedCharacter(session *gameSession) (*dnfguild.Guild, error) {
	if session == nil || session.selectedCharacterID == 0 {
		return nil, nil
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return nil, dnfguild.ErrRepositoryUnavailable
	}
	started := s.performanceStart()
	membership, member, err := dnfguild.ReadGuildMembership(ctx, repos.Settings, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil || !member {
		s.performanceEnd(perfGuildRegistryRead, started, err)
		return nil, err
	}
	character, err := s.currentGuildOwnedSelectedIdentity(session)
	if err != nil {
		s.performanceEnd(perfGuildRegistryRead, started, err)
		return nil, err
	}
	g, err := membership.Display(dnfrepo.GuildOwner{CharacterID: character.CharacterID, AccountID: character.AccountID})
	s.performanceEnd(perfGuildRegistryRead, started, err)
	return g, err
}

func currentGuildMemberChannelByte(channel int) byte {
	if channel > 0 && channel < 255 {
		return byte(channel)
	}
	return currentGuildMemberUnrepresentedChannel
}

func (s *Service) currentGuildOnlineChannels(g *dnfguild.Guild, requester *gameSession) map[string]byte {
	online := map[string]byte{}
	if g == nil {
		return online
	}
	for id, m := range g.Members {
		numericID, err := strconv.ParseUint(id, 10, 16)
		if err != nil || numericID == 0 {
			continue
		}
		peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(numericID))
		if ok && peer.session.conn != nil && s.accountIDForSession(peer.session) == m.AccountID && s.boundGameSessionCharacterCurrent(peer) {
			online[id] = currentGuildMemberChannelByte(peer.session.channel.ID)
		}
	}
	return online
}

// The caller must already own the selected town-scene lock. Do not call the
// scene readiness helper here: login invokes this after committing its actor.
func (s *Service) writeCurrentGuildManagementState(session *gameSession, g *dnfguild.Guild) error {
	return s.writeCurrentGuildManagementProjection(session, g, true)
}

func (s *Service) writeCurrentGuildManagementProjection(session *gameSession, g *dnfguild.Guild, notifyMembership bool) error {
	if session.channelExitAcknowledged {
		return nil
	}
	grade := byte(100)
	if g != nil {
		if m, ok := g.Members[strconv.Itoa(int(session.selectedCharacterID))]; ok && m.AccountID == s.accountIDForSession(session) {
			grade = m.Grade
		} else {
			g = nil
		}
	}
	if g != nil {
		if err := s.sendGameUpperRawClass(session, 70, buildCurrentGuildManagementBody(g, len(s.currentGuildOnlineChannels(g, session)), s.gameplayNow(), grade), 0); err != nil {
			return err
		}
	}
	if notifyMembership {
		if err := s.sendGameUpperRawClass(session, 127, []byte{grade}, 0); err != nil {
			return err
		}
		if err := s.sendGameUpperRawClass(session, currentGuildMembershipNotificationMsgID, buildCurrentGuildMembershipNotificationBody(g), 0); err != nil {
			return err
		}
		if err := s.writeCurrentGuildDungeonSnapshot(session); err != nil {
			return err
		}
		s.projectOptionalGuildEssenceSnapshot(session)
	}
	return s.writeCurrentGuildBuffProjection(session, g, false)
}

// A peer may be transitioning while another member sends a refresh. Skipping
// that optional projection avoids a cross-session town-lock inversion; the
// peer's own scene commit or next member-list request restores current state.
func (s *Service) sendCurrentGuildManagementState(session *gameSession, g *dnfguild.Guild) error {
	return s.sendCurrentGuildManagementProjection(session, g, true)
}

// A roster query also runs when opening the hongbao tab. Keep its model and
// buff refresh, without replaying login/membership-change notifications.
func (s *Service) sendCurrentGuildManagementProjection(session *gameSession, g *dnfguild.Guild, notifyMembership bool) error {
	if session == nil || !session.townMu.TryLock() {
		return nil
	}
	defer session.townMu.Unlock()
	if session.townSceneReadyCharacterID != session.selectedCharacterID || session.selectedCharacterID == 0 {
		return nil
	}
	return s.writeCurrentGuildManagementProjection(session, g, notifyMembership)
}

func (s *Service) refreshCurrentGuildMembership(ctx context.Context, session *gameSession, g *dnfguild.Guild) error {
	if err := s.currentSendNameTagActorRefresh(ctx, session); err != nil {
		return err
	}
	if err := s.sendCurrentGuildManagementState(session, g); err != nil {
		return err
	}
	return s.sendCurrentGuildAccountProjection(ctx, session)
}

func (s *Service) sendCurrentInitialTownGuildStateProjectionLocked(session *gameSession, source string) error {
	if repos, ok := s.repositoryGroup(); !ok || repos.Settings == nil || repos.Character == nil {
		return nil
	}
	if session.channelExitAcknowledged || !session.initialTownLegacySceneReadyAccepted || session.initialTownGuildStateProjectionSent || session.selectedCharacterID == 0 || session.townSceneReadyCharacterID != session.selectedCharacterID {
		return nil
	}
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil {
		return err
	}
	if err = s.writeCurrentGuildManagementState(session, g); err != nil {
		return err
	}
	// Native 1297 looks up the current guild object. Establish membership
	// before projecting the newly committed login income and its notice.
	s.projectOptionalGuildEssenceLoginLocked(session)
	s.grantOptionalGuildContributionAttendanceLocked(session)
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	if err = s.sendCurrentGuildAccountProjection(ctx, session); err != nil {
		return err
	}
	session.initialTownGuildStateProjectionSent = true
	s.logGameEvent(session, "game-initial-town-guild-state-projected", "source", source, "member", g != nil)
	return nil
}

func (s *Service) guildAccountProjection(ctx context.Context, session *gameSession) ([]currentGuildCharacterProjection, []currentGuildCharacterProjection, []*dnfguild.Guild, error) {
	repos, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	characters, err := repos.Character.ListByAccount(ctx, s.accountIDForSession(session), dnfrepo.DefaultCharacterSlotLimit)
	if err != nil {
		return nil, nil, nil, err
	}
	var members, applications []currentGuildCharacterProjection
	var guilds []*dnfguild.Guild
	seen := map[uint32]bool{}
	for _, c := range characters {
		if c.AccountID != s.accountIDForSession(session) {
			return nil, nil, nil, dnfguild.ErrCharacterNotFound
		}
		if g, member, ok := r.GuildForOwner(dnfrepo.GuildOwner{CharacterID: c.CharacterID, AccountID: c.AccountID}); ok {
			members = append(members, currentGuildCharacterProjection{CharacterName: c.Name, GuildName: g.Name, GuildID: g.ID, ServerGroup: member.ServerGroup})
			if !seen[g.ID] {
				guilds = append(guilds, g)
				seen[g.ID] = true
			}
			continue
		}
		for _, g := range r.Search("", 0, len(r.Guilds)) {
			if a, ok := g.Applications[c.CharacterID]; ok && a.AccountID == c.AccountID && c.CharacterID == strconv.Itoa(int(session.selectedCharacterID)) {
				applications = append(applications, currentGuildCharacterProjection{CharacterName: c.Name, GuildName: g.Name, GuildID: g.ID, ApplicationPending: 1})
				if !seen[g.ID] {
					guilds = append(guilds, g)
					seen[g.ID] = true
				}
			}
		}
	}
	return members, applications, guilds, nil
}

func (s *Service) sendCurrentGuildAccountProjection(ctx context.Context, session *gameSession) error {
	members, applications, guilds, err := s.guildAccountProjection(ctx, session)
	if err != nil {
		return err
	}
	if err = s.sendGameUpperSuccess(session, currentGuildAccountIndexOpcode, buildCurrentGuildAccountIndexBody(guilds, members)); err != nil {
		return err
	}
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketReqGuildInfoOfMyChars), buildCurrentGuildAccountListBody(applications))
}

func (s *Service) handleCurrentGuildAccountList(session *gameSession, body []byte) error {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	return s.sendCurrentGuildAccountProjection(ctx, session)
}

func (s *Service) handleCurrentGuildInfo(session *gameSession, _ []byte) error {
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil {
		return err
	}
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketGuildInfo), buildCurrentGuildInfoBody(uint32(session.selectedCharacterID), g))
}

func (s *Service) handleCurrentGuildMemberListForOpcode(session *gameSession, opcode uint16) error {
	return s.handleCurrentGuildMemberListObserved(session, opcode, "client")
}

// Roster and text projections need existence/ownership, not gameplay fields.
// Keep the same binding fence and use the existing non-saveable identity read.
func (s *Service) currentGuildOwnedSelectedIdentity(session *gameSession) (dnfrepo.CharacterIdentity, error) {
	bound, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || !s.boundGameSessionCharacterCurrent(bound) {
		return dnfrepo.CharacterIdentity{}, dnfguild.ErrCharacterNotFound
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return dnfrepo.CharacterIdentity{}, dnfguild.ErrRepositoryUnavailable
	}
	id := strconv.Itoa(int(bound.character))
	character, found, err := dnfrepo.LoadCharacterIdentity(ctx, repos.Character, id)
	if err != nil {
		return dnfrepo.CharacterIdentity{}, err
	}
	if !found || character.CharacterID != id || character.AccountID != s.accountIDForSession(session) || !s.boundGameSessionCharacterCurrent(bound) {
		return dnfrepo.CharacterIdentity{}, dnfguild.ErrCharacterNotFound
	}
	return character, nil
}

func (s *Service) handleCurrentGuildMemberListObserved(session *gameSession, opcode uint16, source string) (result error) {
	started := s.performanceStart()
	var readTime, buildTime, writeTime time.Duration
	defer func() {
		if started.IsZero() {
			return
		}
		total := time.Since(started)
		s.performance.record(perfGuildRosterTotal, total, result != nil)
		if s.performance.admitSlowSample(perfGuildRosterTotal, total, time.Now()) {
			s.logInfo("dnfbridge guild roster timing", "conn_id", session.connID, "generation", session.characterGeneration,
				"source", source, "opcode", opcode, "total_ms", total.Milliseconds(), "read_ms", readTime.Milliseconds(), "build_ms", buildTime.Milliseconds(), "write_ms", writeTime.Milliseconds(), "failed", result != nil)
		}
	}()
	s.logInfo("dnfbridge guild roster request", "conn_id", session.connID,
		"char_id", session.selectedCharacterID, "opcode", opcode)
	session.guild.mu.Lock()
	session.guild.memberListRequested = true
	session.guild.mu.Unlock()
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	full := opcode == uint16(dnfenum.CmdPacketGuildMemerList) || opcode == uint16(dnfenum.CmdPacketGuildAllMemberList)
	cursor := opcode == uint16(dnfenum.CmdPacketGuildAllMemberList) || opcode == uint16(dnfenum.CmdPacketGuildAllMemberListNext)
	bound, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return s.sendGameUpperFailure(session, opcode, currentGuildGenericError)
	}
	readStart := s.performanceStart()
	g, err := s.readCurrentGuildDisplay(ctx, bound, false, full)
	if !readStart.IsZero() {
		readTime = time.Since(readStart)
	}
	if !s.boundGameSessionCharacterCurrent(bound) {
		return nil
	}
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentGuildGenericError)
	}
	if full {
		if primeErr := s.primeCurrentWeddingGuildRosterForBound(ctx, bound, g); primeErr != nil {
			s.logGameEvent(session, "game-wedding-guild-roster-prime-deferred", "error", primeErr)
		}
		if err = s.sendCurrentGuildManagementProjection(session, g, false); err != nil {
			return err
		}
	}
	online := s.currentGuildOnlineChannels(g, session)
	members := 0
	if g != nil {
		members = len(g.Members)
	}
	s.logInfo("dnfbridge guild roster response", "conn_id", session.connID,
		"char_id", session.selectedCharacterID, "opcode", opcode,
		"members", members, "online", len(online), "full", full, "cursor", cursor)
	buildStart := s.performanceStart()
	body := buildCurrentGuildMemberListBody(g, full, cursor, online, s.gameplayNow(), byte(s.channelAdvertiseID()))
	s.performanceEnd(perfGuildProjectionBuild, buildStart, nil)
	if !buildStart.IsZero() {
		buildTime = time.Since(buildStart)
	}
	// EXIT and this final roster frame share the scene fence, just like NOTI127.
	if source == "client" {
		session.townMu.Lock()
	} else if !session.townMu.TryLock() {
		return nil // preserve optional cross-session projection lock ordering
	}
	defer session.townMu.Unlock()
	if session.channelExitAcknowledged || !s.boundGameSessionCharacterCurrent(bound) {
		return nil
	}
	writeStart := s.performanceStart()
	err = s.sendGameUpperSuccess(session, opcode, body)
	s.performanceEnd(perfGuildRosterWrite, writeStart, err)
	if !writeStart.IsZero() {
		writeTime = time.Since(writeStart)
	}
	return err
}

func (s *Service) broadcastCurrentGuildMembers(g *dnfguild.Guild, skip uint16) {
	if g == nil {
		return
	}
	for id, m := range g.Members {
		key := uint16(currentGuildNumericID(id))
		if key == skip {
			continue
		}
		peer, ok := s.onlineGameSessionCharacterSnapshot(key)
		if !ok || s.accountIDForSession(peer.session) != m.AccountID {
			continue
		}
		peer.session.guild.mu.Lock()
		requested := peer.session.guild.memberListRequested
		peer.session.guild.mu.Unlock()
		if requested {
			if err := s.handleCurrentGuildMemberListObserved(peer.session, uint16(dnfenum.CmdPacketGuildAllMemberList), "membership"); err != nil {
				s.logGameEvent(peer.session, "game-guild-member-refresh-failed", "error", err)
			}
		}
	}
}

func (s *Service) handleCurrentGuildSearch(session *gameSession, body []byte) error {
	request, ok := parseCurrentGuildSearchRequest(body)
	if !ok {
		return s.sendGameUpperFailure(session, currentGuildSearchOpcode, currentGuildGenericError)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	total, page, err := r.SearchDirectory(request.Filter, request.Page, currentGuildPageSize)
	if err != nil {
		return s.sendGameUpperFailure(session, currentGuildSearchOpcode, currentGuildGenericError)
	}
	return s.sendGameUpperSuccess(session, currentGuildSearchOpcode, buildCurrentGuildSearchListBody(total, request.Page, page))
}

func (s *Service) handleCurrentGuildRecommend(session *gameSession, _ []byte) error {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	rows := r.Search("", 0, currentGuildPageSize)
	members, _, own, err := s.guildAccountProjection(ctx, session)
	if err != nil {
		return err
	}
	seen := map[uint32]bool{}
	var all []*dnfguild.Guild
	for _, list := range [][]*dnfguild.Guild{rows, own} {
		for _, g := range list {
			if !seen[g.ID] {
				seen[g.ID] = true
				all = append(all, g)
			}
		}
	}
	if err = s.sendGameUpperSuccess(session, currentGuildAccountIndexOpcode, buildCurrentGuildAccountIndexBody(all, members)); err != nil {
		return err
	}
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketReqRecommendGuild), buildCurrentGuildRecommendationBody(rows))
}

func (s *Service) handleCurrentGuildDetail(session *gameSession, body []byte) error {
	name, ok := currentGuildDSTRAtStart(body)
	if !ok {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketReqGuildSerchForJoin), currentGuildGenericError)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	g, found := r.GuildByName(name)
	if !found {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketReqGuildSerchForJoin), currentGuildGenericError)
	}
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketReqGuildSerchForJoin), buildCurrentGuildSearchResultBody(g))
}

// Membership notifications are class-0. The similarly named op365 command is
// an account-board application acknowledgement and must keep its raw shape.
func (s *Service) handleCurrentGuildApplicationNotice(session *gameSession, _ []byte) error {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	c, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		return err
	}
	g, _, found := r.GuildForOwner(dnfrepo.GuildOwner{CharacterID: c.CharacterID, AccountID: c.AccountID})
	id := uint32(0)
	if found {
		id = g.ID
	}
	return s.sendGameUpperRaw(session, uint16(dnfenum.CmdPacketJoinGuildInfo), buildCurrentGuildApplicationNoticeBody(id, byte(c.Slot), c.Name))
}

func (s *Service) handleCurrentGuildGrades(session *gameSession, op uint16) error {
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil {
		return err
	}
	return s.sendGameUpperSuccess(session, op, buildCurrentGuildGradeBody(g, op == currentGuildGradesOpcode))
}
