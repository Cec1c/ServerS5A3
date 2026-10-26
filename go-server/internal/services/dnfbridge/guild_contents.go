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
	case errors.Is(err, guild.ErrPermanentBenefitOwned):
		return 9
	case errors.Is(err, guild.ErrInsufficientGold):
		return 10
	case errors.Is(err, guild.ErrContractMemberLimit):
		return 38
	case errors.Is(err, guild.ErrContractDuration):
		return 19
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
	if !found || (content.Type != 4 && content.Type != 6 && content.Type != 1 && content.Type != 3 && content.Type != 5 && content.Type != 7 && content.Type != 21) {
		s.logGameEvent(session, "game-guild-content-unavailable", "content_id", contentID)
		// The native 761 callback has no case for generic error 34: it silently
		// closes the confirmation. Error 14 is its visible purchase-failed path.
		return fail(14) // No success or charge for effects not implemented yet.
	}
	if content.Type == 21 {
		if _, err := resolveCurrentGuildContractReward(itemCatalog, catalog); err != nil {
			return fail(14)
		}
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
	query := guild.CargoPurchase{
		Owner:     r.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(session)},
		ContentID: contentID, RequestID: requestID, InitialCapacity: currentGuildCargoInitialCapacity,
		Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(source) },
	}
	var guildID uint32
	if content.Type == 4 || content.Type == 6 {
		purchase := guild.PurchaseCargo
		if content.Type == 6 {
			purchase = guild.PurchaseMemberExpansion
		}
		result, purchaseErr := purchase(ctx, repos.Guild, query, catalog, s.gameplayNow())
		guildID, err = result.GuildID, purchaseErr
	} else {
		result, purchaseErr := guild.PurchaseBenefit(ctx, repos.Guild, query, catalog, s.gameplayNow())
		guildID, err = result.GuildID, purchaseErr
	}
	release()
	sendErr := s.finishCurrentGuildEdit(ctx, source, guildID, currentGuildPurchaseOpcode, true, err, guildPurchaseError(err))
	if err == nil && content.Type == 4 {
		s.refreshCurrentGuildCargoPurchase(ctx, guildID, source)
	}
	if err == nil && sendErr == nil && content.Gold {
		// Receipts may be replayed after another wallet operation. Always read
		// the current actor balance under the same identity reservation.
		release, reserveErr := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
		if reserveErr != nil {
			return nil
		}
		defer release()
		actor, loadErr := s.currentGuildOwnedSelectedCharacter(session)
		if loadErr != nil {
			return loadErr
		}
		return s.sendCarryGoldWallet(session, actor.Stats["gold"])
	}
	return sendErr
}

// Refresh actual warehouse state only for members entitled to read it and
// currently in its real hall/NPC scene. Everyone receives the NOTI70 balance.
func (s *Service) refreshCurrentGuildCargoPurchase(ctx context.Context, id uint32, source boundGameSessionCharacter) {
	s.guildCargoMu.Lock()
	defer s.guildCargoMu.Unlock()
	s.refreshCurrentGuildCargoLocked(ctx, id, source)
}

func (s *Service) refreshCurrentGuildCargoLocked(ctx context.Context, id uint32, source boundGameSessionCharacter) {
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
		peer.session.guild.mu.Lock()
		pending := peer.session.guild.cargoPending != 0
		peer.session.guild.mu.Unlock()
		if pending && peer.session != source.session {
			continue // That request will refresh after its ACK, including rejection.
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
		if err := s.handleCurrentGuildCargoQueryLocked(peer.session, currentGuildCargoOpcode, nil, id); err != nil {
			s.logGameEvent(source.session, "game-guild-cargo-refresh-failed", "error", err)
		}
	}
}
