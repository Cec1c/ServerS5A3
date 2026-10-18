package dnfbridge

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Matched native virtual128 excludes packagable=0/category packagable;
// equipment also excludes cash, avatars and creatures. Ordinary free/trade
// items and still-sealed equipment use the authoritative instance state.
func currentGuildCargoRules(items *pvfDungeonDropCatalog, limit uint32, now time.Time) inventory.GuildCargoRules {
	normalize := func(stack r.ItemStack) (r.ItemStack, error) {
		bad := func() (r.ItemStack, error) { return r.ItemStack{}, inventory.ErrGuildCargoRequest }
		if stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 || stack.Count <= 0 || currentDungeonDiscardStackExpired(stack, now) || stack.Extra["trade_locked_by_first_equip"] == "1" || stack.Extra["trade_restriction"] == "1" {
			return bad()
		}
		def, err := items.ResolveItem(uint32(stack.ItemID))
		if err != nil {
			return r.ItemStack{}, err
		}
		container := currentPVFItemTargetContainer(def)
		if container.Avatar || container.PetBody || container.ListType != 0 && container.ListType != 7 {
			return bad()
		}
		// The client's feature-gated crystal/soul IDs use account storage;
		// their additional virtual8 gate is not a guild permission.
		switch stack.ItemID {
		case 3033, 3034, 3035, 3036, 3037, 3262:
			return bad()
		}
		text, err := items.source.ReadText(def.PVFPath)
		if err != nil {
			return r.ItemStack{}, err
		}
		doc, err := pvf.Parse(def.PVFPath, text)
		if err != nil {
			return r.ItemStack{}, err
		}
		if tokens, found := doc.Section("packagable"); found && (len(tokens) != 1 || tokens[0].Kind != pvf.TokenInt || tokens[0].Int == 0) {
			return bad()
		}
		for _, category := range doc.Texts("item category") {
			if normalizeMagicBoxPVFType(category) == "packagable" {
				return bad()
			}
		}
		if cash, found := doc.Int("cash"); def.Kind == dungeonDropItemEquipment && found && cash > 0 {
			return bad()
		}
		switch normalizeMagicBoxPVFType(def.AttachType) {
		case "free", "trade":
			if stack.Bind {
				return bad()
			}
		case "sealing":
			if def.Kind != dungeonDropItemEquipment {
				return bad()
			}
			if !currentEquipmentRegenerationSourceSealed(stack) {
				return bad()
			}
		default:
			return bad()
		}
		if def.Kind == dungeonDropItemEquipment && stack.Count != 1 {
			return bad()
		}
		if def.Kind == dungeonDropItemStackable && (def.StackLimit < 0 || def.StackLimit > 0 && stack.Count > def.StackLimit) {
			return bad()
		}
		if kind := stack.Extra["item_kind"]; kind != "" && kind != string(def.Kind) {
			return bad()
		}
		if path := stack.Extra["pvf_path"]; path != "" && !strings.EqualFold(strings.ReplaceAll(path, "\\", "/"), def.PVFPath) {
			return bad()
		}
		if raw := stack.Extra["stack_limit"]; raw != "" && def.Kind == dungeonDropItemStackable && raw != strconv.FormatInt(def.StackLimit, 10) {
			return bad()
		}
		if _, err := currentGuildCargoItemEntry(0, stack, def); err != nil {
			return r.ItemStack{}, err
		}
		if stack.Extra == nil {
			stack.Extra = map[string]string{}
		}
		stack.Extra["item_kind"] = string(def.Kind)
		stack.Extra["pvf_path"] = def.PVFPath
		if def.Kind == dungeonDropItemStackable {
			stack.Extra["stack_limit"] = strconv.FormatInt(def.StackLimit, 10)
			stack.Extra["stackable_type"] = def.StackableType
		}
		return stack, nil
	}
	return inventory.GuildCargoRules{MaxCapacity: limit, Normalize: normalize,
		Placement: func(_ r.CharacterRecord, stack r.ItemStack) (itemplacement.SlotPolicy, error) {
			def, err := items.ResolveItem(uint32(stack.ItemID))
			if err != nil {
				return itemplacement.SlotPolicy{}, err
			}
			return currentPVFItemPlacementPolicy(def), nil
		},
		Validate: func(_ r.CharacterRecord, req inventory.GuildCargoRequest, source, dest r.ItemStack) error {
			if _, err := normalize(source); err != nil {
				return err
			}
			if dest.ItemID != 0 {
				if _, err := normalize(dest); err != nil {
					return err
				}
			}
			if req.Source.Cargo && req.Destination.Cargo {
				return fmt.Errorf("native warehouse swap is not yet proven")
			}
			return nil
		},
	}
}
