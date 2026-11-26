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
	_, reg, err := s.readCurrentGuildDirectory(ctx)
	if err != nil {
		return access, err
	}
	vm, vok := reg.OwnedMember(dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(v.character)), AccountID: access.viewerAccount})
	am, aok := reg.OwnedMember(dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(a.character)), AccountID: access.actorAccount})
	if !vok || !aok || vm.GuildID == 0 || vm.GuildID != am.GuildID {
		return access, dnfguild.ErrNotMember
	}
	access.guildID, access.viewerJoined, access.actorJoined = vm.GuildID, vm.JoinedAt, am.JoinedAt
	_, err = s.validateCurrentGuildMemberProfileAccess(ctx, access)
	return access, err
}

func (s *Service) validateCurrentGuildMemberProfileAccess(ctx context.Context, a currentGuildMemberProfileAccess) (dnfrepo.CharacterMirrorStats, error) {
	if err := ctx.Err(); err != nil {
		return dnfrepo.CharacterMirrorStats{}, err
	}
	if !s.currentGuildMemberProfileIdentities(a) {
		return dnfrepo.CharacterMirrorStats{}, dnfguild.ErrNotMember
	}
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Character == nil {
		return dnfrepo.CharacterMirrorStats{}, dnfguild.ErrRepositoryUnavailable
	}
	var actor dnfrepo.CharacterMirrorStats
	for i, identity := range []boundGameSessionCharacter{a.viewer, a.actor} {
		account := a.viewerAccount
		if i == 1 {
			account = a.actorAccount
		}
		id := strconv.Itoa(int(identity.character))
		// Ownership checks need no skills, quests or inventory payload. Keep the
		// fresh read at each existing fence, including after waiting for the wire.
		c, found, err := dnfrepo.LoadCharacterMirrorStats(ctx, repos.Character, id, "delete_flag")
		if err != nil {
			return dnfrepo.CharacterMirrorStats{}, err
		}
		if !found || c.CharacterID != id || c.AccountID != account || c.Stats["delete_flag"] != 0 {
			return dnfrepo.CharacterMirrorStats{}, dnfguild.ErrCharacterNotFound
		}
		if i == 1 {
			actor = c
		}
	}
	_, reg, err := s.readCurrentGuildDirectory(ctx)
	if err != nil {
		return dnfrepo.CharacterMirrorStats{}, err
	}
	for i, identity := range []boundGameSessionCharacter{a.viewer, a.actor} {
		account, joined := a.viewerAccount, a.viewerJoined
		if i == 1 {
			account, joined = a.actorAccount, a.actorJoined
		}
		m, found := reg.OwnedMember(dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(identity.character)), AccountID: account})
		if !found || m.GuildID != a.guildID || !m.JoinedAt.Equal(joined) {
			return dnfrepo.CharacterMirrorStats{}, dnfguild.ErrNotMember
		}
	}
	if !s.currentGuildMemberProfileIdentities(a) {
		return dnfrepo.CharacterMirrorStats{}, dnfguild.ErrNotMember
	}
	return actor, ctx.Err()
}

// validateCurrentGuildRosterPrimeActorAccess validates the target actor right
// before writing a lightweight silent mode8 priming frame during a guild-roster
// sweep whose viewer and fresh guild snapshot were already validated for this
// request. It preserves live online-identity and delete_flag checks with 1
// narrow PK lookup instead of repeating 2x3 queries per peer.
func (s *Service) validateCurrentGuildRosterPrimeActorAccess(ctx context.Context, a currentGuildMemberProfileAccess) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.currentGuildMemberProfileIdentities(a) {
		return dnfguild.ErrNotMember
	}
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Character == nil {
		return dnfguild.ErrRepositoryUnavailable
	}
	id := strconv.Itoa(int(a.actor.character))
	c, found, err := dnfrepo.LoadCharacterMirrorStats(ctx, repos.Character, id, "delete_flag")
	if err != nil {
		return err
	}
	if !found || c.CharacterID != id || c.AccountID != a.actorAccount || c.Stats["delete_flag"] != 0 {
		return dnfguild.ErrCharacterNotFound
	}
	// The sweep's original guild snapshot can become stale while waiting for
	// preparation or the viewer transport. Revalidate both membership receipts
	// using the current narrow directory; keep the single actor PK read above.
	_, reg, err := s.readCurrentGuildDirectory(ctx)
	if err != nil {
		return err
	}
	for i, identity := range []boundGameSessionCharacter{a.viewer, a.actor} {
		account, joined := a.viewerAccount, a.viewerJoined
		if i == 1 {
			account, joined = a.actorAccount, a.actorJoined
		}
		m, found := reg.OwnedMember(dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(identity.character)), AccountID: account})
		if !found || m.GuildID != a.guildID || !m.JoinedAt.Equal(joined) {
			return dnfguild.ErrNotMember
		}
	}
	if !s.currentGuildMemberProfileIdentities(a) {
		return dnfguild.ErrNotMember
	}
	return ctx.Err()
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
	return s.sendCurrentGuildMemberProfileWithPolicy(ctx, a, false)
}

// cacheOnly sends the existing silent mode8 cache projection. It does not
// send the separate mode3 inspection request or create a visible scene actor.
func (s *Service) sendCurrentGuildMemberProfileWithPolicy(ctx context.Context, a currentGuildMemberProfileAccess, cacheOnly bool) error {
	_, _, err := s.sendCurrentGuildMemberProfileWithPolicyDelivered(ctx, a, cacheOnly)
	return err
}

func (s *Service) sendCurrentGuildMemberProfileWithPolicyDelivered(ctx context.Context, a currentGuildMemberProfileAccess, cacheOnly bool) (bool, uint64, error) {
	delivered, revision, _, _, err := s.sendCurrentGuildMemberProfileWithPolicyDeliveredWithWriteCtx(ctx, ctx, a, cacheOnly)
	return delivered, revision, err
}

// sendCurrentGuildRosterPrimeMode8DeliveredWithWriteCtx delivers a lightweight
// silent mode8 priming frame during a guild-roster sweep whose viewer and
// fresh guild snapshot g were already validated once for the current request.
func (s *Service) sendCurrentGuildRosterPrimeMode8DeliveredWithWriteCtx(
	prepCtx context.Context,
	writeCtx context.Context,
	a currentGuildMemberProfileAccess,
	g *dnfguild.Guild,
) (delivered bool, revision uint64, transportErr bool, err error) {
	if prepCtx == nil {
		prepCtx = context.Background()
	}
	if writeCtx == nil {
		writeCtx = prepCtx
	}
	if !s.currentGuildMemberProfileIdentities(a) {
		return false, 0, false, nil
	}
	priming, rev, cacheGen, dynamicExpiresAt, ok, err := s.loadOrBuildGuildCacheOnlyMode8(prepCtx, a, a.request.channel, g)
	if err != nil {
		return false, 0, false, err
	}
	if !ok {
		return false, 0, false, nil
	}
	frames := []currentGuildMemberProfileFrame{{2, 0, priming}}
	sent, writeFailed, err := s.sendCurrentGuildMemberProfileFramesDeliveredWithValidatorAndGuard(
		prepCtx,
		writeCtx,
		a,
		rev,
		frames,
		s.validateCurrentGuildRosterPrimeActorAccess,
		s.guildMode8CacheSendGuard(a.actor.character, cacheGen, dynamicExpiresAt),
	)
	return sent, rev, writeFailed, err
}

// sendCurrentGuildMemberProfileWithPolicyDeliveredWithWriteCtx bounds all
// pre-write preparation (validation, DB/singleflight build, character
// reservation, appearance/wire locks) by prepCtx, while passing writeCtx to
// beginGameWriteBudgetLocked so an optional priming budget expiring before the
// socket write never closes the viewer's TCP transport.
func (s *Service) sendCurrentGuildMemberProfileWithPolicyDeliveredWithWriteCtx(
	prepCtx context.Context,
	writeCtx context.Context,
	a currentGuildMemberProfileAccess,
	cacheOnly bool,
) (delivered bool, revision uint64, dynamicExpiresAt time.Time, transportErr bool, err error) {
	if prepCtx == nil {
		prepCtx = context.Background()
	}
	if writeCtx == nil {
		writeCtx = prepCtx
	}
	if _, err := s.validateCurrentGuildMemberProfileAccess(prepCtx, a); err != nil {
		if errors.Is(err, dnfguild.ErrNotMember) || errors.Is(err, dnfguild.ErrCharacterNotFound) {
			return false, 0, time.Time{}, false, nil
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return false, 0, time.Time{}, false, err
		}
		return false, 0, time.Time{}, false, nil
	}
	if cacheOnly && a.request.kind == 0 {
		priming, rev, cacheGen, dynExp, ok, err := s.loadOrBuildGuildCacheOnlyMode8(prepCtx, a, a.request.channel)
		if err != nil {
			return false, 0, time.Time{}, false, err
		}
		if !ok {
			return false, 0, time.Time{}, false, nil
		}
		frames := []currentGuildMemberProfileFrame{{2, 0, priming}}
		sent, writeFailed, err := s.sendCurrentGuildMemberProfileFramesDeliveredWithWriteCtxAndGuard(
			prepCtx,
			writeCtx,
			a,
			rev,
			frames,
			s.guildMode8CacheSendGuard(a.actor.character, cacheGen, dynExp),
		)
		return sent, rev, dynExp, writeFailed, err
	}
	repos, ok := s.repositoryGroupFor(prepCtx)
	if !ok || repos.Character == nil {
		return false, 0, time.Time{}, false, dnfguild.ErrRepositoryUnavailable
	}
	c, found, err := repos.Character.Load(prepCtx, strconv.Itoa(int(a.actor.character)))
	if err != nil {
		return false, 0, time.Time{}, false, err
	}
	if !found || c.CharacterID != strconv.Itoa(int(a.actor.character)) || c.AccountID != a.actorAccount || c.Stats["delete_flag"] != 0 {
		return false, 0, time.Time{}, false, nil
	}
	actor := a.actor.session
	revision = currentCrossSceneAppearanceRevision(actor)
	var frames []currentGuildMemberProfileFrame
	if a.request.kind == 4 {
		body, err := s.buildCurrentGuildMemberAdventure(prepCtx, a, c)
		if err != nil {
			return false, 0, time.Time{}, false, err
		}
		frames = append(frames, currentGuildMemberProfileFrame{currentAdventureInfoRequestMsgID, 1, upperSuccessBody(body)})
	} else {
		owner := a.request.channel
		buildCtx := prepCtx
		if s.equipMode0RepositoryMemoEnabled() {
			buildCtx, _ = withEquipMode0RepositoryMemo(prepCtx)
		}
		mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(buildCtx, actor, a.actor.character, c.Name, c, true, owner)
		if err != nil {
			return false, 0, time.Time{}, false, err
		}
		detailCtx := context.WithValue(buildCtx, nativePeerProfileSkillsKey{}, c.CharacterID)
		if buffSwapReapplyEnabled() {
			detailCtx = context.WithValue(detailCtx, nativePeerBuffInspectKey{}, c.CharacterID)
		}
		mode3 := s.buildCurrentSelectedUserInfoMode3BodyInContext(detailCtx, actor, repos.LegacyUserInfo, c, true, a.actor.character, owner)
		priming, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
		if err != nil {
			return false, 0, time.Time{}, false, err
		}
		frames = []currentGuildMemberProfileFrame{{2, 0, priming}, {2, 0, mode3}}
		if cacheOnly {
			frames = frames[:1]
		}
	}
	sent, writeFailed, err := s.sendCurrentGuildMemberProfileFramesDeliveredWithWriteCtx(prepCtx, writeCtx, a, revision, frames)
	return sent, revision, time.Time{}, writeFailed, err
}

// Keep this checked wire path separate from the party/raid memory-only
// callback. Membership is stored in the guild repository and must be reread
// after a wire wait. No service, guild-owner or database transaction lock is
// held across a network write; only this actor's appearance and viewer stream.
func (s *Service) sendCurrentGuildMemberProfileFrames(ctx context.Context, a currentGuildMemberProfileAccess, revision uint64, frames []currentGuildMemberProfileFrame) error {
	_, err := s.sendCurrentGuildMemberProfileFramesDelivered(ctx, a, revision, frames)
	return err
}

// sendCurrentGuildMemberProfileFramesDelivered 仅在所有帧都直接写出到底层连接
// （outcome == upperPacketWritten，排除阻断/诊断过滤、批次缓冲待写及任何错误）时
// 才返回 true，供调用方据此维护预热去重缓存。
func (s *Service) sendCurrentGuildMemberProfileFramesDelivered(ctx context.Context, a currentGuildMemberProfileAccess, revision uint64, frames []currentGuildMemberProfileFrame) (bool, error) {
	delivered, _, err := s.sendCurrentGuildMemberProfileFramesDeliveredWithWriteCtx(ctx, ctx, a, revision, frames)
	return delivered, err
}

func (s *Service) sendCurrentGuildMemberProfileFramesDeliveredWithWriteCtx(
	prepCtx context.Context,
	writeCtx context.Context,
	a currentGuildMemberProfileAccess,
	revision uint64,
	frames []currentGuildMemberProfileFrame,
) (delivered bool, transportErr bool, err error) {
	return s.sendCurrentGuildMemberProfileFramesDeliveredWithWriteCtxAndGuard(prepCtx, writeCtx, a, revision, frames, nil)
}

func (s *Service) sendCurrentGuildMemberProfileFramesDeliveredWithWriteCtxAndGuard(
	prepCtx context.Context,
	writeCtx context.Context,
	a currentGuildMemberProfileAccess,
	revision uint64,
	frames []currentGuildMemberProfileFrame,
	validateCache func() bool,
) (delivered bool, transportErr bool, err error) {
	return s.sendCurrentGuildMemberProfileFramesDeliveredWithValidatorAndGuard(
		prepCtx,
		writeCtx,
		a,
		revision,
		frames,
		nil,
		validateCache,
	)
}

func (s *Service) sendCurrentGuildMemberProfileFramesDeliveredWithValidator(
	prepCtx context.Context,
	writeCtx context.Context,
	a currentGuildMemberProfileAccess,
	revision uint64,
	frames []currentGuildMemberProfileFrame,
	validate func(context.Context, currentGuildMemberProfileAccess) error,
) (delivered bool, transportErr bool, err error) {
	return s.sendCurrentGuildMemberProfileFramesDeliveredWithValidatorAndGuard(
		prepCtx,
		writeCtx,
		a,
		revision,
		frames,
		validate,
		nil,
	)
}

func (s *Service) sendCurrentGuildMemberProfileFramesDeliveredWithValidatorAndGuard(
	prepCtx context.Context,
	writeCtx context.Context,
	a currentGuildMemberProfileAccess,
	revision uint64,
	frames []currentGuildMemberProfileFrame,
	validate func(context.Context, currentGuildMemberProfileAccess) error,
	validateCache func() bool,
) (delivered bool, transportErr bool, err error) {
	if len(frames) == 0 {
		return false, false, nil
	}
	if prepCtx == nil {
		prepCtx = context.Background()
	}
	if writeCtx == nil {
		writeCtx = prepCtx
	}
	if validate == nil {
		validate = func(ctx context.Context, access currentGuildMemberProfileAccess) error {
			_, err := s.validateCurrentGuildMemberProfileAccess(ctx, access)
			return err
		}
	}
	if err := prepCtx.Err(); err != nil {
		return false, false, err
	}
	release, err := s.reserveCurrentGuildParticipants(prepCtx, []boundGameSessionCharacter{a.viewer, a.actor})
	if err != nil {
		// A peer may reconnect between preparation and binding reservation.
		// Drop this inspection instead of failing the viewer's command stream.
		if errors.Is(err, errDungeonWorldMapUnavailable) {
			return false, false, nil
		}
		return false, false, err
	}
	defer release()
	actor, viewer := a.actor.session, a.viewer.session
	if err := lockCurrentCrossSceneProfile(prepCtx, &actor.crossSceneAppearanceMu); err != nil {
		return false, false, err
	}
	defer actor.crossSceneAppearanceMu.Unlock()
	if err := s.lockGameWire(viewer, prepCtx); err != nil {
		return false, false, err
	}
	defer viewer.wireMu.Unlock()
	if err := prepCtx.Err(); err != nil {
		return false, false, err
	}
	finishWrite, err := beginGameWriteBudgetLocked(viewer, writeCtx)
	if err != nil {
		return false, false, err
	}
	defer finishWrite()
	allWritten := true
	for i, frame := range frames {
		if revision != actor.crossSceneAppearanceRevision {
			return false, false, nil
		}
		if validateCache != nil && !validateCache() {
			return false, false, nil
		}
		if err := validate(prepCtx, a); err != nil {
			if errors.Is(err, dnfguild.ErrNotMember) || errors.Is(err, dnfguild.ErrCharacterNotFound) {
				return false, false, nil
			}
			return false, false, err
		}
		if validateCache != nil && !validateCache() {
			return false, false, nil
		}
		if i == 0 {
			if err := prepCtx.Err(); err != nil {
				return false, false, err
			}
		}
		outcome, err := s.sendGameUpperRawClassCodecLockedWithOutcome(viewer, frame.opcode, frame.body, frame.class, true)
		if err != nil {
			viewer.closeGameTransport()
			return false, true, err
		}
		if outcome != upperPacketWritten {
			allWritten = false
		}
	}
	return allWritten, false, nil
}
