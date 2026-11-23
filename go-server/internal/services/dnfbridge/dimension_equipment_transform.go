package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type currentDimensionTransformRequest struct {
	Slot       int16
	ItemID     uint32
	SourceSlot int16
}
type dimensionTransformChoice struct {
	ItemID uint32
	Weight int64
}
type dimensionTransformRule struct {
	Input  map[uint32]bool
	Output []dimensionTransformChoice
	Total  int64
}

var errDimensionTransform = errors.New("dimension equipment transform rejected")

const currentDimensionTransformOpcode = 880

// Paired native 23F2D90 writes u8 list, u16 equipment slot, u32 item ID,
// u16 consumable slot through 374F390/3C0/3F0. Only the main equipment bag
// is supported; worn targets cannot be replaced by an arbitrary equipment type.
func parseCurrentDimensionTransformRequest(body []byte) (currentDimensionTransformRequest, error) {
	if len(body) != 9 || body[0] != 0 {
		return currentDimensionTransformRequest{}, errDimensionTransform
	}
	r := currentDimensionTransformRequest{Slot: int16(binary.LittleEndian.Uint16(body[1:3])), ItemID: binary.LittleEndian.Uint32(body[3:7]), SourceSlot: int16(binary.LittleEndian.Uint16(body[7:9]))}
	if r.Slot < 9 || r.Slot > 64 || r.SourceSlot < 65 || r.SourceSlot > 120 || r.ItemID == 0 {
		return currentDimensionTransformRequest{}, errDimensionTransform
	}
	return r, nil
}
func loadCurrentDimensionTransformRule(items *pvfDungeonDropCatalog) (dimensionTransformRule, error) {
	reject := func() (dimensionTransformRule, error) { return dimensionTransformRule{}, errDimensionTransform }
	if items == nil || items.source == nil {
		return reject()
	}
	book, e := items.ResolveItem(10158122)
	if e != nil {
		return dimensionTransformRule{}, e
	}
	if book.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(book.StackableType) != "[waste]" {
		return reject()
	}
	text, e := items.source.ReadText(book.PVFPath)
	if e != nil {
		return dimensionTransformRule{}, e
	}
	d, e := pvf.Parse(book.PVFPath, text)
	if e != nil {
		return dimensionTransformRule{}, e
	}
	action, present, e := currentEquipmentRecipeCompoundQualificationTokens(d, "action type")
	if e != nil || !present || len(action) != 1 || action[0].Kind != pvf.TokenString || action[0].Value != "[item change]" {
		return reject()
	}
	in, present, e := currentEquipmentRecipeCompoundQualificationTokens(d, "transformation input")
	if e != nil || !present || len(in) < 2 || in[0].Kind != pvf.TokenInt || in[0].Int != 1 {
		return reject()
	}
	out, present, e := currentEquipmentRecipeCompoundQualificationTokens(d, "transformation output")
	if e != nil || !present || len(out) != 1+2*(len(in)-1) || out[0].Kind != pvf.TokenInt || out[0].Int != 1 {
		return reject()
	}
	r := dimensionTransformRule{Input: map[uint32]bool{}}
	for _, v := range in[1:] {
		if v.Kind != pvf.TokenInt || v.Int <= 0 || v.Int > 0xffffffff || r.Input[uint32(v.Int)] {
			return reject()
		}
		def, e := items.ResolveItem(uint32(v.Int))
		if e != nil {
			return dimensionTransformRule{}, e
		}
		p := currentPVFItemPlacementPolicy(def)
		if def.Kind != dungeonDropItemEquipment || p.ListType != 0 || p.Start != 9 || p.End != 64 || !def.ExpirationDate.IsZero() {
			return reject()
		}
		r.Input[uint32(v.Int)] = true
	}
	seen := map[uint32]bool{}
	for i := 1; i < len(out); i += 2 {
		id, w := out[i], out[i+1]
		if id.Kind != pvf.TokenInt || id.Int <= 0 || id.Int > 0xffffffff || !r.Input[uint32(id.Int)] || seen[uint32(id.Int)] || w.Kind != pvf.TokenInt || w.Int <= 0 || w.Int > 1000000 {
			return reject()
		}
		seen[uint32(id.Int)] = true
		r.Output = append(r.Output, dimensionTransformChoice{uint32(id.Int), w.Int})
		r.Total += w.Int
	}
	return r, nil
}
func (s *Service) commitCurrentDimensionTransform(ctx context.Context, se *gameSession, r currentDimensionTransformRequest) error {
	if s == nil || se == nil || se.selectedCharacterID == 0 || r.Slot < 9 || r.Slot > 64 || r.SourceSlot < 65 || r.SourceSlot > 120 {
		return errDimensionTransform
	}
	id := se.selectedCharacterID
	generation := se.characterGeneration
	account := strings.TrimSpace(s.accountIDForSession(se))
	key := strconv.FormatUint(uint64(id), 10)
	g, ok := s.repositoryGroup()
	if !ok || g.CharacterSettlement == nil || account == "" {
		return errDimensionTransform
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		return e
	}
	rule, e := loadCurrentDimensionTransformRule(items)
	if e != nil {
		return e
	}
	now := s.gameplayNow()
	return g.CharacterSettlement.WithinCharacterSettlement(ctx, key, func(tx repo.Group) error {
		if se.selectedCharacterID != id || se.characterGeneration != generation || strings.TrimSpace(s.accountIDForSession(se)) != account || tx.Character == nil || tx.Inventory == nil {
			return errDimensionTransform
		}
		ch, found, e := tx.Character.Load(ctx, key)
		if e != nil {
			return e
		}
		if !found || ch.AccountID != account {
			return errDimensionTransform
		}
		inv, found, e := tx.Inventory.Load(ctx, key)
		if e != nil {
			return e
		}
		if !found {
			return errDimensionTransform
		}
		inv = repo.CloneInventory(inv)
		targetKey, bookKey := currentDungeonPickupMainSlotKey(r.Slot), currentDungeonPickupMainSlotKey(r.SourceSlot)
		target, book := inv.Slots[targetKey], inv.Slots[bookKey]
		if target.ItemID != int64(r.ItemID) || !rule.Input[r.ItemID] || target.Count != 1 || book.ItemID != 10158122 || book.Count < 1 || currentNPCShopItemLocked(target) || currentNPCShopItemLocked(book) || currentDungeonDiscardStackExpired(target, now) || currentDungeonDiscardStackExpired(book, now) {
			return errDimensionTransform
		}
		bookDefinition, e := items.ResolveItem(10158122)
		if e != nil {
			return e
		}
		if e = validateCurrentCeraPackageSourceExpiration(book, bookDefinition, now); e != nil {
			return e
		}
		if len(target.RawEntry) != 0 && (len(target.RawEntry) != currentItemListEntryWireSize || binary.LittleEndian.Uint32(target.RawEntry[2:6]) != r.ItemID) {
			return errDimensionTransform
		}
		roll, e := rand.Int(rand.Reader, big.NewInt(rule.Total))
		if e != nil {
			return e
		}
		value := roll.Int64()
		var output uint32
		for _, choice := range rule.Output {
			if value < choice.Weight {
				output = choice.ItemID
				break
			}
			value -= choice.Weight
		}
		if output == 0 {
			return errDimensionTransform
		}
		def, e := items.ResolveItem(output)
		if e != nil {
			return e
		}
		// Build a new native instance; none of the old enhancement/enchant/quality/
		// dimension/emblem state is copied. This matches the PVF's explicit reset.
		fresh, e := buildCurrentDungeonPickupStack(r.Slot, def, 1)
		if e != nil {
			return e
		}
		fresh.Extra["source"] = "dimension_equipment_transform"
		book.Count--
		if book.Count == 0 {
			delete(inv.Slots, bookKey)
		} else {
			row := currentItemListEntryFromStack(0, r.SourceSlot, book)
			book.RawEntry = append([]byte(nil), row.data[:]...)
			inv.Slots[bookKey] = book
		}
		inv.Slots[targetKey] = fresh
		inv.UpdatedAt = now
		if se.selectedCharacterID != id || se.characterGeneration != generation || strings.TrimSpace(s.accountIDForSession(se)) != account {
			return errDimensionTransform
		}
		return repo.SaveInventoryFields(ctx, tx.Inventory, inv, repo.InventoryFieldSlots)
	})
}
func (s *Service) handleCurrentDimensionTransform(se *gameSession, body []byte) error {
	r, e := parseCurrentDimensionTransformRequest(body)
	if e == nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		e = s.commitCurrentDimensionTransform(ctx, se, r)
		cancel()
	}
	if e != nil {
		s.logGameEvent(se, "game-dimension-equipment-transform-rejected", "error", e)
		return s.sendGameUpperFailure(se, currentDimensionTransformOpcode, 0)
	}
	// 1FCB6F0 resolves the success list/slot to display the resulting item. Refresh
	// after commit and before ACK so it resolves the newly created equipment.
	if e = s.sendSelectedCurrentContainerListsRefresh(se, "dimension_equipment_transform"); e != nil {
		return e
	}
	ack := []byte{1, 0, 0, 0}
	binary.LittleEndian.PutUint16(ack[2:], uint16(r.Slot))
	s.logGameEvent(se, "game-dimension-equipment-transform-committed", "slot", r.Slot, "source_slot", r.SourceSlot, "item", fmt.Sprint(r.ItemID))
	return s.sendGameUpperRawClass(se, currentDimensionTransformOpcode, ack, proto.DefaultChannelClassification)
}
