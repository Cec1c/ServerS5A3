package dnfbridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const privateGrowthOpcode uint16 = 0x7f20
const privateGrowthResponseSize = 48 + 56*96

type privateGrowthPending struct {
	identity boundGameSessionCharacter
	nonce    [16]byte
	until    time.Time
	slot     int16
	ticket   repo.ItemStack
	targets  map[int16]repo.ItemStack
}

func (s *Service) initializePrivateGrowthGrant(stack *repo.ItemStack) error {
	_, _, ok := inventory.GrowthTicketType(stack.ItemID)
	if !ok {
		return nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	def, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return err
	}
	if def.Kind != dungeonDropItemEquipment || strings.Trim(def.AttachType, "[] ") != "account" || !strings.HasPrefix(def.PVFPath, "equipment/private_seria_growth/") {
		return inventory.ErrGrowthTicketInvalid
	}
	return inventory.InitializeGrowthTicket(stack, 0)
}

func (s *Service) growthTargetDefinition(ticket, target repo.ItemStack) (dungeonDropItemDefinition, uint16, error) {
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return dungeonDropItemDefinition{}, 0, err
	}
	def, err := catalog.ResolveItem(uint32(target.ItemID))
	if err != nil {
		return def, 0, err
	}
	mode, attr, ok := inventory.GrowthTicketMode(ticket)
	placement, valid := currentEquipmentPlacementRuleForPVFType(def.EquipmentType)
	_, _, isGrowth := inventory.GrowthTicketType(target.ItemID)
	if !ok || isGrowth || def.Kind != dungeonDropItemEquipment || !valid || placement.targetSlot < 12 || placement.targetSlot > 25 || placement.targetSlot == 13 {
		return def, 0, inventory.ErrGrowthTicketInvalid
	}
	for _, v := range def.ImpossibleContents {
		v = strings.Trim(v, "`[] ")
		if v == "upgrade" || (mode == "amplify" && v == "amplify upgrade") {
			return def, 0, inventory.ErrGrowthTicketInvalid
		}
	}
	var value uint16
	if attr != 0 {
		if def.MinimumLevel < 55 || def.Rarity < 2 {
			return def, 0, inventory.ErrGrowthTicketInvalid
		}
		config, e := currentAmplifyItemConfigFromSource(catalog.source)
		if e != nil {
			return def, 0, e
		}
		values, e := currentAmplifyInitialValues(config, def.Rarity)
		if e != nil {
			return def, 0, e
		}
		value = values[attr]
	}
	return def, value, nil
}

func (s *Service) handleGrowthTicketOpenMove(p *gameSession, classification byte, opcode uint16, body []byte) (bool, error) {
	if opcode != uint16(dnfenum.CmdPacketMoveItemspace) || classification != 1 || p == nil {
		return false, nil
	}
	q, err := inventory.DecodeMoveItemspaceRequest(body)
	if err != nil || q.SourceListType != 0 || (q.DestinationListType != 3 && q.DestinationListType != 17) {
		return false, nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound {
		return false, nil
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Inventory == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := g.Inventory.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found {
		return false, err
	}
	stack, exists := record.Slots[currentCeraShopInventorySlotKey(0, q.SourceSlotIndex)]
	if _, _, growth := inventory.GrowthTicketType(stack.ItemID); !exists || !growth {
		return false, nil
	}
	// Reuse the verified zero-move acknowledgement to finish the native request
	// without a wear or a failure popup; the paired selector owns the use flow.
	if err := s.sendGameUpperRawClass(p, opcode, inventory.GrowthTicketOpenMoveAck(q), 1); err != nil {
		return true, err
	}
	return true, s.openPrivateGrowth(ctx, p, identity, q.SourceSlotIndex, stack, record)
}

func (s *Service) openPrivateGrowth(ctx context.Context, p *gameSession, identity boundGameSessionCharacter, slot int16, ticket repo.ItemStack, record repo.InventoryRecord) error {
	p.growthTicketPending = nil
	b := make([]byte, privateGrowthResponseSize)
	copy(b, "GTS1")
	binary.LittleEndian.PutUint16(b[4:], 1)
	binary.LittleEndian.PutUint16(b[8:], identity.character)
	fail := func(code uint16) error {
		binary.LittleEndian.PutUint16(b[6:], code)
		return s.sendGameUpperRawClass(p, privateGrowthOpcode, b, 0)
	}
	if currentDungeonSceneActive(p) || !s.boundGameSessionCharacterCurrent(identity) || slot < 9 || slot > 64 || !inventory.GrowthTicketUsable(ticket, s.gameplayNow()) || inventory.GrowthTicketLevel(ticket) == 0 {
		return fail(1)
	}
	pending := &privateGrowthPending{identity: identity, until: s.gameplayNow().Add(2 * time.Minute), slot: slot, ticket: cloneOnlineItemTradeStack(ticket), targets: map[int16]repo.ItemStack{}}
	if _, err := rand.Read(pending.nonce[:]); err != nil {
		return err
	}
	copy(b[16:], pending.nonce[:])
	binary.LittleEndian.PutUint32(b[32:], uint32(ticket.ItemID))
	binary.LittleEndian.PutUint16(b[36:], uint16(slot))
	b[38] = inventory.GrowthTicketLevel(ticket)
	_, b[39], _ = inventory.GrowthTicketMode(ticket)
	count := 0
	for slot := int16(9); slot <= 64; slot++ {
		target, found := record.Slots[currentCeraShopInventorySlotKey(0, slot)]
		if !found || !inventory.GrowthTicketTargetUsable(ticket, target, s.gameplayNow()) {
			continue
		}
		def, _, err := s.growthTargetDefinition(ticket, target)
		if err != nil {
			continue
		}
		pending.targets[slot] = cloneOnlineItemTradeStack(target)
		o := 48 + count*96
		binary.LittleEndian.PutUint16(b[o:], uint16(slot))
		b[o+2] = inventory.GrowthTicketLevel(target)
		binary.LittleEndian.PutUint32(b[o+4:], uint32(target.ItemID))
		name := utf16.Encode([]rune(def.Name))
		if len(name) > 43 {
			name = name[:43]
			if name[42] >= 0xd800 && name[42] <= 0xdbff {
				name = name[:42]
			}
		}
		for j, c := range name {
			binary.LittleEndian.PutUint16(b[o+8+j*2:], c)
		}
		count++
	}
	binary.LittleEndian.PutUint16(b[10:], uint16(count))
	p.growthTicketPending = pending
	return s.sendGameUpperRawClass(p, privateGrowthOpcode, b, 0)
}

func (s *Service) handlePrivateGrowth(p *gameSession, classification byte, body []byte) error {
	if p == nil || classification != 1 || len(body) != 32 || string(body[:4]) != "GTQ1" || binary.LittleEndian.Uint16(body[4:]) != 1 || binary.LittleEndian.Uint16(body[6:]) != 1 {
		return nil
	}
	for _, v := range body[10:16] {
		if v != 0 {
			return nil
		}
	}
	pending := p.growthTicketPending
	if pending == nil || subtle.ConstantTimeCompare(body[16:], pending.nonce[:]) != 1 {
		return nil
	}
	p.growthTicketPending = nil // one confirmation per preview, including failures
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	status := uint16(2)
	if !bound || identity != pending.identity || !s.boundGameSessionCharacterCurrent(identity) || !s.gameplayNow().Before(pending.until) || currentDungeonSceneActive(p) {
		return s.growthResult(p, status)
	}
	slot := int16(binary.LittleEndian.Uint16(body[8:]))
	target, ok := pending.targets[slot]
	if !ok {
		return s.growthResult(p, status)
	}
	_, value, err := s.growthTargetDefinition(pending.ticket, target)
	if err != nil {
		return s.growthResult(p, status)
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return s.growthResult(p, status)
	}
	owner, err := inventory.NewOwner(g)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	out, err := owner.ApplyGrowthTicket(ctx, inventory.GrowthTicketApply{CharacterID: identity.character, TicketSlot: pending.slot, TargetSlot: slot, Ticket: pending.ticket, Target: target, TargetAllowed: true, TargetAmplifyValue: value, Now: s.gameplayNow()})
	if err != nil {
		s.logGameEvent(p, "private-growth-use-rejected", "reason", err)
		return s.growthResult(p, status)
	}
	// Native list-0 op14 removes an object only for item ID -1. ID 0 is an
	// in-place empty update and leaves a +0 ghost until character re-selection.
	var consumed currentItemListEntry
	consumed.patchCore(pending.slot, math.MaxUint32, 0)
	updates := []currentItemListEntry{consumed, currentItemListEntryFromStack(0, slot, out)}
	if err = s.sendGameUpperRawClass(p, 14, buildCurrentItemUpdateBody(0, updates), 0); err != nil {
		return err
	}
	return s.growthResult(p, 3)
}
func (s *Service) growthResult(p *gameSession, status uint16) error {
	b := make([]byte, privateGrowthResponseSize)
	copy(b, "GTS1")
	binary.LittleEndian.PutUint16(b[4:], 1)
	binary.LittleEndian.PutUint16(b[6:], status)
	if p != nil {
		binary.LittleEndian.PutUint16(b[8:], p.selectedCharacterID)
	}
	return s.sendGameUpperRawClass(p, privateGrowthOpcode, b, 0)
}
