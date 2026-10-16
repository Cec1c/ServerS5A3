package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildPurchaseOpcode uint16 = 761

func guildPurchaseError(err error) byte {
	switch {
	case errors.Is(err, guild.ErrPermissionDenied):
		return 36
	case errors.Is(err, guild.ErrInsufficientMileage):
		return 22
	case errors.Is(err, guild.ErrCargoLimitReached):
		return 13
	case errors.Is(err, guild.ErrMemberExpansionPrerequisite):
		return 105
	case errors.Is(err, guild.ErrMemberExpansionUsed):
		return 116
	default:
		return 14
	}
}

func (s *Service) handleCurrentGuildPurchase(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-purchase-class-rejected", "command_class_mismatch") {
		return nil
	}
	fail := func(code byte) error { return s.sendGameUpperFailure(session, currentGuildPurchaseOpcode, code) }
	// Expansion uses the native empty DSTR confirmation. Other content types
	// may have text inputs and will get their own parser when implemented.
	if request.Sequence == nil || len(request.Body) != 8 || binary.LittleEndian.Uint32(request.Body[4:]) != 0 {
		return fail(14)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return fail(14)
	}
	itemCatalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(14)
	}
	catalog, err := guild.LoadContentCatalog(itemCatalog.source)
	if err != nil {
		return fail(14)
	}
	contentID := binary.LittleEndian.Uint32(request.Body)
	content, found := catalog.Contents[contentID]
	if !found || (content.Type != 4 && content.Type != 6) {
		s.logGameEvent(session, "game-guild-content-unavailable", "content_id", contentID)
		return fail(34) // No success or charge for effects not implemented yet.
	}
	requestID, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return fail(14)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err != nil {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		release()
		return fail(14)
	}
	purchase := guild.PurchaseCargo
	if content.Type == 6 {
		purchase = guild.PurchaseMemberExpansion
	}
	result, err := purchase(ctx, repos.Guild, guild.CargoPurchase{
		Owner:     r.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(session)},
		ContentID: contentID, RequestID: requestID, InitialCapacity: currentGuildCargoInitialCapacity,
		Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(source) },
	}, catalog, s.gameplayNow())
	release()
	sendErr := s.finishCurrentGuildEdit(ctx, source, result.GuildID, currentGuildPurchaseOpcode, true, err, guildPurchaseError(err))
	if err == nil && content.Type == 4 {
		s.refreshCurrentGuildCargoPurchase(ctx, result.GuildID, source)
	}
	return sendErr
}

// Refresh actual warehouse state only for members entitled to read it and
// currently in its real hall/NPC scene. Everyone receives the NOTI70 balance.
func (s *Service) refreshCurrentGuildCargoPurchase(ctx context.Context, id uint32, source boundGameSessionCharacter) {
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return
	}
	g, found := reg.GuildByID(id)
	if !found {
		return
	}
	for _, member := range guild.SortedMembers(g) {
		if guild.GradePermissionsFor(g, member.Grade)&guild.CargoPermission == 0 {
			continue
		}
		character, err := strconv.ParseUint(member.CharacterID, 10, 16)
		if err != nil || character == 0 {
			continue
		}
		peer, online := s.onlineGameSessionCharacterSnapshot(uint16(character))
		if !online {
			continue
		}
		if !peer.session.townMu.TryLock() {
			continue
		}
		position := peer.session.townPositionSnapshot
		ready := peer.session.townSceneReadyCharacterID == peer.character && position.PositionValid
		peer.session.townMu.Unlock()
		area, found := s.townArea(int64(position.TownID), int64(position.AreaID))
		if !ready || !found || !townMapPathLooksLikeGuildHall(area.MapPath) {
			continue
		}
		// The query rechecks the exact membership, permission, generation and
		// physical NPC and reads the latest committed capacity/items.
		if err := s.handleCurrentGuildCargoQueryForGuild(peer.session, currentGuildCargoOpcode, nil, id); err != nil {
			s.logGameEvent(source.session, "game-guild-cargo-refresh-failed", "error", err)
		}
	}
}
