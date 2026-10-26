package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The current EXE's op25 sender (0x33C2840) checks the material count through
// 0x33C1EA0. It does not consult the making-level ledger on this transform path.
// Ordinary crafting retains its separate making-floor admission. A transform
// additionally requires one existing equipment instance and one PVF output.
func currentEquipmentRecipeUpgradeDocument(entry equipmentRecipeEntry, source dnfpvf.Source) (*dnfpvf.Document, error) {
	if source == nil || entry.Kind != equipmentRecipeKindMaterialOutput || entry.MaintainAbility != "normal" {
		return nil, errCurrentEquipmentRecipeCompoundUnsupported
	}
	text, err := source.ReadText(entry.PVFPath)
	if err != nil {
		return nil, err
	}
	document, err := dnfpvf.Parse(entry.PVFPath, text)
	if err != nil {
		return nil, err
	}
	ability, found, err := currentEquipmentRecipeCompoundQualificationTokens(document, "maintain ability")
	if err != nil || !found || len(ability) != 1 || ability[0].Kind != dnfpvf.TokenString || ability[0].Value != "normal" {
		return nil, fmt.Errorf("%w: maintain ability shape", errCurrentEquipmentRecipeCompoundUnsupported)
	}
	socket, found, err := currentEquipmentRecipeCompoundQualificationTokens(document, "maintain socket stat")
	if err != nil || found != entry.MaintainSocketStat || len(socket) != 0 {
		return nil, fmt.Errorf("%w: maintain socket shape", errCurrentEquipmentRecipeCompoundUnsupported)
	}
	minimum, found, err := currentEquipmentRecipeCompoundQualificationTokens(document, "minimum itemmaking level")
	if err != nil || found != entry.MinimumItemmakingFound {
		return nil, errCurrentEquipmentRecipeCompoundUnsupported
	}
	if found {
		if len(minimum) != 2 || minimum[0].Kind != dnfpvf.TokenString || minimum[1].Kind != dnfpvf.TokenInt ||
			minimum[0].Value != entry.MinimumItemmakingField || minimum[1].Int != entry.MinimumItemmakingLevel || minimum[1].Int <= 0 {
			return nil, errCurrentEquipmentRecipeCompoundUnsupported
		}
		switch minimum[0].Value {
		case "weapon", "cloth", "leather", "larmor", "harmor", "plate", "amulet", "wrist", "ring", "support", "magic stone":
		default:
			return nil, errCurrentEquipmentRecipeCompoundUnsupported
		}
	}
	shopPrice, err := currentHeblonOwnedUpgradeShopPrice(entry, document)
	if err != nil {
		return nil, err
	}
	plain := entry
	plain.MaintainAbility, plain.MinimumItemmakingField = "", ""
	plain.MaintainSocketStat, plain.MinimumItemmakingFound = false, false
	plain.MinimumItemmakingLevel = 0
	if shopPrice {
		// Only the validated owned Heblon drawing treats this field as its
		// purchase-domain price. The catalog and generic compound stay intact.
		plain.ExtraMaterials = nil
	}
	if reason := currentEquipmentRecipeCompoundRejectReason(plain, nil); reason != "" {
		return nil, fmt.Errorf("%w: %s", errCurrentEquipmentRecipeCompoundUnsupported, reason)
	}
	filtered := *document
	filtered.Sections = make([]dnfpvf.Section, 0, len(document.Sections))
	for _, section := range document.Sections {
		if shopPrice && strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(section.Name), "/"), "need material") {
			continue
		}
		switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(section.Name)), "/") {
		case "maintain ability", "maintain socket stat", "minimum itemmaking level":
			continue
		}
		filtered.Sections = append(filtered.Sections, section)
	}
	if reason := currentEquipmentRecipeCompoundDocumentRejectReason(&filtered); reason != "" {
		return nil, fmt.Errorf("%w: %s", errCurrentEquipmentRecipeCompoundUnsupported, reason)
	}
	return &filtered, nil
}

type currentEquipmentRecipeUpgradePlan struct {
	Key         string
	Slot        int16
	Stack       dnfrepo.ItemStack
	Output      dungeonDropItemDefinition
	KeepSockets bool
}

func planCurrentEquipmentRecipeUpgrade(entry equipmentRecipeEntry, items *pvfDungeonDropCatalog, inventory *dnfrepo.InventoryRecord, count int64) (*currentEquipmentRecipeUpgradePlan, error) {
	if count != 1 || inventory == nil || items == nil || len(entry.Materials) == 0 || len(entry.Outputs) != 1 || entry.Outputs[0].Count != 1 || entry.Materials[0].Count != 1 {
		return nil, errCurrentEquipmentRecipeCompoundUnsupported
	}
	for _, material := range entry.Materials {
		if material.ItemID <= 0 || material.ItemID > math.MaxUint32 {
			return nil, errCurrentEquipmentRecipeCompoundUnsupported
		}
	}
	if entry.Outputs[0].ItemID <= 0 || entry.Outputs[0].ItemID > math.MaxUint32 {
		return nil, errCurrentEquipmentRecipeCompoundUnsupported
	}
	input, err := items.ResolveItem(uint32(entry.Materials[0].ItemID))
	if err != nil || input.Kind != dungeonDropItemEquipment {
		return nil, errCurrentEquipmentRecipeCompoundUnsupported
	}
	output, err := items.ResolveItem(uint32(entry.Outputs[0].ItemID))
	if err != nil || output.Kind != dungeonDropItemEquipment || input.EquipmentType != output.EquipmentType ||
		currentPVFItemTargetContainer(input).ListType != dnfrepo.MainInventoryListType || currentPVFItemTargetContainer(output).ListType != dnfrepo.MainInventoryListType ||
		!input.ExpirationDate.IsZero() || !output.ExpirationDate.IsZero() {
		return nil, errCurrentEquipmentRecipeCompoundUnsupported
	}
	for _, material := range entry.Materials[1:] {
		definition, err := items.ResolveItem(uint32(material.ItemID))
		if err != nil || definition.Kind != dungeonDropItemStackable || material.ItemID == entry.Materials[0].ItemID {
			return nil, errCurrentEquipmentRecipeCompoundUnsupported
		}
	}
	slots := make([]int, 0)
	for key, stack := range inventory.Slots {
		list, slot, ok := parseSceneInventorySlotKey(key)
		if ok && list == dnfrepo.MainInventoryListType && slot >= 3 && !dnfrepo.IsAccountSharedInventorySlot(list, slot) && stack.ItemID == entry.Materials[0].ItemID && stack.Count == 1 && !currentNPCShopItemLocked(stack) {
			slots = append(slots, int(slot))
		}
	}
	sort.Ints(slots)
	if len(slots) == 0 {
		return nil, errCurrentEquipmentRecipeCompoundMaterials
	}
	slot := int16(slots[0])
	key := currentCeraShopInventorySlotKey(0, slot)
	stack := dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{key: inventory.Slots[key]}}).Slots[key]
	if !stack.ExpireAt.IsZero() || currentItemListStackExpire(stack) != 0 ||
		(len(stack.RawEntry) != 0 && (len(stack.RawEntry) != currentItemListEntryWireSize || binary.LittleEndian.Uint32(stack.RawEntry[2:6]) != uint32(stack.ItemID))) {
		return nil, errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	// Slot selection is deterministic and performed inside the same owner UOW
	// that will consume this exact instance, all materials, and the drawing.
	return &currentEquipmentRecipeUpgradePlan{Key: key, Slot: slot, Stack: stack, Output: output, KeepSockets: entry.MaintainSocketStat}, nil
}

func applyCurrentEquipmentRecipeUpgrade(inventory *dnfrepo.InventoryRecord, plan *currentEquipmentRecipeUpgradePlan) error {
	if inventory == nil || plan == nil {
		return errCurrentEquipmentRecipeCompoundUnavailable
	}
	if _, occupied := inventory.Slots[plan.Key]; occupied {
		return errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	stack := plan.Stack
	// Resolve the quality seed before changing the input template identity.
	if seed := currentEquipmentQualitySeedFromStack(stack); seed != 0 {
		if err := applyCurrentEquipmentQualitySeed(&stack, seed); err != nil {
			return err
		}
	}
	stack.ItemID, stack.Count = int64(plan.Output.ItemID), 1
	if stack.Extra == nil {
		stack.Extra = make(map[string]string)
	}
	stack.Extra["item_kind"], stack.Extra["pvf_path"] = string(dungeonDropItemEquipment), plan.Output.PVFPath
	stack.Extra["equipment_type"] = plan.Output.EquipmentType
	stack.Extra["source"] = "equipment_recipe_upgrade"
	delete(stack.Extra, "stackable_type")
	seal := byte(0)
	if normalizeDungeonDropStackableType(plan.Output.AttachType) == "sealing" {
		seal = 1
	}
	stack.Bind = seal != 0
	for _, key := range []string{"seal_flag", "seal", "bind_flag", "bind"} {
		stack.Extra[key] = fmt.Sprint(seal)
	}
	row := currentItemListEntryFromStack(0, plan.Slot, stack)
	row.data[0x0D] = seal
	if !plan.KeepSockets {
		for _, key := range currentEquipmentEmblemDataExtraKeys {
			delete(stack.Extra, key)
		}
		for _, key := range currentEquipmentTailDataExtraKeys {
			data := currentItemListVariableExtraBytes(stack.Extra, key)
			if len(data) >= currentEquipmentEmblemDataBytes {
				for i := 0; i < currentEquipmentEmblemDataBytes; i++ {
					data[i] = 0
				}
				currentSetHexExtra(stack.Extra, key, data)
			}
		}
		for i := currentEquipmentVectorOffset; i < currentEquipmentVectorOffset+9; i++ {
			row.data[i] = 0
		}
	}
	stack.RawEntry = append([]byte(nil), row.data[:]...)
	inventory.Slots[plan.Key] = stack
	return nil
}
