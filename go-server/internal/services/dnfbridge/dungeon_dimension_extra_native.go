package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The exact v141 client maps this PVF action to 156, registers its use
// callback, and sends a 19-byte op515 request after confirmation. Native
// mapper/registration/callback/sender replays are recorded in the protocol note.
const (
	currentDimensionGateExtraTicketAction   uint32 = 156
	currentDimensionGateExtraTicketBodySize        = 19
	currentDimensionGateExtraTicketFailure  byte   = 1
)

type currentDimensionGateExtraTicketDefinition struct {
	ItemID         int64
	Amount         int64
	MinimumLevel   int64
	Cooldown       time.Duration
	ExpiresAt      time.Time
	MaterialItemID int64
	MaterialCount  int64
}

// parseCurrentDimensionGateExtraTicketDefinition reads only the PVF fields
// that the existing dimension_gate_extra transaction needs. The request never
// supplies the grant amount, level, cooldown, expiration or material fee.
func parseCurrentDimensionGateExtraTicketDefinition(itemID int64, doc *dnfpvf.Document) (currentDimensionGateExtraTicketDefinition, error) {
	var definition currentDimensionGateExtraTicketDefinition
	if itemID <= 0 || doc == nil {
		return definition, fmt.Errorf("dimension extra ticket definition is missing")
	}
	stackType, _ := doc.Text("stackable type")
	if normalizeDungeonDropStackableType(stackType) != "[waste]" {
		return definition, fmt.Errorf("item=%d is not a waste stackable", itemID)
	}
	action, found := doc.Section("action type")
	if !found || len(action) != 2 || action[0].Kind != dnfpvf.TokenString ||
		!strings.EqualFold(strings.TrimSpace(action[0].Value), "[add dimension dungeon enter count]") ||
		action[1].Kind != dnfpvf.TokenInt || action[1].Int <= 0 {
		return definition, fmt.Errorf("item=%d does not declare a positive dimension entry action", itemID)
	}
	places := doc.Texts("action usable place")
	if len(places) != 2 || !dimensionExtraTicketPlacesAllow(places) {
		return definition, fmt.Errorf("item=%d usable places=%v, want [village] and [seria room]", itemID, places)
	}
	minimum, _ := doc.Int("minimum level")
	cooldownMS, found := doc.Int("cool time")
	if !found || cooldownMS < 0 || cooldownMS > int64(math.MaxInt64/time.Millisecond) {
		return definition, fmt.Errorf("item=%d has invalid cooldown", itemID)
	}
	expiresAt, err := parseCurrentPVFExpirationDate(doc)
	if err != nil {
		return definition, fmt.Errorf("item=%d expiration: %w", itemID, err)
	}
	materialID, materialCount := dnfdungeon.DimensionGateExtraMaterialRequirement(doc.Ints("need material"))
	if materialID < 0 || materialCount < 0 || (materialID > 0) != (materialCount > 0) {
		return definition, fmt.Errorf("item=%d has an invalid material pair", itemID)
	}
	definition = currentDimensionGateExtraTicketDefinition{
		ItemID:         itemID,
		Amount:         action[1].Int,
		MinimumLevel:   minimum,
		Cooldown:       time.Duration(cooldownMS) * time.Millisecond,
		ExpiresAt:      expiresAt,
		MaterialItemID: materialID,
		MaterialCount:  materialCount,
	}
	return definition, nil
}

func dimensionExtraTicketPlacesAllow(places []string) bool {
	village, seria := false, false
	for _, place := range places {
		switch strings.ToLower(strings.TrimSpace(place)) {
		case "[village]":
			village = true
		case "[seria room]":
			seria = true
		default:
			return false
		}
	}
	return village && seria
}

func (s *Service) currentDimensionGateExtraTicketSceneUsable(session *gameSession) bool {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return false
	}
	if !session.dungeon.mu.TryLock() {
		return false
	}
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		return false
	}
	session.townMu.Lock()
	ready := session.townSceneReadyCharacterID == session.selectedCharacterID
	position := session.townPositionSnapshot
	session.townMu.Unlock()
	if !ready || !position.PositionValid {
		return false
	}
	area, found := s.townArea(int64(position.TownID), int64(position.AreaID))
	return found && area.MapPath != ""
}

// handleCurrentDimensionGateExtraTicketUse is the single native entry for the
// PVF action [add dimension dungeon enter count]. It resolves the item and all
// grant parameters from the runtime PVF, then delegates the atomic ticket,
// optional material and cumulative allowance commit to
// dungeon.Owner.GrantDimensionGateExtraEntry.
func (s *Service) handleCurrentDimensionGateExtraTicketUse(session *gameSession, body []byte) (bool, error) {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" || len(body) < 11 || binary.LittleEndian.Uint32(body[7:11]) != currentDimensionGateExtraTicketAction {
		return false, nil
	}
	slot := int16(binary.LittleEndian.Uint16(body[0:2]))
	reject := func(reason any) (bool, error) {
		s.logGameEvent(session, "game-dimension-gate-extra-ticket-rejected",
			"msg_id", uint16(dnfenum.CmdPacketUseStackableAction),
			"char_id", session.selectedCharacterID,
			"slot", slot,
			"action_index", currentDimensionGateExtraTicketAction,
			"reason", reason)
		return true, s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction),
			dnfinventory.BuildStackableActionFailure(slot, body[2], byte(currentDimensionGateExtraTicketAction), currentDimensionGateExtraTicketFailure), 1)
	}
	if len(body) != currentDimensionGateExtraTicketBodySize || body[2] != dnfrepo.MainInventoryListType || slot < 0 ||
		binary.LittleEndian.Uint32(body[3:7]) != 0 ||
		binary.LittleEndian.Uint32(body[11:15]) != 0 ||
		binary.LittleEndian.Uint32(body[15:19]) != 0 {
		return reject("invalid_action_envelope")
	}
	if session.selectedCharacterID == 0 {
		return reject("unbound_character")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil || repos.Character == nil || repos.CharacterAssets == nil {
		return reject("repository_unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	inventory, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		return reject("inventory_unavailable")
	}
	stack, found := inventory.Slots["0:"+strconv.Itoa(int(slot))]
	if !found || stack.ItemID <= 0 || stack.Count <= 0 {
		return reject("source_slot_empty")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return reject("item_catalog_unavailable")
	}
	item, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil || item.Kind != dungeonDropItemStackable {
		return reject("item_definition_unavailable")
	}
	text, err := catalog.source.ReadText(item.PVFPath)
	if err != nil {
		return reject("item_pvf_unreadable")
	}
	document, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil {
		return reject("item_pvf_invalid")
	}
	ticket, err := parseCurrentDimensionGateExtraTicketDefinition(stack.ItemID, document)
	if err != nil {
		return reject("unsupported_dimension_extra_ticket")
	}
	base, source, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		return reject("dimension_limit_unavailable")
	}
	extraLimit, err := s.currentDimensionGateDailyExtraLimit(base)
	if err != nil || ticket.Amount > extraLimit {
		return reject("dimension_extra_limit_unavailable")
	}
	if !s.currentDimensionGateExtraTicketSceneUsable(session) {
		return reject("requires_village_or_seria_room")
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return reject("owner_unavailable")
	}
	now := s.gameplayNow().UTC()
	result, err := owner.GrantDimensionGateExtraEntry(ctx, dnfdungeon.DimensionGateExtraEntryCommand{
		CharacterID:    characterID,
		AccountID:      s.accountIDForSession(session),
		Slot:           slot,
		ItemID:         stack.ItemID,
		Count:          ticket.Amount,
		BaseLimit:      base,
		ExtraLimit:     extraLimit,
		MinimumLevel:   int(ticket.MinimumLevel),
		Cooldown:       ticket.Cooldown,
		ExpiresAt:      ticket.ExpiresAt,
		MaterialItemID: ticket.MaterialItemID,
		MaterialCount:  ticket.MaterialCount,
		Project: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			return stack, nil
		},
		UpdatedAt: now,
	})
	if err != nil {
		return reject("domain_rejected")
	}
	used, granted, total, allowanceErr := dnfdungeon.DimensionGateDailyAllowance(result.Character, now, base, extraLimit)
	s.logGameEvent(session, "game-dimension-gate-extra-ticket-used",
		"msg_id", uint16(dnfenum.CmdPacketUseStackableAction),
		"char_id", session.selectedCharacterID,
		"slot", slot,
		"item_id", stack.ItemID,
		"action_index", currentDimensionGateExtraTicketAction,
		"granted", result.Granted,
		"extra_total", granted,
		"used", used,
		"total", total,
		"remaining", total-used,
		"allowance_err", allowanceErr,
		"base_limit", base,
		"extra_limit", extraLimit,
		"limit_source", source,
		"pvf_path", item.PVFPath,
		"material_item_id", ticket.MaterialItemID,
		"material_count", ticket.MaterialCount)
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction),
		dnfinventory.BuildStackableActionAck(slot, dnfrepo.MainInventoryListType, currentDimensionGateExtraTicketAction), 1); err != nil {
		return true, err
	}
	refreshes := []alignedcmd.ItemSlotRefresh{{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot}}
	for _, material := range result.MaterialSlots {
		refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: material.Slot})
	}
	return true, s.sendSelectedIncrementalItemSlotRefreshes(session, "dimension_gate_extra_ticket_after_ack", refreshes)
}
