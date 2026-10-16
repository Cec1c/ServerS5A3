package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentEquipmentEmancipateOpcode = uint16(dnfenum.CmdPacketUpgradeChronicle)

var errCurrentEquipmentEmancipate = errors.New("equipment conversion request or PVF rule is unavailable")

// Current EXE senders 0x1dc1b2b/0x1e8db36 write exactly 19 bytes. The reader
// 0x1fbbdd0 consumes a success byte, count and (u8 list,u16 slot,u32 consumed)
// rows. These ranges are byte-identical in the pinned v103 and v107 images.
// Byte 12 is opaque: conversion choices come exclusively from owned items/PVF.
type currentEquipmentEmancipateRequest struct {
	TicketSlot, TargetSlot, MaterialSlot int16
	TicketID, TargetID, MaterialID       uint32
}

func equipmentEmancipateGameplayModule() gameplayModuleDefinition {
	op := currentEquipmentEmancipateOpcode
	return gameplayModuleDefinition{Name: "equipment-emancipate", LegacyHandlers: map[uint16]gameplayHandler{op: func(s *Service, g *gameSession, r gameplayRequest) error {
		return s.handleCurrentEquipmentEmancipate(g, r.Body)
	}}, UpperHandlers: map[uint16]gameplayHandler{op: defaultClassGameplayHandler("equipment-conversion-class-rejected", "271 requires class1", (*Service).handleCurrentEquipmentEmancipate)}}
}

func parseCurrentEquipmentEmancipateRequest(body []byte) (currentEquipmentEmancipateRequest, error) {
	if len(body) != 19 {
		return currentEquipmentEmancipateRequest{}, errCurrentEquipmentEmancipate
	}
	r := currentEquipmentEmancipateRequest{TicketSlot: int16(binary.LittleEndian.Uint16(body)), TicketID: binary.LittleEndian.Uint32(body[2:]), TargetSlot: int16(binary.LittleEndian.Uint16(body[6:])), TargetID: binary.LittleEndian.Uint32(body[8:]), MaterialSlot: int16(binary.LittleEndian.Uint16(body[13:])), MaterialID: binary.LittleEndian.Uint32(body[15:])}
	return r, r.validate()
}

func (r currentEquipmentEmancipateRequest) validate() error {
	if r.TicketSlot < 3 || r.TargetSlot < 9 || r.TargetSlot > 64 || r.TicketSlot == r.TargetSlot || r.TicketID == 0 || r.TargetID == 0 || dnfrepo.IsAccountSharedInventorySlot(0, r.TicketSlot) {
		return errCurrentEquipmentEmancipate
	}
	// The second native sender emits 0xffff for both optional material fields.
	if r.MaterialSlot == -1 && r.MaterialID == math.MaxUint16 {
		return nil
	}
	if r.MaterialSlot < 3 || r.MaterialID == 0 || r.MaterialSlot == r.TargetSlot || r.MaterialSlot == r.TicketSlot {
		return errCurrentEquipmentEmancipate
	}
	return nil
}

func (c *pvfDungeonDropCatalog) currentEmancipateDocument(id uint32) (dungeonDropItemDefinition, *dnfpvf.Document, error) {
	d, err := c.ResolveItem(id)
	if err != nil {
		return d, nil, err
	}
	text, err := c.source.ReadText(d.PVFPath)
	if err != nil {
		return d, nil, err
	}
	doc, err := dnfpvf.Parse(d.PVFPath, text)
	return d, doc, err
}

func currentEmancipateInteger(doc *dnfpvf.Document, name string) (int64, error) {
	t, found, err := currentEquipmentRecipeCompoundQualificationTokens(doc, name)
	if err != nil || !found || len(t) != 1 || t[0].Kind != dnfpvf.TokenInt {
		return 0, errCurrentEquipmentEmancipate
	}
	return t[0].Int, nil
}

type currentEquipmentEmancipateRule struct {
	TicketType int64
	Materials  []dnfexpertjob.RecipeEntry
	OutputID   uint32
}

// Parse the nested block, not global [input]/[output] tags from another feature.
// Ticket 17/20 resource text explicitly discards reinforcement, amplification,
// forging, purification and enchantment. Heritage/capped tickets and growth
// are separate rules and remain rejected here, without consuming anything.
func parseCurrentEquipmentEmancipateRule(ticket, source *dnfpvf.Document) (currentEquipmentEmancipateRule, error) {
	typ, err := currentEmancipateInteger(ticket, "emancipate ticket")
	if err != nil || (typ != 17 && typ != 20) {
		return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
	}
	for _, name := range []string{"emancipate grade max", "emancipate amplify max", "emancipate genuineGrade max", "equipment level emancipate"} {
		if _, present, err := equipmentRecipePayloadTokens(ticket, name); err != nil || present {
			return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
		}
	}
	start, end := -1, -1
	for i, section := range source.Sections {
		switch strings.ToLower(section.Name) {
		case "emancipate":
			if start != -1 || section.Start != section.End {
				return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
			}
			start = i
		case "/emancipate":
			if end != -1 || section.Start != section.End {
				return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
			}
			end = i
		}
	}
	names := []string{"emancipate", "input", "/input", "output", "/output", "emancipate type", "/emancipate"}
	if start < 0 || end-start != len(names)-1 {
		return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
	}
	block := *source
	block.Sections = source.Sections[start : end+1]
	for i, name := range names {
		if !strings.EqualFold(block.Sections[i].Name, name) || (strings.HasPrefix(name, "/") && block.Sections[i].Start != block.Sections[i].End) {
			return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
		}
	}
	sourceType, err := currentEmancipateInteger(&block, "emancipate type")
	if err != nil || sourceType != typ {
		return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
	}
	input, _, err := equipmentRecipePayloadTokens(&block, "input")
	if err != nil || len(input) == 0 || len(input)%2 != 0 {
		return currentEquipmentEmancipateRule{}, errCurrentEquipmentEmancipate
	}
	rule := currentEquipmentEmancipateRule{TicketType: typ}
	counts := map[int64]int64{}
	for i := 0; i < len(input); i += 2 {
		id, n := input[i], input[i+1]
		if id.Kind != dnfpvf.TokenInt || n.Kind != dnfpvf.TokenInt || id.Int <= 0 || id.Int > math.MaxUint32 || n.Int <= 0 || n.Int > math.MaxUint32-counts[id.Int] {
			return rule, errCurrentEquipmentEmancipate
		}
		counts[id.Int] += n.Int
	}
	ids := make([]int64, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		rule.Materials = append(rule.Materials, dnfexpertjob.RecipeEntry{ItemID: id, Count: counts[id]})
	}
	output, _, err := equipmentRecipePayloadTokens(&block, "output")
	if err != nil || len(output) != 2 || output[0].Kind != dnfpvf.TokenInt || output[1].Kind != dnfpvf.TokenInt || output[0].Int <= 0 || output[0].Int > math.MaxUint32 || output[1].Int != 1 {
		return rule, errCurrentEquipmentEmancipate
	}
	rule.OutputID = uint32(output[0].Int)
	return rule, nil
}

type currentEquipmentEmancipateConsumption struct {
	Slot  int16
	Count uint32
}

type currentEquipmentEmancipateResult struct {
	Consumptions []currentEquipmentEmancipateConsumption
	Entries      []currentItemListEntry
}

func (s *Service) commitCurrentEquipmentEmancipate(ctx context.Context, g *gameSession, r currentEquipmentEmancipateRequest) (currentEquipmentEmancipateResult, error) {
	var result currentEquipmentEmancipateResult
	if s == nil || g == nil || g.selectedCharacterID == 0 || r.validate() != nil {
		return result, errCurrentEquipmentEmancipate
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil || items == nil || items.source == nil {
		return result, errCurrentEquipmentEmancipate
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return result, errCurrentEquipmentEmancipate
	}
	owner, err := dnfexpertjob.NewOwner(repos)
	if err != nil {
		return result, err
	}
	now := s.gameplayNow()
	err = owner.Compound(ctx, dnfexpertjob.Command{AccountID: s.accountIDForSession(g), CharacterID: strconv.Itoa(int(g.selectedCharacterID)), UpdatedAt: now, Project: func(a *dnfexpertjob.Assets) (dnfexpertjob.Changes, error) {
		result = currentEquipmentEmancipateResult{}
		ticketDef, ticketDoc, err := items.currentEmancipateDocument(r.TicketID)
		if err != nil || ticketDef.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(ticketDef.StackableType) != "[etc]" || !currentPVFItemPlacementPolicy(ticketDef).ForInventory(a.Inventory).Contains(0, r.TicketSlot) || !ticketDef.ExpirationDate.IsZero() {
			return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
		}
		sourceDef, sourceDoc, err := items.currentEmancipateDocument(r.TargetID)
		if err != nil || sourceDef.Kind != dungeonDropItemEquipment || !sourceDef.ExpirationDate.IsZero() || !currentPVFItemPlacementPolicy(sourceDef).ForInventory(a.Inventory).Contains(0, r.TargetSlot) {
			return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
		}
		rule, err := parseCurrentEquipmentEmancipateRule(ticketDoc, sourceDoc)
		if err != nil || rule.OutputID == r.TargetID {
			return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
		}
		ticketKey, targetKey := currentDungeonPickupMainSlotKey(r.TicketSlot), currentDungeonPickupMainSlotKey(r.TargetSlot)
		ticket, target := a.Inventory.Slots[ticketKey], a.Inventory.Slots[targetKey]
		for _, row := range []struct {
			stack dnfrepo.ItemStack
			id    uint32
		}{{ticket, r.TicketID}, {target, r.TargetID}} {
			if row.stack.ItemID != int64(row.id) || row.stack.Count <= 0 || currentNPCShopItemLocked(row.stack) || currentDungeonDiscardStackExpired(row.stack, now) || (len(row.stack.RawEntry) != 0 && (len(row.stack.RawEntry) != currentItemListEntryWireSize || binary.LittleEndian.Uint32(row.stack.RawEntry[2:6]) != row.id)) {
				return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
			}
		}
		if target.Count != 1 || !target.ExpireAt.IsZero() || currentItemListStackExpire(target) != 0 {
			return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
		}
		if rule.TicketType == 17 {
			switch normalizeDungeonDropStackableType(sourceDef.EquipmentType) {
			case "[support]", "[magic stone]":
				return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
			}
		}
		output, err := items.ResolveItem(rule.OutputID)
		if err != nil || output.Kind != dungeonDropItemEquipment || output.EquipmentType != sourceDef.EquipmentType || !output.ExpirationDate.IsZero() || !currentPVFItemPlacementPolicy(output).Contains(0, r.TargetSlot) {
			return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
		}
		converted, err := buildCurrentDungeonPickupStack(r.TargetSlot, output, 1)
		if err != nil {
			return dnfexpertjob.Changes{}, err
		}
		// The two supported books say the output cannot be traded. Bind governs
		// server trade admission; seal state is separate and survives conversion.
		seal := currentItemListEntryFromStack(0, r.TargetSlot, target).data[0x0d]
		converted.Bind = true
		converted.Extra["source"] = "equipment_emancipate"
		converted.Extra["emancipate_seal_flag"] = strconv.Itoa(int(seal))
		converted.Extra["seal_flag"] = strconv.Itoa(int(seal))
		entry := currentItemListEntryFromStack(0, r.TargetSlot, converted)
		converted.RawEntry = append([]byte(nil), entry.data[:]...)
		// Consume the exact selected book, never an arbitrary row of its ID.
		ticket.Count--
		if ticket.Count == 0 {
			delete(a.Inventory.Slots, ticketKey)
		} else {
			a.Inventory.Slots[ticketKey] = ticket
		}
		result.Consumptions = append(result.Consumptions, currentEquipmentEmancipateConsumption{r.TicketSlot, 1})
		accountDirty := false
		hintFound := r.MaterialSlot == -1
		for _, m := range rule.Materials {
			if m.ItemID == int64(r.TicketID) || m.ItemID == int64(r.TargetID) {
				return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
			}
			def, err := items.ResolveItem(uint32(m.ItemID))
			if err != nil || def.Kind != dungeonDropItemStackable || !def.ExpirationDate.IsZero() {
				return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
			}
			slots := a.Inventory.Slots
			policy := currentPVFItemPlacementPolicy(def).ForInventory(a.Inventory)
			fixed, shared := dnfrepo.AccountWarehouseSlotForItem(m.ItemID)
			if shared {
				slots = a.AccountInventory.Slots
				accountDirty = true
			}
			keys := make([]string, 0)
			for key, stack := range slots {
				list, slot, valid := parseSceneInventorySlotKey(key)
				if !valid || list != 0 || stack.ItemID != m.ItemID || stack.Count <= 0 || stack.Count > math.MaxUint32 || currentNPCShopItemLocked(stack) || currentDungeonDiscardStackExpired(stack, now) {
					continue
				}
				if (shared && slot != fixed) || (!shared && (dnfrepo.IsAccountSharedInventorySlot(list, slot) || !policy.Contains(list, slot))) {
					continue
				}
				if slot == r.MaterialSlot && uint32(m.ItemID) == r.MaterialID {
					hintFound = true
				}
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool {
				_, x, _ := parseSceneInventorySlotKey(keys[i])
				_, y, _ := parseSceneInventorySlotKey(keys[j])
				return x < y
			})
			remaining := m.Count
			for _, key := range keys {
				if remaining == 0 {
					break
				}
				stack := slots[key]
				n := min(stack.Count, remaining)
				stack.Count -= n
				remaining -= n
				_, slot, _ := parseSceneInventorySlotKey(key)
				result.Consumptions = append(result.Consumptions, currentEquipmentEmancipateConsumption{slot, uint32(n)})
				if stack.Count == 0 {
					delete(slots, key)
				} else {
					slots[key] = stack
				}
			}
			if remaining != 0 {
				return dnfexpertjob.Changes{}, errCurrentExpertJobInsufficientMaterials
			}
		}
		if !hintFound || len(result.Consumptions) > math.MaxUint8 {
			return dnfexpertjob.Changes{}, errCurrentEquipmentEmancipate
		}
		a.Inventory.Slots[targetKey] = converted
		result.Entries = append(result.Entries, currentItemListEntryFromStack(0, r.TargetSlot, converted))
		for _, consumed := range result.Consumptions {
			slots := a.Inventory.Slots
			if dnfrepo.IsAccountSharedInventorySlot(0, consumed.Slot) {
				slots = a.AccountInventory.Slots
			}
			if stack, exists := slots[currentDungeonPickupMainSlotKey(consumed.Slot)]; exists {
				result.Entries = append(result.Entries, currentItemListEntryFromStack(0, consumed.Slot, stack))
			} else {
				var empty currentItemListEntry
				empty.patchCore(consumed.Slot, math.MaxUint32, 0)
				result.Entries = append(result.Entries, empty)
			}
		}
		return dnfexpertjob.Changes{Inventory: true, AccountInventory: accountDirty}, nil
	}})
	if err != nil {
		return currentEquipmentEmancipateResult{}, err
	}
	return result, nil
}

func buildCurrentEquipmentEmancipateSuccess(result currentEquipmentEmancipateResult) []byte {
	body := make([]byte, 3+7*len(result.Consumptions))
	body[0], body[1], body[2] = 1, 1, byte(len(result.Consumptions))
	for i, row := range result.Consumptions {
		offset := 3 + 7*i
		binary.LittleEndian.PutUint16(body[offset+1:], uint16(row.Slot))
		binary.LittleEndian.PutUint32(body[offset+3:], row.Count)
	}
	return body
}

func (s *Service) handleCurrentEquipmentEmancipate(g *gameSession, body []byte) error {
	r, err := parseCurrentEquipmentEmancipateRequest(body)
	var result currentEquipmentEmancipateResult
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		result, err = s.commitCurrentEquipmentEmancipate(ctx, g, r)
	}
	if err != nil {
		s.logGameEvent(g, "equipment-conversion-rejected", "error", err)
		// Generic failure completes the native response path; do not invent
		// a material-specific error text from an unverified numeric code.
		return s.sendGameUpperFailure(g, currentEquipmentEmancipateOpcode, 0)
	}
	s.logGameEvent(g, "equipment-conversion-committed", "target_slot", r.TargetSlot, "target_id", r.TargetID, "consumption_rows", len(result.Consumptions))
	if err = s.sendGameUpperRawClass(g, currentEquipmentEmancipateOpcode, buildCurrentEquipmentEmancipateSuccess(result), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	var empty currentItemListEntry
	empty.patchCore(r.TargetSlot, math.MaxUint32, 0)
	if err = s.sendGameUpperRawClass(g, 14, buildCurrentItemUpdateBody(0, []currentItemListEntry{empty}), 0); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(g, 14, buildCurrentItemUpdateBody(0, result.Entries), 0)
}
