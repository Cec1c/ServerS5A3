package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentPermanentWeaponAction uint32 = 88

func eventItemGameplayModule() gameplayModuleDefinition {
	op := uint16(dnfenum.CmdPacketUpgradeInventory)
	handler := func(s *Service, p *gameSession, body []byte) error { return s.handleCurrentEventItem(p, op, body) }
	return gameplayModuleDefinition{Name: "event-container-item", LegacyHandlers: map[uint16]gameplayHandler{op: func(s *Service, p *gameSession, r gameplayRequest) error { return handler(s, p, r.Body) }}, UpperHandlers: map[uint16]gameplayHandler{op: defaultClassGameplayHandler("event-item-class-rejected", "op430_requires_class1", handler)}, LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{op: func(b []byte) []byte { return stripLegacyTransportTrailer(b, 2) }}}
}

// Matching native writers: op430=u16 slot; action88's two u32 arguments are
// target list and slot. They are not reserved zeros as in the quest coupons.
func parseCurrentEventItemRequest(op uint16, body []byte) (inventory.EventItemKind, int16, int16, error) {
	if (op == 430 || op == 160) && len(body) == 2 && binary.LittleEndian.Uint16(body) <= math.MaxInt16 {
		kind := inventory.EventInventoryExpansion
		if op == 160 {
			kind = inventory.EventSilverCargo
		}
		return kind, int16(binary.LittleEndian.Uint16(body)), 0, nil
	}
	if op == 515 && len(body) == 19 && body[2] == 0 && binary.LittleEndian.Uint32(body[3:7]) == 0 && binary.LittleEndian.Uint32(body[7:11]) == currentPermanentWeaponAction && binary.LittleEndian.Uint32(body[11:15]) == 0 && binary.LittleEndian.Uint16(body) <= math.MaxInt16 && binary.LittleEndian.Uint32(body[15:]) <= math.MaxInt16 {
		return inventory.EventPermanentWeapon, int16(binary.LittleEndian.Uint16(body)), int16(binary.LittleEndian.Uint32(body[15:])), nil
	}
	return 0, 0, 0, inventory.ErrEventItem
}

func (s *Service) handleCurrentPermanentWeapon(session *gameSession, body []byte) (bool, error) {
	if len(body) < 11 || binary.LittleEndian.Uint32(body[7:11]) != currentPermanentWeaponAction {
		return false, nil
	}
	return true, s.handleCurrentEventItem(session, 515, body)
}

func (s *Service) handleCurrentEventItem(p *gameSession, op uint16, body []byte) error {
	kind, slot, targetSlot, err := parseCurrentEventItemRequest(op, body)
	reject := func(reason any) error {
		s.logGameEvent(p, "game-event-item-rejected", "opcode", op, "reason", reason)
		if op == 515 && len(body) >= 3 {
			return s.sendGameUpperRawClass(p, op, inventory.BuildStackableActionFailure(int16(binary.LittleEndian.Uint16(body)), body[2], byte(currentPermanentWeaponAction), 17), 1)
		}
		return s.sendGameUpperFailure(p, op, 17)
	}
	if err != nil {
		return reject(err)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || identity.generation == 0 {
		return reject("unbound_character")
	}
	g, ok := s.repositoryGroup()
	if !ok || g.CeraShopAssets == nil {
		return reject("asset_transaction_unavailable")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	if !p.townMu.TryLock() {
		return reject("town_transition")
	}
	if !p.dungeon.mu.TryLock() {
		p.townMu.Unlock()
		return reject("dungeon_transition")
	}
	r, err := func() (inventory.EventItemResult, error) {
		defer p.dungeon.mu.Unlock()
		defer p.townMu.Unlock()
		area, found := s.townArea(int64(p.townPositionSnapshot.TownID), int64(p.townPositionSnapshot.AreaID))
		if !found || area.MapPath == "" || !p.townPositionSnapshot.PositionValid || p.townSceneReadyCharacterID != identity.character || p.dungeon.runtime != nil {
			return inventory.EventItemResult{}, fmt.Errorf("event item requires village or Seria room")
		}
		place := "[village]"
		if townMapPathLooksLikeSeria(area.MapPath) {
			place = "[seria room]"
		}
		now := s.gameplayNow()
		return inventory.ApplyEventItem(ctx, g, inventory.EventItemRequest{AccountID: s.accountIDForSession(p), CharacterID: strconv.Itoa(int(identity.character)), SourceSlot: slot, TargetSlot: targetSlot, Kind: kind, Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) && p.dungeon.runtime == nil }, Resolve: func(_ repo.CharacterRecord, source, target repo.ItemStack) (inventory.EventItemPolicy, repo.ItemStack, repo.ItemStack, error) {
			return currentEventItemPolicy(catalog, kind, source, target, now, place)
		}}, now)
	}()
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(p, "game-event-item-committed", "opcode", op, "source_item", r.SourceItemID, "source_slot", slot, "remaining", r.Remaining, "target_item", r.Target.ItemID, "target_slot", targetSlot, "main_expansion", r.MainExpansion, "cargo_slots", r.CargoSlots)
	switch kind {
	case inventory.EventPermanentWeapon:
		if err := s.sendGameUpperRawClass(p, op, inventory.BuildStackableActionAck(slot, 0, currentPermanentWeaponAction), 1); err != nil {
			return err
		}
		var removed currentItemListEntry
		removed.patchCore(targetSlot, math.MaxUint32, 0)
		if err := s.sendGameUpperRawClass(p, 14, buildCurrentItemUpdateBody(0, []currentItemListEntry{removed}), 0); err != nil {
			return err
		}
	case inventory.EventSilverCargo:
		result := currentBoosterCommitResult{SourceItemID: uint32(r.SourceItemID), SourceSlot: slot, SourceRemaining: uint32(r.Remaining)}
		if err := s.sendGameUpperSuccess(p, op, buildCurrentBoosterSuccessBody(result)); err != nil {
			return err
		}
	case inventory.EventInventoryExpansion:
		// No dedicated op430 callback is registered in the matched client.
		// Capacity and source removal are carried by the authoritative op13.
	}
	if r.CargoSlots != 0 {
		if err := s.sendCurrentCeraShopCargoRefresh(ctx, p, currentCeraShopCommitResult{PersonalCargoSlotCount: r.CargoSlots}); err != nil {
			return err
		}
	}
	if r.MainExpansion != 0 {
		b, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, p, 0)
		if !ok {
			return errCurrentBoosterOwnerUnavailable
		}
		return s.sendCurrentSceneItemList(p, 13, b)
	}
	slots := []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}
	if kind == inventory.EventPermanentWeapon {
		slots = append(slots, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: targetSlot})
	}
	return s.sendSelectedIncrementalItemSlotRefreshes(p, "event_item", slots)
}
