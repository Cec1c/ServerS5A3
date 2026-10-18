package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/pvf"
)

const currentLuckyStarItemAction uint32 = 105

func resolveCurrentLuckyStarItem(items *pvfDungeonDropCatalog, itemID int64) (inventory.LuckyStarItemDefinition, error) {
	var result inventory.LuckyStarItemDefinition
	if items == nil || itemID <= 0 || itemID > math.MaxUint32 {
		return result, inventory.ErrLuckyStarItem
	}
	def, err := items.ResolveItem(uint32(itemID))
	if err != nil {
		return result, err
	}
	if def.Kind != dungeonDropItemStackable || def.ActionType != "[rent point]" || normalizeDungeonDropStackableType(def.StackableType) != "[waste]" {
		return result, inventory.ErrLuckyStarItem
	}
	text, err := items.source.ReadText(def.PVFPath)
	if err != nil {
		return result, err
	}
	doc, err := pvf.Parse(def.PVFPath, text)
	if err != nil {
		return result, err
	}
	action, _ := doc.Section("action type")
	packet, _ := doc.Int("use action packet")
	level, _ := doc.Int("minimum level")
	places := doc.Texts("action usable place")
	if len(action) != 2 || action[1].Kind != pvf.TokenInt || action[1].Int <= 0 || action[1].Int > math.MaxUint32 || packet != 1 || level < 0 || len(places) != 2 || places[0] != "[village]" || places[1] != "[seria room]" {
		return result, inventory.ErrLuckyStarItem
	}
	if conditions, _ := doc.Section("action usable condition"); len(conditions) != 0 {
		return result, inventory.ErrLuckyStarItem
	}
	catalog, err := parseCurrentRentalCatalog(items.source)
	if err != nil {
		return result, err
	}
	return inventory.LuckyStarItemDefinition{ItemID: itemID, PVFPath: def.PVFPath, Amount: uint32(action[1].Int), Limit: catalog.Limit, MinimumLevel: level, DailyDelete: def.DailyDeleteItem}, nil
}

// The current native 515 callback consumes one item for action 105 and has no
// action-specific payload reader. The authoritative 985 state supplies points.
func (s *Service) handleCurrentLuckyStarItemRequest(session *gameSession, request gameplayRequest) (bool, error) {
	body := request.Body
	if request.Opcode != 515 || len(body) < 11 || binary.LittleEndian.Uint32(body[7:11]) != currentLuckyStarItemAction {
		return false, nil
	}
	if !requireDefaultGameplayClassification(s, session, request, "game-lucky-star-item-class-rejected", "command_class_mismatch") {
		return true, nil
	}
	slot := int16(binary.LittleEndian.Uint16(body))
	fail := func(err error) (bool, error) {
		s.logGameEvent(session, "game-lucky-star-item-rejected", "reason", err)
		return true, s.sendGameUpperRawClass(session, 515, inventory.BuildStackableActionFailure(slot, body[2], byte(currentLuckyStarItemAction), 17), 1)
	}
	if request.Sequence == nil || len(body) != 19 || slot < 1 || body[2] != 0 || binary.LittleEndian.Uint32(body[3:7]) != 0 || binary.LittleEndian.Uint64(body[11:19]) != 0 {
		return fail(inventory.ErrLuckyStarItem)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return fail(inventory.ErrLuckyStarItem)
	}
	requestID, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return fail(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(inventory.ErrOwnerUnavailable)
	}
	owner, err := inventory.NewOwner(repos)
	if err != nil {
		return fail(err)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return fail(err)
	}
	defer release()
	if !session.townMu.TryLock() {
		return fail(fmt.Errorf("town transition in progress"))
	}
	result, err := func() (inventory.LuckyStarItemResult, error) {
		defer session.townMu.Unlock()
		revalidate := func() bool {
			if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || !session.townPositionSnapshot.PositionValid {
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
		return owner.UseLuckyStarItem(ctx, inventory.LuckyStarItemRequest{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character)), RequestID: requestID, Slot: slot, Resolve: func(id int64) (inventory.LuckyStarItemDefinition, error) {
			return resolveCurrentLuckyStarItem(items, id)
		}, Revalidate: revalidate}, s.gameplayNow())
	}()
	if err != nil {
		return fail(err)
	}
	ack := inventory.BuildStackableActionAck(slot, 0, currentLuckyStarItemAction)
	if result.Replayed {
		// Do not apply the client's local decrement again, especially after slot reuse.
		ack = inventory.BuildStackableActionFailure(slot, 0, byte(currentLuckyStarItemAction), 0)
	}
	if err := s.sendGameUpperRawClass(session, 515, ack, 1); err != nil {
		return true, err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "lucky_star_item", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}); err != nil {
		return true, err
	}
	return true, s.sendSelectedCurrentRentalPointState(session, "lucky_star_item", true)
}
