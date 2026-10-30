package dnfbridge

import (
	"slices"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildPlacementOpcode uint16 = 837

// The compatibility release retains placement state while denying new writes.
// Enable only after that release is the verified rollback baseline.
const currentGuildPlacementWritesDefault = true

func (s *Service) handleCurrentGuildPlacement(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-placement-class-rejected", "command_class_mismatch") {
		return nil
	}
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-placement-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildPlacementOpcode, 3)
	}
	if !s.options.guildEssenceEnabled || !s.options.guildPlacementWritesEnabled || request.Sequence == nil {
		return fail(guild.ErrPlacementIdentity)
	}
	q, err := parseCurrentGuildPlacementRequest(request.Body)
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
	book, ok := catalog.books[q.item]
	if !ok || uint32(book.Width) != q.width || uint32(book.Height) != q.height || !slices.Equal(book.Mask, q.mask) {
		return fail(guild.ErrPlacementCatalog)
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
	result, err := func() (guild.PlacementReceipt, error) {
		defer session.townMu.Unlock()
		p := session.townPositionSnapshot
		area, ok := s.townArea(int64(p.TownID), int64(p.AreaID))
		if !ok || !townMapPathLooksLikeGuildHall(area.MapPath) {
			return guild.PlacementReceipt{}, guild.ErrPlacementPosition
		}
		m, err := loadCurrentGuildPlacementMap(items.source, area.MapPath)
		if err != nil {
			return guild.PlacementReceipt{}, err
		}
		if uint32(m.Area) != q.area {
			return guild.PlacementReceipt{}, guild.ErrPlacementPosition
		}
		return guild.ApplyPlacement(ctx, repos.Guild, guild.PlacementRequest{
			Owner: owner, GuildID: g.ID, RequestID: requestID, Slot: q.slot, X: int(q.x), Y: int(q.y), Book: book, Map: m, BuildSeconds: catalog.buildSeconds,
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
	// Schedule peers even when the initiating socket fails after commit.
	s.queueCurrentGuildEssenceRefresh(result.GuildID, identity.character)
	if err = s.sendGameUpperSuccess(session, currentGuildPlacementOpcode, nil); err != nil {
		return err
	}
	if err = s.sendSelectedIncrementalItemSlotRefreshes(session, "guild_placement", []alignedcmd.ItemSlotRefresh{{ListType: r.MainInventoryListType, SlotIndex: int16(result.Slot)}}); err != nil {
		return err
	}
	return s.writeCurrentGuildEssenceSnapshot(session)
}
