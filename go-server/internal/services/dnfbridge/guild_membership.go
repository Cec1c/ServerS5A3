package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Lookup is advisory; the scoped transaction reloads ownership and the registry
// and the callback checks the session generation after the SQL locks are held.
func (s *Service) mutateCurrentGuildForSession(ctx context.Context, session *gameSession, target *dnfrepo.GuildOwner, apply func(dnfrepo.Group, *dnfguild.Registry, dnfrepo.CharacterRecord) error) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return dnfguild.ErrCharacterNotFound
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return err
	}
	defer release()
	owner := dnfrepo.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}
	owners := []dnfrepo.GuildOwner{owner}
	if target != nil && target.CharacterID != owner.CharacterID {
		owners = append(owners, *target)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfguild.ErrRepositoryUnavailable
	}
	return dnfguild.Update(ctx, repos.Guild, owners, s.gameplayNow(), func(tx dnfrepo.Group, r *dnfguild.Registry) error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return dnfguild.ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, owner.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return dnfguild.ErrCharacterNotFound
		}
		return apply(tx, r, actor)
	})
}

func buildCurrentGuildJoinCheckBody(g *dnfguild.Guild) []byte {
	var w packetWriter
	writeClientNameDstr(&w, g.Name)
	writeClientNameDstr(&w, g.MasterName)
	w.writeUint32(uint32(len(g.Members)))
	w.writeUint32(uint32(g.MemberLimit))
	w.writeByte(g.JoinMode)
	return w.bytes()
}

func buildCurrentGuildApplyBody(g *dnfguild.Guild) []byte {
	var w packetWriter
	w.writeUint32(g.ID)
	writeClientNameDstr(&w, g.Name)
	writeClientNameDstr(&w, g.MasterName)
	w.writeUint32(uint32(len(g.Members)))
	w.writeByte(g.JoinMode)
	return w.bytes()
}

func (s *Service) handleCurrentGuildJoinCheck(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketCheckJoinGuild)
	name, ok := currentGuildDSTRAtStart(body)
	if !ok {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	g, found := r.GuildByName(name)
	if !found {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	return s.sendGameUpperSuccess(session, op, buildCurrentGuildJoinCheckBody(g))
}

func (s *Service) handleCurrentGuildApply(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketRequestJoinGuild)
	name, next, ok := parseCurrentGuildDSTR(body, 0)
	message := ""
	if ok && next < len(body) {
		message, next, ok = parseCurrentGuildDSTR(body, next)
	}
	if !ok || next != len(body) || name == "" || dnfguild.ValidatePromotion(message) != nil {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var selected *dnfguild.Guild
	joined := false
	err := s.mutateCurrentGuildForSession(ctx, session, nil, func(_ dnfrepo.Group, r *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
		g, found := r.GuildByName(name)
		if !found {
			return dnfguild.ErrGuildNotFound
		}
		selected = g
		if current, _, member := r.GuildForCharacter(actor.CharacterID); member {
			if current.ID != g.ID {
				return dnfguild.ErrAlreadyMember
			}
			joined = true
			return nil // exact retry, no membership or money mutation
		}
		if g.JoinMode == 1 {
			_, err := r.JoinDirect(g.ID, actor, s.gameplayNow())
			joined = err == nil
			return err
		}
		if application, exists := g.Applications[actor.CharacterID]; exists {
			if application.AccountID == actor.AccountID && application.Message == message {
				return nil
			}
			return dnfguild.ErrApplicationExists
		}
		return r.Apply(g.ID, actor, message, s.gameplayNow())
	})
	if err != nil {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	// The native apply callback changes its own UI. ACK before membership.
	if err = s.sendGameUpperSuccess(session, op, buildCurrentGuildApplyBody(selected)); err != nil {
		return err
	}
	if joined {
		if err = s.refreshCurrentGuildMembership(ctx, session, selected); err != nil {
			return err
		}
		s.broadcastCurrentGuildMembers(selected, session.selectedCharacterID)
		return nil
	}
	return s.sendCurrentGuildAccountProjection(ctx, session)
}

func (s *Service) handleCurrentGuildCancelApply(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketCancelJoinGuild)
	if len(body) != 4 {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	id := binary.LittleEndian.Uint32(body)
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	err := s.mutateCurrentGuildForSession(ctx, session, nil, func(_ dnfrepo.Group, r *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
		return r.CancelApplication(id, actor.CharacterID, s.gameplayNow())
	})
	if err != nil {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	if err = s.sendGameUpperSuccess(session, op, nil); err != nil {
		return err
	}
	return s.sendCurrentGuildAccountProjection(ctx, session)
}

func (s *Service) handleCurrentGuildApplicationList(session *gameSession, _ []byte) error {
	op := uint16(dnfenum.CmdPacketGuildJoinList)
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	g, member, found := r.GuildForCharacter(strconv.Itoa(int(session.selectedCharacterID)))
	if !found || member.Grade > 2 {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	var rows []dnfguild.Application
	for _, a := range dnfguild.SortedApplications(g) {
		if _, _, exists := r.GuildForCharacter(a.CharacterID); exists {
			continue
		}
		c, found, err := repos.Character.Load(ctx, a.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.AccountID != a.AccountID {
			continue
		}
		a.Name, a.Job, a.Level, a.GrowType = c.Name, c.Job, c.Level, strconv.FormatInt(c.Stats["grow_type"], 10)
		rows = append(rows, a)
	}
	return s.sendGameUpperSuccess(session, op, buildCurrentGuildApplicationListBody(rows))
}

func (s *Service) decideCurrentGuildApplication(session *gameSession, body []byte, approve bool, op uint16) error {
	expected := 8
	if approve {
		expected = 5
	}
	if len(body) != expected {
		return s.rejectCurrentGuildRequest(session, op, body)
	}
	id := binary.LittleEndian.Uint32(body)
	region := uint32(body[4])
	if !approve {
		region = binary.LittleEndian.Uint32(body[4:])
	}
	if id == 0 || id > 0xffff || region >= 250 {
		return s.rejectCurrentGuildRequest(session, op, body)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, _, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	targetID := strconv.FormatUint(uint64(id), 10)
	target, found, err := repos.Character.Load(ctx, targetID)
	if err != nil {
		return err
	}
	if !found {
		return s.rejectCurrentGuildRequest(session, op, body)
	}
	owner := dnfrepo.GuildOwner{AccountID: target.AccountID, CharacterID: targetID}
	var selected *dnfguild.Guild
	err = s.mutateCurrentGuildForSession(ctx, session, &owner, func(tx dnfrepo.Group, r *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
		g, member, found := r.GuildForCharacter(actor.CharacterID)
		if !found || member.Grade > 2 {
			return dnfguild.ErrPermissionDenied
		}
		application, found := g.Applications[targetID]
		if !found || application.AccountID != owner.AccountID || uint32(application.ServerGroup) != region {
			return dnfguild.ErrApplicationNotFound
		}
		c, found, err := tx.Character.Load(ctx, targetID)
		if err != nil {
			return err
		}
		if !found {
			return dnfguild.ErrCharacterNotFound
		}
		group, err := dnfguild.CharacterServerGroup(c)
		if err != nil || uint32(group) != region {
			return dnfguild.ErrCharacterNotFound
		}
		application.Name, application.Job, application.Level, application.GrowType = c.Name, c.Job, c.Level, strconv.FormatInt(c.Stats["grow_type"], 10)
		g.Applications[targetID] = application
		_, err = r.DecideApplication(g.ID, actor.CharacterID, targetID, approve, s.gameplayNow())
		selected = g
		return err
	})
	if err != nil {
		return s.rejectCurrentGuildRequest(session, op, body)
	}
	if err = s.sendGameUpperSuccess(session, op, body); err != nil {
		return err
	}
	if peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(id)); ok && s.accountIDForSession(peer.session) == owner.AccountID && s.boundGameSessionCharacterCurrent(peer) {
		if approve {
			err = s.refreshCurrentGuildMembership(ctx, peer.session, selected)
		} else {
			err = s.sendCurrentGuildAccountProjection(ctx, peer.session)
		}
		if err != nil {
			s.logGameEvent(peer.session, "game-guild-decision-refresh-failed", "error", err)
		}
	}
	if approve {
		s.broadcastCurrentGuildMembers(selected, 0)
	}
	return s.handleCurrentGuildApplicationList(session, nil)
}

// The matched op350 callback reads {u32 character, u8 region} on failure as
// well as success (RVA 0x01bddbf2). A bare error byte leaves it reading past
// the packet. Malformed requests still get a complete zero identity.
func (s *Service) rejectCurrentGuildRequest(session *gameSession, op uint16, body []byte) error {
	if op != uint16(dnfenum.CmdPacketApproveJoinGuild) {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	echo := make([]byte, 5)
	if len(body) == 5 {
		copy(echo, body)
	}
	return s.sendGameUpperRawClass(session, op, append([]byte{0, currentGuildGenericError}, echo...), 1)
}

func (s *Service) handleCurrentGuildApprove(session *gameSession, body []byte) error {
	return s.decideCurrentGuildApplication(session, body, true, uint16(dnfenum.CmdPacketApproveJoinGuild))
}

func (s *Service) handleCurrentGuildDeny(session *gameSession, body []byte) error {
	return s.decideCurrentGuildApplication(session, body, false, uint16(dnfenum.CmdPacketDenyJoinGuild))
}
