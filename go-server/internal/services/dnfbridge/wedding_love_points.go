package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

const currentWeddingLovePointItemOpcode uint16 = 1037

// 24EB450 validates married state and available headroom, then 24EB557 sends
// opcode 1037 with 17B. Only tail[13:17] is the owned personal-inventory slot;
// the native request contains no item code or per-instance token.
func decodeCurrentWeddingLovePointItem(body []byte) (uint32, bool) {
	if len(body) != 17 {
		return 0, false
	}
	slot := binary.LittleEndian.Uint32(body[13:17])
	return slot, slot >= 3 && slot <= 288
}

type currentWeddingLovePointUseState struct {
	identity boundGameSessionCharacter
	sequence uint64
	slot     uint32
	semantic int32
	itemID   int64
}

func currentWeddingLovePointItemHandler(s *Service, session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-wedding-love-point-class-rejected", "command_class_mismatch") {
		return nil
	}
	return s.handleCurrentWeddingLovePointItem(session, request)
}

func (s *Service) handleCurrentWeddingLovePointItem(session *gameSession, request gameplayRequest) error {
	fail := func(result int32) error {
		return s.sendGameUpperRawClass(session, currentWeddingLovePointItemOpcode, weddingResultACK(result), 1)
	}
	slot, valid := decodeCurrentWeddingLovePointItem(request.Body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !valid || !bound || request.Sequence == nil {
		return fail(-5)
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	if s.wedding.lovePointUses == nil {
		s.wedding.lovePointUses = make(map[uint16]*currentWeddingLovePointUseState)
	}
	if previous := s.wedding.lovePointUses[actor.character]; previous != nil && previous.identity == actor {
		if *request.Sequence < previous.sequence || *request.Sequence == previous.sequence && previous.slot != slot {
			return fail(-5)
		}
		if *request.Sequence == previous.sequence {
			if previous.semantic != 0 {
				return fail(previous.semantic)
			}
			return s.finishCurrentWeddingLovePointUse(actor, slot)
		}
	}
	// Failed attempts are fenced as well: changing the inventory or balance
	// later cannot turn a replay of a rejected click into a new consumption.
	receipt := &currentWeddingLovePointUseState{identity: actor, sequence: *request.Sequence, slot: slot, semantic: -5}
	s.wedding.lovePointUses[actor.character] = receipt
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	req, actors, err := s.currentWeddingPartnerRequest(ctx, actor)
	if err != nil {
		return fail(-5)
	}
	if s.onlinePlayers == nil || isPVPChannel(session.channel) {
		return fail(-5)
	}
	place, present := s.onlinePlayers.PlayerForCharacter(actor.character)
	if !present || place.Session != session {
		return fail(-5)
	}
	area, known := s.townArea(int64(place.TownID), int64(place.AreaID))
	if !known {
		return fail(-5)
	}
	seria := townMapPathLooksLikeSeria(area.MapPath)
	revalidate := req.Revalidate
	req.Revalidate = func() bool {
		if session.loginRevoked.Load() || !revalidate() {
			return false
		}
		current, found := s.onlinePlayers.PlayerForCharacter(actor.character)
		if !found || current.Session != session || current.ChannelID != place.ChannelID || current.TownID != place.TownID || current.AreaID != place.AreaID || current.Instance != place.Instance {
			return false
		}
		if !session.dungeon.mu.TryLock() {
			return false
		}
		inDungeon := session.dungeon.runtime != nil
		session.dungeon.mu.Unlock()
		return !inDungeon
	}
	if !req.Revalidate() {
		return fail(-5)
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, actors)
	if err != nil {
		return fail(-5)
	}
	result, err := func() (wedding.LovePointUseResult, error) {
		owner, err := s.currentWeddingOwner()
		if err != nil {
			return wedding.LovePointUseResult{}, err
		}
		if owner.Repositories.Inventory == nil {
			return wedding.LovePointUseResult{}, wedding.ErrUnavailable
		}
		inventory, found, err := owner.Repositories.Inventory.Load(ctx, req.Actor.CharacterID)
		if err != nil {
			return wedding.LovePointUseResult{}, err
		}
		if !found || inventory.CharacterID != req.Actor.CharacterID {
			return wedding.LovePointUseResult{}, wedding.ErrOwnership
		}
		stack, found := inventory.Slots[strconv.Itoa(int(r.MainInventoryListType))+":"+strconv.FormatUint(uint64(slot), 10)]
		if !found {
			return wedding.LovePointUseResult{}, wedding.ErrItem
		}
		coupon, known := owner.Rules.LovePointItems[stack.ItemID]
		if !known {
			return wedding.LovePointUseResult{}, wedding.ErrItem
		}
		catalog, err := s.currentPVFItemCatalog()
		if err != nil {
			return wedding.LovePointUseResult{}, err
		}
		definition, err := catalog.ResolveItem(uint32(coupon.ItemID))
		if err != nil {
			return wedding.LovePointUseResult{}, err
		}
		if definition.Kind != dungeonDropItemStackable || strings.Trim(definition.StackableType, "[] ") != "waste" || strings.Trim(definition.ActionType, "[] ") != "love point coupon" || !strings.EqualFold(strings.ReplaceAll(definition.PVFPath, "\\", "/"), coupon.PVFPath) {
			return wedding.LovePointUseResult{}, wedding.ErrItem
		}
		nonce, err := session.guild.purchaseSequence.identity(0)
		if err != nil {
			return wedding.LovePointUseResult{}, err
		}
		return owner.ConsumeLovePointCoupon(ctx, req, wedding.LovePointUseRequest{Slot: int16(slot), ExpectedItemID: coupon.ItemID, Session: nonce, Sequence: *request.Sequence, Slots: currentPVFItemPlacementPolicy(definition), SeriaRoom: seria})
	}()
	release()
	if err != nil {
		s.logGameEvent(session, "game-wedding-love-point-item-rejected", "slot", slot, "error", err)
		// 1AD5360 maps -2 to DSTR72913 (心意点已达上限); -5 is
		// the native state/eligibility rejection branch (DSTR72844).
		if errors.Is(err, wedding.ErrLovePointLimit) {
			receipt.semantic = -2
		}
		return fail(receipt.semantic)
	}
	receipt.semantic, receipt.itemID = 0, result.ItemID
	return s.finishCurrentWeddingLovePointUse(actor, slot)
}

func (s *Service) finishCurrentWeddingLovePointUse(actor boundGameSessionCharacter, slot uint32) error {
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	if err := s.sendGameUpperRawClass(actor.session, currentWeddingLovePointItemOpcode, weddingResultACK(0), 1); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	character, err := s.currentWeddingCharacter(ctx, actor)
	if err != nil {
		return err
	}
	state, err := wedding.Read(character)
	if err != nil {
		return err
	}
	if err = s.sendCurrentWeddingLovePoints(actor.session, uint32(state.LovePoints)); err != nil {
		return err
	}
	return s.sendSelectedIncrementalItemSlotRefreshes(actor.session, "wedding_love_point_item_committed", []alignedcmd.ItemSlotRefresh{{ListType: r.MainInventoryListType, SlotIndex: int16(slot)}})
}
