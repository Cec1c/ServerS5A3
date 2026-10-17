package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfitemgrade "longheng.io/server/internal/modules/dnf/itemgrade"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Material 14 is the runtime stackable.lst entry cash/candle_goldwax.stk.
// Routing is only a preview; the owner reloads and validates the real material
// under the character asset transaction. A grade adjust racing this preview
// must also reject material 14 in its own catalog validation.
func (s *Service) tryCurrentGoldenWax(ctx context.Context, session *gameSession, request currentResetItemAttrRequest, body []byte, repos dnfrepo.Group) (bool, error) {
	if repos.Inventory == nil || session == nil || session.selectedCharacterID == 0 {
		return false, nil
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	preview, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		return true, s.sendGameUpperFailure(session, currentResetItemAttrOpcode, 2)
	}
	if preview.Slots[fmt.Sprintf("0:%d", request.MaterialSlot)].ItemID != 14 {
		return false, nil
	}
	reject := func(reason error) (bool, error) {
		s.logGameEvent(session, "game-golden-wax-rejected", "target_slot", request.TargetSlot, "target_item_id", request.TargetItemID, "material_slot", request.MaterialSlot, "err", reason)
		return true, s.sendGameUpperFailure(session, currentResetItemAttrOpcode, 4)
	}
	if len(body) != 8 && len(body) != 10 || len(body) == 10 && binary.LittleEndian.Uint16(body[8:]) != 0 {
		return reject(dnfitemgrade.ErrWaxReseal)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject(err)
	}
	rules, err := loadCurrentGoldenWaxRules(catalog.source)
	if err != nil {
		return reject(err)
	}
	owner, err := dnfitemgrade.NewWaxOwner(repos, currentGoldenWaxCatalog{catalog}, rules)
	if err != nil {
		return reject(err)
	}
	result, err := owner.Reseal(ctx, dnfitemgrade.WaxCommand{
		AccountID: s.accountIDForSession(session), CharacterID: characterID, TargetSlot: request.TargetSlot, TargetItemID: uint32(request.TargetItemID), MaterialSlot: request.MaterialSlot, UpdatedAt: s.gameplayNow(),
		Project: func(slot int16, stack dnfrepo.ItemStack) ([]byte, bool, error) {
			entry := currentItemListEntryFromStack(0, slot, stack)
			return append([]byte(nil), entry.data[:]...), currentEquipmentRegenerationSourceSealed(stack), nil
		},
	})
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(session, "game-golden-wax-success", "char_id", characterID, "target_item_id", request.TargetItemID, "target_slot", result.TargetSlot, "material_slot", result.MaterialSlot, "cost", result.Cost, "remaining", result.MaterialRemaining, "reseal_count", result.ResealCount)
	var w packetWriter
	w.writeUint16(uint16(result.MaterialSlot))
	w.writeUint32(uint32(result.MaterialRemaining))
	w.writeUint16(1)
	if err = s.sendGameUpperSuccess(session, currentResetItemAttrOpcode, w.bytes()); err != nil {
		return true, err
	}
	entry := currentItemListEntryFromStack(0, result.TargetSlot, result.TargetStack)
	return true, s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentItemUpdateBody(0, []currentItemListEntry{entry}), 0)
}

type currentGoldenWaxCatalog struct{ catalog *pvfDungeonDropCatalog }

func (c currentGoldenWaxCatalog) ResolveWaxItem(id uint32) (dnfitemgrade.WaxDefinition, error) {
	if c.catalog == nil {
		return dnfitemgrade.WaxDefinition{}, errDungeonDropSourceRequired
	}
	definition, err := c.catalog.ResolveItem(id)
	if err != nil {
		return dnfitemgrade.WaxDefinition{}, err
	}
	rule := dnfitemgrade.WaxDefinition{Equipment: definition.Kind == dungeonDropItemEquipment, Resealable: strings.Trim(strings.TrimSpace(definition.AttachType), "`[]") == "sealing", Placement: currentPVFItemPlacementPolicy(definition)}
	if rule.Equipment {
		text, err := c.catalog.source.ReadText(definition.PVFPath)
		if err != nil {
			return dnfitemgrade.WaxDefinition{}, err
		}
		doc, err := dnfpvf.Parse(definition.PVFPath, text)
		if err != nil {
			return dnfitemgrade.WaxDefinition{}, err
		}
		rule.MinimumLevel, _ = doc.Int("minimum level")
		rule.Rarity, _ = doc.Int("rarity")
		if definition.UsablePeriodDays > 0 || !definition.ExpirationDate.IsZero() {
			rule.Resealable = false
		}
	} else {
		rule.GoldenWax = id == 14 && strings.EqualFold(strings.TrimPrefix(strings.ReplaceAll(definition.PVFPath, "\\", "/"), "stackable/"), "cash/candle_goldwax.stk")
	}
	return rule, nil
}
