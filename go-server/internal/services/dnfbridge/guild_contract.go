package dnfbridge

import (
	"encoding/binary"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/inventory"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildContractOpcode uint16 = 1244

func resolveCurrentGuildContractReward(items *pvfDungeonDropCatalog, catalog guild.ContentCatalog) (alignedcmd.MagicBoxRewardItem, error) {
	if items == nil || catalog.ContractRewardItemID == 0 || catalog.ContractRewardCount != 1 {
		return alignedcmd.MagicBoxRewardItem{}, guild.ErrInvalidContent
	}
	def, err := items.ResolveItem(catalog.ContractRewardItemID)
	if err != nil {
		return alignedcmd.MagicBoxRewardItem{}, err
	}
	if !def.DailyDeleteItem || def.StackLimit != 1 {
		return alignedcmd.MagicBoxRewardItem{}, guild.ErrInvalidContent
	}
	reward, err := resolveCurrentMagicBoxRewardItem(items, items.source, int64(catalog.ContractRewardItemID))
	if err == nil && (reward.Kind != "stackable" || reward.TargetListType != r.MainInventoryListType) {
		err = guild.ErrInvalidContent
	}
	return reward, err
}

func (s *Service) handleCurrentGuildContract(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-contract-class-rejected", "command_class_mismatch") {
		return nil
	}
	fail := func(err error) error {
		code := byte(14)
		if errors.Is(err, guild.ErrContractAlreadyClaimed) {
			code = 19
		}
		s.logGameEvent(session, "game-guild-contract-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildContractOpcode, code)
	}
	// V110 0x1196560 writes 17 bytes. Only its final u32 is initialized;
	// the first 13 are opaque stack bytes, not an owner, nonce or item ID.
	if request.Sequence == nil || len(request.Body) != 17 || binary.LittleEndian.Uint32(request.Body[13:]) != 21 {
		return fail(guild.ErrInvalidContent)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || source.generation == 0 {
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
	catalog, err := guild.LoadContentCatalog(items.source)
	if err != nil {
		return fail(err)
	}
	reward, err := resolveCurrentGuildContractReward(items, catalog)
	if err != nil {
		return fail(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(guild.ErrRepositoryUnavailable)
	}
	itemOwner, err := inventory.NewOwner(repos)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err != nil {
		return nil
	}
	defer release()
	if !session.townMu.TryLock() {
		return fail(guild.ErrCharacterNotFound)
	}
	claim, err := func() (guild.ContractClaimReceipt, error) {
		defer session.townMu.Unlock()
		revalidate := func() bool {
			if !s.boundGameSessionCharacterCurrent(source) || session.townSceneReadyCharacterID != source.character || !session.townPositionSnapshot.PositionValid {
				return false
			}
			if !session.dungeon.mu.TryLock() {
				return false
			}
			inDungeon := session.dungeon.runtime != nil
			session.dungeon.mu.Unlock()
			area, found := s.townArea(int64(session.townPositionSnapshot.TownID), int64(session.townPositionSnapshot.AreaID))
			return !inDungeon && found && area.MapPath != ""
		}
		return itemOwner.ClaimGuildContract(ctx, inventory.GuildContractClaim{
			Owner:     r.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(session)},
			RequestID: requestID, Catalog: catalog, Reward: reward, Revalidate: revalidate,
		}, s.gameplayNow())
	}()
	if err != nil {
		return fail(err)
	}
	if err := s.sendGameUpperSuccess(session, currentGuildContractOpcode, nil); err != nil {
		return err
	}
	refresh := make([]alignedcmd.ItemSlotRefresh, 0, len(claim.Slots))
	for _, slot := range claim.Slots {
		refresh = append(refresh, alignedcmd.ItemSlotRefresh{ListType: r.MainInventoryListType, SlotIndex: slot})
	}
	// A replay repairs the current bag view; it never restores a consumed box.
	return s.sendSelectedIncrementalItemSlotRefreshes(session, "guild_contract_claim", refresh)
}
