package dnfbridge

import (
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentGuildPlacementRemoveOpcode uint16 = 838
	currentGuildPlacementMoveOpcode   uint16 = 839
)

func parseCurrentGuildPlacementChange(body []byte, opcode uint16) (guild.PlacementChange, error) {
	var q guild.PlacementChange
	if len(body) != 33 {
		return q, guild.ErrPlacementIdentity
	}
	q.ID = binary.LittleEndian.Uint32(body[13:])
	q.ObjectID = binary.LittleEndian.Uint32(body[17:])
	x, y, area := binary.LittleEndian.Uint32(body[21:]), binary.LittleEndian.Uint32(body[25:]), binary.LittleEndian.Uint32(body[29:])
	if q.ID == 0 || q.ObjectID == 0 || x > 32767 || y > 32767 || area > 7 {
		return q, guild.ErrPlacementIdentity
	}
	q.X, q.Y, q.Area = int(x), int(y), uint8(area)
	switch opcode {
	case currentGuildPlacementRemoveOpcode:
		q.Action = guild.PlacementRemove
	case currentGuildPlacementMoveOpcode:
		q.Action = guild.PlacementMove
	default:
		return q, guild.ErrPlacementIdentity
	}
	return q, nil
}

func (s *Service) handleCurrentGuildPlacementChange(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-placement-change-class-rejected", "command_class_mismatch") {
		return nil
	}
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-placement-change-rejected", "error", err)
		return s.sendGameUpperFailure(session, request.Opcode, 3)
	}
	if !s.options.guildEssenceEnabled || request.Sequence == nil {
		return fail(guild.ErrPlacementIdentity)
	}
	change, err := parseCurrentGuildPlacementChange(request.Body, request.Opcode)
	if err != nil {
		return fail(err)
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || identity.generation == 0 {
		return fail(guild.ErrCharacterNotFound)
	}
	requestID, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return fail(err)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(err)
	}
	catalog, err := s.currentGuildPlacementCatalog(items)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(guild.ErrRepositoryUnavailable)
	}
	owner := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return fail(err)
	}
	g, _, ok := reg.GuildForOwner(owner)
	if !ok {
		return fail(guild.ErrNotMember)
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return nil
	}
	defer release()
	if !session.townMu.TryLock() {
		return fail(guild.ErrPlacementPosition)
	}
	result, err := func() (guild.PlacementChangeReceipt, error) {
		defer session.townMu.Unlock()
		p := session.townPositionSnapshot
		area, ok := s.townArea(int64(p.TownID), int64(p.AreaID))
		if !ok || !townMapPathLooksLikeGuildHall(area.MapPath) {
			return guild.PlacementChangeReceipt{}, guild.ErrPlacementPosition
		}
		m, err := loadCurrentGuildPlacementMap(items.source, area.MapPath)
		if err != nil {
			return guild.PlacementChangeReceipt{}, err
		}
		return guild.ApplyPlacementChange(ctx, repos.Guild, guild.PlacementChangeRequest{
			Owner: owner, GuildID: g.ID, RequestID: requestID, Change: change, Books: catalog.books, Map: m,
			Revalidate: func() bool {
				if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || session.townPositionSnapshot != p || p.CharacterID != identity.character || p.TownID != 8 || !p.PositionValid {
					return false
				}
				if !session.dungeon.mu.TryLock() {
					return false
				}
				inDungeon := session.dungeon.runtime != nil
				session.dungeon.mu.Unlock()
				return !inDungeon
			},
			CanInteract: func(actor r.CharacterRecord, _ *guild.Guild) bool {
				return actor.Stats["town_id"] == int64(p.TownID) && actor.Stats["area_id"] == int64(p.AreaID)
			},
		}, s.gameplayNow())
	}()
	if err != nil {
		return fail(err)
	}
	s.queueCurrentGuildEssenceRefresh(result.GuildID, identity.character)
	if err = s.sendGameUpperSuccess(session, request.Opcode, nil); err != nil {
		return err
	}
	return s.writeCurrentGuildEssenceSnapshot(session)
}
