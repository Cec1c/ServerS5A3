package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	currentGuildGenericError          = byte(34)
	currentGuildInvalidNameError      = byte(106)
	currentGuildDuplicateNameError    = byte(107)
	currentGuildPromoterMissingError  = byte(108)
	currentGuildCreateGoldError       = byte(31)
	currentGuildCreateStateError      = byte(104)
	currentGuildCreatePermitReject    = currentGuildGenericError
	currentGuildCreatePermitStale     = byte(0)
	currentGuildCreateInviteOpcode    = uint16(685)
	currentGuildCreateAuthStateOpcode = uint16(686)
	currentGuildCreateCancelOpcode    = uint16(687)
	currentGuildCreateAuthResetValue  = byte(2)
)

type currentGuildCreateInvitation struct {
	creator     boundGameSessionCharacter
	target      boundGameSessionCharacter
	accepted    bool
	timer       *time.Timer
	operationID string
}

type guildSessionState struct {
	placement             currentGuildPlacementProjection
	mu                    sync.Mutex
	purchaseSequence      guildPurchaseSequence
	teleport              *currentGuildTeleport
	teleportLast          *currentGuildTeleport
	pendingName           string
	nameChecked           bool
	promotion             string
	createCommitted       bool
	createInvitation      *currentGuildCreateInvitation
	incomingInvitation    *currentGuildInvitation
	outgoingInvitations   map[*currentGuildInvitation]struct{}
	lastCreate            *dnfguild.CreateRequest
	soloOperationID       string
	memberListRequested   bool
	cargoLoadedGuild      uint32
	cargoLoadedGeneration uint64
	cargoPending          uint32
}

func (s *Service) handleCurrentGuildCreate(session *gameSession, body []byte) error {
	if len(body) != 1 || body[0] != 1 {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCallGuildCreateRight), currentGuildCreateStateError)
	}
	var founder boundGameSessionCharacter
	result, err := func() (dnfguild.CreateResult, error) {
		s.guildCreateMu.Lock()
		defer s.guildCreateMu.Unlock()
		identity, bound := s.boundGameSessionCharacterSnapshot(session)
		if !bound {
			return dnfguild.CreateResult{}, dnfguild.ErrCharacterNotFound
		}
		repos, ok := s.repositoryGroup()
		if !ok {
			return dnfguild.CreateResult{}, dnfguild.ErrRepositoryUnavailable
		}
		var req dnfguild.CreateRequest
		if session.guild.createCommitted && session.guild.lastCreate != nil {
			req = *session.guild.lastCreate
			req.Revalidate = func() bool { return s.boundGameSessionCharacterCurrent(identity) }
			if id, err := strconv.ParseUint(req.Founder.CharacterID, 10, 16); err == nil {
				if peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(id)); ok && s.accountIDForSession(peer.session) == req.Founder.AccountID {
					founder = peer
				}
			}
		} else if s.options.guildSoloCreate {
			if session.guild.incomingInvitation != nil {
				return dnfguild.CreateResult{}, dnfguild.ErrPermissionDenied
			}
			if !session.guild.nameChecked || session.guild.soloOperationID == "" {
				return dnfguild.CreateResult{}, dnfguild.ErrPermissionDenied
			}
			req = dnfguild.CreateRequest{OperationID: session.guild.soloOperationID,
				Creator: dnfrepo.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))},
				Name:    session.guild.pendingName, Promotion: session.guild.promotion,
				Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) }}
		} else {
			invite := session.guild.createInvitation
			if invite == nil || !invite.accepted || invite.creator.session != session || !session.guild.nameChecked {
				return dnfguild.CreateResult{}, dnfguild.ErrPermissionDenied
			}
			founder = invite.target
			req = dnfguild.CreateRequest{OperationID: invite.operationID, Creator: dnfrepo.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}, Founder: dnfrepo.GuildOwner{AccountID: s.accountIDForSession(founder.session), CharacterID: strconv.Itoa(int(founder.character))}, Name: session.guild.pendingName, Promotion: session.guild.promotion, Revalidate: func() bool {
				return s.boundGameSessionCharacterCurrent(invite.creator) && s.boundGameSessionCharacterCurrent(invite.target)
			}}
		}
		ctx, cancel := s.currentGuildContext()
		defer cancel()
		identities := []boundGameSessionCharacter{identity}
		if !session.guild.createCommitted && !s.options.guildSoloCreate {
			identities = append(identities, founder)
		}
		release, err := s.reserveCurrentGuildParticipants(ctx, identities)
		if err != nil {
			return dnfguild.CreateResult{}, err
		}
		defer release()
		create := dnfguild.CreateWithFoundingMember
		if s.options.guildSoloCreate {
			create = dnfguild.CreateSolo
		}
		created, err := create(ctx, repos.Guild, req, s.gameplayNow())
		if err != nil {
			return dnfguild.CreateResult{}, err
		}
		session.guild.lastCreate = &req
		session.guild.createCommitted = true
		if invite := session.guild.createInvitation; invite != nil {
			s.clearCurrentGuildCreateInvitation(invite)
		}
		return created, nil
	}()
	if err != nil {
		code := currentGuildGenericError
		switch {
		case errors.Is(err, dnfguild.ErrInsufficientGold):
			code = currentGuildCreateGoldError
		case errors.Is(err, dnfguild.ErrNameExists):
			code = currentGuildDuplicateNameError
		case errors.Is(err, dnfguild.ErrAlreadyMember):
			code = 33
		}
		s.logGameEvent(session, "game-guild-create-rejected", "error", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCallGuildCreateRight), code)
	}
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketCallGuildCreateRight), nil); err != nil {
		return err
	}
	if err := s.sendCarryGoldWallet(session, result.GoldAfter); err != nil {
		return err
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	if err := s.refreshCurrentGuildMembership(ctx, session, result.Guild); err != nil {
		return err
	}
	if founder.session != nil && s.boundGameSessionCharacterCurrent(founder) {
		if err := s.refreshCurrentGuildMembership(ctx, founder.session, result.Guild); err != nil {
			s.logGameEvent(founder.session, "game-guild-founder-refresh-failed", "error", err)
		}
	}
	s.logGameEvent(session, "game-guild-create-committed", "guild_id", result.Guild.ID, "replay", result.Replay)
	return nil
}

// Use the existing paid-asset binding barrier: validation alone would leave a
// gap in which reconnect/selection could retire consent before SQL commit.
func (s *Service) reserveCurrentGuildParticipants(ctx context.Context, identities []boundGameSessionCharacter) (func(), error) {
	ordered := append([]boundGameSessionCharacter(nil), identities...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].character < ordered[j].character })
	var held []*currentCharacterAssetReservation
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			s.releaseCurrentCharacterAssets(held[i])
		}
	}
	for _, identity := range ordered {
		r, err := s.reserveCurrentCharacterAssets(ctx, identity, s.accountIDForSession(identity.session))
		if err != nil {
			release()
			return nil, err
		}
		held = append(held, r)
	}
	return release, nil
}

func (s *Service) clearCurrentGuildCreateInvitation(invite *currentGuildCreateInvitation) {
	if invite.timer != nil {
		invite.timer.Stop()
	}
	for _, identity := range []boundGameSessionCharacter{invite.creator, invite.target} {
		if identity.session.guild.createInvitation == invite {
			identity.session.guild.createInvitation = nil
		}
	}
}

func (s *Service) cancelCurrentGuildCreateInvitation(invite *currentGuildCreateInvitation, result byte, notifyTarget bool) error {
	s.clearCurrentGuildCreateInvitation(invite)
	var err error
	if s.boundGameSessionCharacterCurrent(invite.creator) {
		err = s.sendGameUpperSecondaryRaw(invite.creator.session, currentGuildCreateAuthStateOpcode, []byte{result})
	}
	if notifyTarget && s.boundGameSessionCharacterCurrent(invite.target) {
		err = errors.Join(err, s.sendGameUpperSecondaryRaw(invite.target.session, currentGuildCreateCancelOpcode, nil))
	}
	return err
}

func (s *Service) closeCurrentGuildCreateSession(session *gameSession) {
	s.ensureGameplayTimeQueue().Cancel(guildBenefitTimerName(session))
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	s.closeCurrentGuildInvitationsLocked(session)
	if invite := session.guild.createInvitation; invite != nil {
		if err := s.cancelCurrentGuildCreateInvitation(invite, currentGuildCreateAuthResetValue, true); err != nil {
			s.logGameEvent(session, "game-guild-create-cancel-notify-failed", "error", err)
		}
	}
	session.guild.mu.Lock()
	session.guild.pendingName = ""
	session.guild.nameChecked = false
	session.guild.promotion = ""
	session.guild.createCommitted = false
	session.guild.lastCreate = nil
	session.guild.soloOperationID = ""
	session.guild.memberListRequested = false
	session.guild.mu.Unlock()
}

func (s *Service) expireCurrentGuildCreateInvitation(invite *currentGuildCreateInvitation) {
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	if invite.creator.session.guild.createInvitation == invite && !invite.accepted {
		if err := s.cancelCurrentGuildCreateInvitation(invite, currentGuildCreateAuthResetValue, true); err != nil {
			s.logGameEvent(invite.creator.session, "game-guild-create-expiry-notify-failed", "error", err)
		}
	}
}

func (s *Service) currentGuildContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), createWriteTimeout)
}

func (s *Service) loadCurrentGuildRegistry(ctx context.Context) (dnfrepo.Group, dnfguild.Registry, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Settings == nil || repositories.Character == nil {
		return dnfrepo.Group{}, dnfguild.Registry{}, dnfguild.ErrRepositoryUnavailable
	}
	registry, err := dnfguild.Load(ctx, repositories.Settings)
	return repositories, registry, err
}

func (s *Service) readCurrentGuild(
	ctx context.Context,
	read func(dnfrepo.Group, dnfguild.Registry) error,
) error {
	repositories, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	return read(repositories, registry)
}

func selectedGuildCharacter(ctx context.Context, repositories dnfrepo.Group, session *gameSession) (dnfrepo.CharacterRecord, error) {
	if session == nil || session.selectedCharacterID == 0 || repositories.Character == nil {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrCharacterNotFound
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	if !found || character.CharacterID == "" {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrCharacterNotFound
	}
	return character, nil
}

func (s *Service) currentGuildOwnedSelectedCharacter(session *gameSession) (dnfrepo.CharacterRecord, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !s.boundGameSessionCharacterCurrent(identity) {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrCharacterNotFound
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrRepositoryUnavailable
	}
	character, err := selectedGuildCharacter(ctx, repositories, session)
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	if character.AccountID != s.accountIDForSession(session) || !s.boundGameSessionCharacterCurrent(identity) {
		return dnfrepo.CharacterRecord{}, dnfguild.ErrCharacterNotFound
	}
	return character, nil
}

func currentGuildDSTRAtStart(body []byte) (string, bool) {
	value, next, ok := parseCurrentGuildDSTR(body, 0)
	return value, ok && next == len(body) && value != ""
}

func (s *Service) handleCurrentGuildNameCheck(session *gameSession, body []byte) error {
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	if invite := session.guild.createInvitation; invite != nil {
		if err := s.cancelCurrentGuildCreateInvitation(invite, currentGuildCreateAuthResetValue, true); err != nil {
			return err
		}
	}
	session.guild.mu.Lock()
	session.guild.nameChecked = false
	session.guild.soloOperationID = ""
	session.guild.mu.Unlock()
	name, ok := currentGuildDSTRAtStart(body)
	if !ok || dnfguild.ValidateName(name) != nil {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCheckGuildNameDouble), currentGuildInvalidNameError)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	available := false
	err := s.readCurrentGuild(ctx, func(_ dnfrepo.Group, registry dnfguild.Registry) error {
		available = registry.NameAvailable(name)
		return nil
	})
	if err != nil {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCheckGuildNameDouble), currentGuildGenericError)
	}
	if !available {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCheckGuildNameDouble), currentGuildDuplicateNameError)
	}
	operationID := ""
	if s.options.guildSoloCreate {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return err
		}
		operationID = hex.EncodeToString(raw[:])
	}
	session.guild.mu.Lock()
	session.guild.pendingName = name
	session.guild.nameChecked = true
	session.guild.promotion = ""
	session.guild.createCommitted = false
	session.guild.soloOperationID = operationID
	session.guild.mu.Unlock()
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketCheckGuildNameDouble), nil); err != nil {
		return err
	}
	state := currentGuildCreateAuthResetValue
	if s.options.guildSoloCreate {
		// Native state 1 enables the paid create button. No invitation or second
		// membership exists under this explicitly configured server policy.
		state = 1
	}
	return s.sendGameUpperSecondaryRaw(session, currentGuildCreateAuthStateOpcode, []byte{state})
}

func (s *Service) handleCurrentGuildPromoterCheck(session *gameSession, body []byte) error {
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	promotion, ok := currentGuildDSTRAtStart(body)
	if !ok || dnfguild.ValidatePromotion(promotion) != nil {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCheckGuildCreatePromoteMsg), currentGuildPromoterMissingError)
	}
	_, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		s.logGameEvent(session, "game-guild-promoter-check-rejected", "reason", "selected character", "error", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCheckGuildCreatePromoteMsg), currentGuildCreateStateError)
	}
	session.guild.mu.Lock()
	valid := session.guild.nameChecked && session.guild.createInvitation == nil
	if valid {
		session.guild.promotion = promotion
	}
	session.guild.mu.Unlock()
	if !valid {
		s.logGameEvent(session, "game-guild-promoter-check-rejected", "reason", "guild name not checked or invitation pending")
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketCheckGuildCreatePromoteMsg), currentGuildCreateStateError)
	}
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketCheckGuildCreatePromoteMsg), nil)
}

func (s *Service) rejectCurrentGuildCreatePermit(session *gameSession, founder string, reason string, cause error) error {
	fields := []any{"reason", reason}
	if founder != "" {
		fields = append(fields, "founder", founder)
	}
	if cause != nil {
		fields = append(fields, "error", cause)
	}
	s.logGameEvent(session, "game-guild-create-permit-rejected", fields...)
	return s.sendGameUpperFailure(
		session,
		uint16(dnfenum.CmdPacketRequestGuildCreatePermit),
		currentGuildCreatePermitReject,
	)
}

func (s *Service) handleCurrentGuildCreatePermit(session *gameSession, body []byte) error {
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	if s.options.guildSoloCreate {
		return s.rejectCurrentGuildCreatePermit(session, "", "solo creation does not use invitations", nil)
	}
	session.guild.mu.Lock()
	committed := session.guild.createCommitted
	session.guild.mu.Unlock()
	if committed {
		return s.sendGameUpperFailure(
			session,
			uint16(dnfenum.CmdPacketRequestGuildCreatePermit),
			currentGuildCreatePermitStale,
		)
	}
	if len(body) < 5 || int(body[0]) != s.channelAdvertiseID() || session.guild.createInvitation != nil || session.guild.incomingInvitation != nil {
		return s.rejectCurrentGuildCreatePermit(session, "", "request state", nil)
	}
	name, next, ok := parseCurrentGuildDSTR(body, 1)
	if !ok || next != len(body) || name == "" {
		return s.rejectCurrentGuildCreatePermit(session, "", "founder name", nil)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	creator, online := s.boundGameSessionCharacterSnapshot(session)
	var target boundGameSessionCharacter
	var character dnfrepo.CharacterRecord
	err := s.readCurrentGuild(ctx, func(repositories dnfrepo.Group, registry dnfguild.Registry) error {
		var err error
		character, err = selectedGuildCharacter(ctx, repositories, session)
		if err != nil || !online || character.AccountID != s.accountIDForSession(session) {
			return dnfguild.ErrCharacterNotFound
		}
		id, found, err := repositories.Character.FindIDByName(ctx, name)
		if err != nil || !found || id == character.CharacterID {
			return dnfguild.ErrCharacterNotFound
		}
		numericID, err := strconv.ParseUint(id, 10, 16)
		if err != nil {
			return dnfguild.ErrCharacterNotFound
		}
		target, found = s.onlineGameSessionCharacterSnapshot(uint16(numericID))
		if !found || target.session.guild.createInvitation != nil || target.session.guild.incomingInvitation != nil {
			return dnfguild.ErrCharacterNotFound
		}
		promoter, found, err := repositories.Character.Load(ctx, id)
		if err != nil || !found || promoter.AccountID != s.accountIDForSession(target.session) {
			return dnfguild.ErrCharacterNotFound
		}
		for _, memberID := range []string{character.CharacterID, id} {
			if _, _, found := registry.GuildForCharacter(memberID); found {
				return dnfguild.ErrAlreadyMember
			}
		}
		return nil
	})
	session.guild.mu.Lock()
	guildName, promotion := session.guild.pendingName, session.guild.promotion
	nameChecked := session.guild.nameChecked
	session.guild.mu.Unlock()
	switch {
	case err != nil:
		return s.rejectCurrentGuildCreatePermit(session, name, "founder lookup", err)
	case !nameChecked:
		return s.rejectCurrentGuildCreatePermit(session, name, "guild name not checked", nil)
	case promotion == "" || dnfguild.ValidatePromotion(promotion) != nil:
		return s.rejectCurrentGuildCreatePermit(session, name, "promotion message", nil)
	}
	var operation [16]byte
	if _, err := rand.Read(operation[:]); err != nil {
		return err
	}
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketRequestGuildCreatePermit), nil); err != nil {
		return err
	}
	invite := &currentGuildCreateInvitation{creator: creator, target: target, operationID: hex.EncodeToString(operation[:])}
	session.guild.createInvitation = invite
	target.session.guild.createInvitation = invite
	var writer packetWriter
	writeClientNameDstr(&writer, character.Name)
	writer.writeByte(byte(s.channelAdvertiseID()))
	writeClientNameDstr(&writer, guildName)
	writeClientNameDstr(&writer, promotion)
	if err := s.sendGameUpperSecondaryRaw(target.session, currentGuildCreateInviteOpcode, writer.bytes()); err != nil {
		return errors.Join(err, s.cancelCurrentGuildCreateInvitation(invite, currentGuildCreateAuthResetValue, false))
	}
	invite.timer = time.AfterFunc(time.Minute, func() { s.expireCurrentGuildCreateInvitation(invite) })
	return nil
}

func (s *Service) handleCurrentGuildCreatePermitReply(session *gameSession, body []byte) error {
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	invite := session.guild.createInvitation
	if len(body) != 1 || body[0] > 1 || invite == nil || invite.target.session != session || invite.accepted ||
		!s.boundGameSessionCharacterCurrent(invite.creator) || !s.boundGameSessionCharacterCurrent(invite.target) {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketReplyGuildCreatePermit), currentGuildCreateStateError)
	}
	if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketReplyGuildCreatePermit), nil); err != nil {
		return errors.Join(err, s.cancelCurrentGuildCreateInvitation(invite, currentGuildCreateAuthResetValue, false))
	}
	if body[0] == 0 {
		return s.cancelCurrentGuildCreateInvitation(invite, 0, false)
	}
	invite.accepted = true
	invite.timer.Stop()
	if err := s.sendGameUpperSecondaryRaw(invite.creator.session, currentGuildCreateAuthStateOpcode, []byte{1}); err != nil {
		return errors.Join(err, s.cancelCurrentGuildCreateInvitation(invite, currentGuildCreateAuthResetValue, true))
	}
	return nil
}

func (s *Service) handleCurrentGuildCreateCancel(session *gameSession, _ []byte) error {
	s.closeCurrentGuildCreateSession(session)
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketCancelGuildCreate), nil)
}
