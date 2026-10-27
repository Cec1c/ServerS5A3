package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildMemberProfileOpcode = uint16(dnfenum.CmdPacketRaidOtherChannelUserinfo)

type currentGuildMemberProfileRequest struct {
	channel, kind, directory byte
	character                uint16
}

func parseCurrentGuildMemberProfileRequest(body []byte) (currentGuildMemberProfileRequest, bool) {
	if len(body) != 5 {
		return currentGuildMemberProfileRequest{}, false
	}
	r := currentGuildMemberProfileRequest{channel: body[0], character: binary.LittleEndian.Uint16(body[1:3]), kind: body[3], directory: body[4]}
	return r, r.channel > 0 && r.channel < 255 && r.character > 0 && r.character < 65535 && (r.kind == 0 || r.kind == 4) && r.directory < 250
}

// A receipt binds the online identities and the membership incarnations, not
// the name or the UID alone. No client-supplied account/guild ID is trusted.
type currentGuildMemberProfileAccess struct {
	viewer, actor               boundGameSessionCharacter
	viewerAccount, actorAccount string
	viewerChannel               int
	request                     currentGuildMemberProfileRequest
	guildID                     uint32
	viewerJoined, actorJoined   time.Time
}

func (s *Service) currentGuildMemberProfileIdentities(a currentGuildMemberProfileAccess) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" &&
		a.viewer.session != nil && a.actor.session != nil && a.viewer.session != a.actor.session &&
		a.viewer.character != 0 && a.actor.character != 0 && a.viewer.character != a.actor.character &&
		a.viewer.session.conn != nil && a.actor.session.conn != nil &&
		!a.viewer.session.loginRevoked.Load() && !a.actor.session.loginRevoked.Load() &&
		s.boundGameSessionCharacterCurrent(a.viewer) && s.boundGameSessionCharacterCurrent(a.actor) &&
		a.viewerAccount != "" && a.actorAccount != "" &&
		s.accountIDForSession(a.viewer.session) == a.viewerAccount && s.accountIDForSession(a.actor.session) == a.actorAccount &&
		a.viewerChannel > 0 && currentTownPresenceChannelID(a.viewer.session) == a.viewerChannel &&
		currentTownPresenceChannelID(a.actor.session) == int(a.request.channel) &&
		a.request.character == a.actor.character && a.request.directory == s.currentSceneActorRoute()
}

func (s *Service) loadCurrentGuildMemberProfileAccess(ctx context.Context, viewer *gameSession, request currentGuildMemberProfileRequest) (currentGuildMemberProfileAccess, error) {
	v, vok := s.boundGameSessionCharacterSnapshot(viewer)
	a, aok := s.onlineGameSessionCharacterSnapshot(request.character)
	if !vok || !aok {
		return currentGuildMemberProfileAccess{}, dnfguild.ErrNotMember
	}
	access := currentGuildMemberProfileAccess{viewer: v, actor: a, viewerAccount: s.accountIDForSession(viewer), actorAccount: s.accountIDForSession(a.session), viewerChannel: currentTownPresenceChannelID(viewer), request: request}
	if !s.currentGuildMemberProfileIdentities(access) {
		return access, dnfguild.ErrNotMember
	}
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return access, err
	}
	vg, vm, vok := reg.GuildForOwner(dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(v.character)), AccountID: access.viewerAccount})
	ag, am, aok := reg.GuildForOwner(dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(a.character)), AccountID: access.actorAccount})
	if !vok || !aok || vg.ID == 0 || vg.ID != ag.ID {
		return access, dnfguild.ErrNotMember
	}
	access.guildID, access.viewerJoined, access.actorJoined = vg.ID, vm.JoinedAt, am.JoinedAt
	_, err = s.validateCurrentGuildMemberProfileAccess(ctx, access)
	return access, err
}

func (s *Service) validateCurrentGuildMemberProfileAccess(ctx context.Context, a currentGuildMemberProfileAccess) (dnfrepo.CharacterRecord, error) {
	if err := ctx.Err(); err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	if !s.currentGuildMemberProfileIdentities(a) {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrNotMember
	}
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Character == nil {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrRepositoryUnavailable
	}
	var actor dnfrepo.CharacterRecord
	for i, identity := range []boundGameSessionCharacter{a.viewer, a.actor} {
		account := a.viewerAccount
		if i == 1 {
			account = a.actorAccount
		}
		id := strconv.Itoa(int(identity.character))
		c, found, err := repos.Character.Load(ctx, id)
		if err != nil {
			return dnfrepo.CharacterRecord{}, err
		}
		if !found || c.CharacterID != id || c.AccountID != account || c.Stats["delete_flag"] != 0 {
			return dnfrepo.CharacterRecord{}, dnfguild.ErrCharacterNotFound
		}
		if i == 1 {
			actor = c
		}
	}
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	for i, identity := range []boundGameSessionCharacter{a.viewer, a.actor} {
		account, joined := a.viewerAccount, a.viewerJoined
		if i == 1 {
			account, joined = a.actorAccount, a.actorJoined
		}
		g, m, found := reg.GuildForOwner(dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(identity.character)), AccountID: account})
		if !found || g.ID != a.guildID || !m.JoinedAt.Equal(joined) {
			return dnfrepo.CharacterRecord{}, dnfguild.ErrNotMember
		}
	}
	if !s.currentGuildMemberProfileIdentities(a) {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrNotMember
	}
	return actor, ctx.Err()
}

func (s *Service) handleCurrentGuildMemberProfile(session *gameSession, body []byte) error {
	r, ok := parseCurrentGuildMemberProfileRequest(body)
	if !ok {
		return nil
	}
	ctx, cancel := guildChatContext(session)
	defer cancel()
	a, err := s.loadCurrentGuildMemberProfileAccess(ctx, session, r)
	if err != nil {
		s.logGameEvent(session, "game-guild-member-profile-rejected", "target_char_id", r.character, "error", err)
		return nil
	}
	return s.sendCurrentGuildMemberProfile(ctx, a)
}

// Local-context menus retain their native op8/op1401 requests. Only a current
// same-channel guild relationship may opt into this separate owner; existing
// party/raid and ordinary visible-peer permissions remain unchanged.
func (s *Service) tryCurrentGuildMemberProfile(session *gameSession, target uint16, kind byte) (bool, error) {
	channel := currentTownPresenceChannelID(session)
	if channel <= 0 || channel >= 255 {
		return false, nil
	}
	r := currentGuildMemberProfileRequest{channel: byte(channel), character: target, kind: kind, directory: s.currentSceneActorRoute()}
	ctx, cancel := guildChatContext(session)
	defer cancel()
	a, err := s.loadCurrentGuildMemberProfileAccess(ctx, session, r)
	if err != nil {
		return false, nil
	}
	return true, s.sendCurrentGuildMemberProfile(ctx, a)
}

type currentGuildMemberProfileFrame struct {
	opcode uint16
	class  byte
	body   []byte
}

func (s *Service) sendCurrentGuildMemberProfile(ctx context.Context, a currentGuildMemberProfileAccess) error {
	c, err := s.validateCurrentGuildMemberProfileAccess(ctx, a)
	if err != nil {
		return nil
	}
	actor := a.actor.session
	revision := currentCrossSceneAppearanceRevision(actor)
	var frames []currentGuildMemberProfileFrame
	if a.request.kind == 4 {
		body, err := s.buildCurrentGuildMemberAdventure(ctx, a, c)
		if err != nil {
			return err
		}
		frames = append(frames, currentGuildMemberProfileFrame{currentAdventureInfoRequestMsgID, 1, upperSuccessBody(body)})
	} else {
		owner := a.request.channel
		mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, actor, a.actor.character, c.Name, c, true, owner)
		if err != nil {
			return err
		}
		repos, ok := s.repositoryGroupFor(ctx)
		if !ok {
			return dnfguild.ErrRepositoryUnavailable
		}
		ctx = context.WithValue(ctx, nativePeerProfileSkillsKey{}, c.CharacterID)
		if buffSwapReapplyEnabled() {
			ctx = context.WithValue(ctx, nativePeerBuffInspectKey{}, c.CharacterID)
		}
		mode3 := s.buildCurrentSelectedUserInfoMode3BodyInContext(ctx, actor, repos.LegacyUserInfo, c, true, a.actor.character, owner)
		priming, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
		if err != nil {
			return err
		}
		frames = []currentGuildMemberProfileFrame{{2, 0, priming}, {2, 0, mode3}}
	}
	return s.sendCurrentGuildMemberProfileFrames(ctx, a, revision, frames)
}

// Keep this checked wire path separate from the party/raid memory-only
// callback. Membership is stored in the guild repository and must be reread
// after a wire wait. No service, guild-owner or database transaction lock is
// held across a network write; only this actor's appearance and viewer stream.
func (s *Service) sendCurrentGuildMemberProfileFrames(ctx context.Context, a currentGuildMemberProfileAccess, revision uint64, frames []currentGuildMemberProfileFrame) error {
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{a.viewer, a.actor})
	if err != nil {
		return err
	}
	defer release()
	actor, viewer := a.actor.session, a.viewer.session
	if err := lockCurrentCrossSceneProfile(ctx, &actor.crossSceneAppearanceMu); err != nil {
		return err
	}
	defer actor.crossSceneAppearanceMu.Unlock()
	if err := lockCurrentCrossSceneProfile(ctx, &viewer.wireMu); err != nil {
		return err
	}
	defer viewer.wireMu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.DeadlineExceeded
	}
	if err := viewer.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = viewer.conn.SetWriteDeadline(time.Now()); close(finished) })
	defer func() {
		if !stop() {
			<-finished
		}
		_ = viewer.conn.SetWriteDeadline(time.Time{})
	}()
	for _, frame := range frames {
		if revision != actor.crossSceneAppearanceRevision {
			return nil
		}
		if _, err := s.validateCurrentGuildMemberProfileAccess(ctx, a); err != nil {
			if errors.Is(err, dnfguild.ErrNotMember) || errors.Is(err, dnfguild.ErrCharacterNotFound) {
				return nil
			}
			return err
		}
		if err := s.sendGameUpperRawClassCodecLocked(viewer, frame.opcode, frame.body, frame.class, true); err != nil {
			_ = viewer.conn.Close()
			return err
		}
	}
	return nil
}
