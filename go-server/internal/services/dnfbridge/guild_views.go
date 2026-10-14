package dnfbridge

import (
	"context"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentGuildForSelectedCharacter(session *gameSession) (*dnfguild.Guild, error) {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return nil, err
	}
	g, _, _ := registry.GuildForCharacter(strconv.Itoa(int(session.selectedCharacterID)))
	return g, nil
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
		peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(currentGuildNumericID(id)))
		if ok && peer.session.conn != nil && s.accountIDForSession(peer.session) == m.AccountID && s.boundGameSessionCharacterCurrent(peer) {
			online[id] = currentGuildMemberChannelByte(peer.session.channel.ID)
		}
	}
	return online
}

// The caller must already own the selected town-scene lock. Do not call the
// scene readiness helper here: login invokes this after committing its actor.
func (s *Service) writeCurrentGuildManagementState(session *gameSession, g *dnfguild.Guild) error {
	grade := byte(100)
	if g != nil {
		if m, ok := g.Members[strconv.Itoa(int(session.selectedCharacterID))]; ok {
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
	if err := s.sendGameUpperRawClass(session, 127, []byte{grade}, 0); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, currentGuildMembershipNotificationMsgID, buildCurrentGuildMembershipNotificationBody(g), 0)
}

// A peer may be transitioning while another member sends a refresh. Skipping
// that optional projection avoids a cross-session town-lock inversion; the
// peer's own scene commit or next member-list request restores current state.
func (s *Service) sendCurrentGuildManagementState(session *gameSession, g *dnfguild.Guild) error {
	if session == nil || !session.townMu.TryLock() {
		return nil
	}
	defer session.townMu.Unlock()
	if session.townSceneReadyCharacterID != session.selectedCharacterID || session.selectedCharacterID == 0 {
		return nil
	}
	return s.writeCurrentGuildManagementState(session, g)
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
	if !session.initialTownLegacySceneReadyAccepted || session.initialTownGuildStateProjectionSent || session.selectedCharacterID == 0 || session.townSceneReadyCharacterID != session.selectedCharacterID {
		return nil
	}
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil {
		return err
	}
	if err = s.writeCurrentGuildManagementState(session, g); err != nil {
		return err
	}
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
		if g, member, ok := r.GuildForCharacter(c.CharacterID); ok {
			members = append(members, currentGuildCharacterProjection{CharacterName: c.Name, GuildName: g.Name, GuildID: g.ID, ServerGroup: member.ServerGroup})
			if !seen[g.ID] {
				guilds = append(guilds, g)
				seen[g.ID] = true
			}
			continue
		}
		for _, g := range r.Search("", 0, len(r.Guilds)) {
			if _, ok := g.Applications[c.CharacterID]; ok && c.CharacterID == strconv.Itoa(int(session.selectedCharacterID)) {
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
	session.guild.mu.Lock()
	session.guild.memberListRequested = true
	session.guild.mu.Unlock()
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, currentGuildGenericError)
	}
	g, _, _ := r.GuildForCharacter(strconv.Itoa(int(session.selectedCharacterID)))
	// Refresh only this detached read projection. Listing a guild is not a
	// mutation and never rewrites another player's persisted profile.
	if g != nil {
		for id, m := range g.Members {
			if c, found, e := repos.Character.Load(ctx, id); e == nil && found && c.AccountID == m.AccountID {
				m.Name = c.Name
				m.Level = c.Level
				m.Job = c.Job
				m.GrowType = strconv.FormatInt(c.Stats["grow_type"], 10)
				g.Members[id] = m
			}
		}
	}
	full := opcode == uint16(dnfenum.CmdPacketGuildMemerList) || opcode == uint16(dnfenum.CmdPacketGuildAllMemberList)
	cursor := opcode == uint16(dnfenum.CmdPacketGuildAllMemberList) || opcode == uint16(dnfenum.CmdPacketGuildAllMemberListNext)
	if full {
		if err = s.sendCurrentGuildManagementState(session, g); err != nil {
			return err
		}
	}
	return s.sendGameUpperSuccess(session, opcode, buildCurrentGuildMemberListBody(g, full, cursor, s.currentGuildOnlineChannels(g, session), s.gameplayNow(), byte(s.channelAdvertiseID())))
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
			if err := s.handleCurrentGuildMemberListForOpcode(peer.session, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
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
	all := r.Search(request.Query, 0, len(r.Guilds))
	page := r.Search(request.Query, request.Page, currentGuildPageSize)
	return s.sendGameUpperSuccess(session, currentGuildSearchOpcode, buildCurrentGuildSearchListBody(len(all), request.Page, page))
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
	repos, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	c, err := selectedGuildCharacter(ctx, repos, session)
	if err != nil {
		return err
	}
	g, _, found := r.GuildForCharacter(c.CharacterID)
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
