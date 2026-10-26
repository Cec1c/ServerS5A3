package dnfbridge

import (
	"math"
	"strconv"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func currentAikenSetRules(c *pvfDungeonDropCatalog) (aiken.SetRules, error) {
	var out aiken.SetRules
	individual, err := currentAikenRules(c)
	if err != nil {
		return out, err
	}
	text, err := c.source.ReadText("etc/customability/partsetcustominfo.etc")
	if err != nil {
		return out, err
	}
	catalog, err := aiken.ParseSetCatalog(text)
	if err != nil {
		return out, err
	}
	out.Item = individual
	out.Write = currentAikenWriteSet
	out.Existing = func(s repo.ItemStack) (aiken.Record, bool, error) {
		if s.ItemID <= 0 || s.ItemID > math.MaxUint32 {
			return aiken.Record{}, false, nil
		}
		item, err := c.ResolveItem(uint32(s.ItemID))
		if err != nil {
			return aiken.Record{}, false, err
		}
		if item.Kind != dungeonDropItemEquipment {
			return aiken.Record{}, false, nil
		}
		d, err := aikenDoc(c.source, item.PVFPath)
		if err != nil {
			return aiken.Record{}, false, err
		}
		if !aikenString(d, "custom ability type", "warriors dungeon") {
			return aiken.Record{}, false, nil
		}
		return currentAikenReadSet(s)
	}
	plan := func(items []repo.ItemStack, options []byte, previous []aiken.Record) (aiken.SetPlan, error) {
		var result aiken.SetPlan
		grades := make([]byte, 0, len(items))
		occupied := map[int16]bool{}
		index := int64(0)
		armorCount := 0
		armorTypes := map[string]bool{}
		for _, s := range items {
			item, err := c.ResolveItem(uint32(s.ItemID))
			if err != nil {
				return result, err
			}
			d, err := aikenDoc(c.source, item.PVFPath)
			if err != nil {
				return result, err
			}
			v, err := aikenSection(d, "custom ability part set index")
			if err != nil || len(v) != 1 || v[0].Kind != pvf.TokenInt || v[0].Int <= 0 || v[0].Int > 255 {
				return result, aiken.ErrInvalid
			}
			if index != 0 && index != v[0].Int {
				return result, aiken.ErrInvalid
			}
			index = v[0].Int
			placement, ok := currentEquipmentPlacementRuleForPVFType(item.EquipmentType)
			if !ok || occupied[placement.targetSlot] {
				return result, aiken.ErrInvalid
			}
			occupied[placement.targetSlot] = true
			if placement.targetSlot >= 14 && placement.targetSlot <= 18 {
				armorCount++
				sub, err := aikenSection(d, "sub type")
				if err != nil || len(sub) != 1 || sub[0].Kind != pvf.TokenInt {
					return result, aiken.ErrInvalid
				}
				armorTypes[strconv.FormatInt(sub[0].Int, 10)] = true
			}
			state, err := currentAikenState(s)
			if err != nil {
				return result, err
			}
			grades = append(grades, state[4])
		}
		if armorCount == 5 && len(armorTypes) != 1 {
			return result, aiken.ErrInvalid
		}
		grade, cost, err := catalog.PlanSelection(int(index), grades, options)
		if err != nil {
			return result, err
		}
		result.Costs = cost.Materials
		for _, old := range previous {
			if int64(old.Record0) != index {
				continue
			}
			oldGrades := make([]byte, int(old.Record1))
			for i := range oldGrades {
				oldGrades[i] = old.Record18
			}
			_, oldCost, err := catalog.PlanSelection(int(index), oldGrades, old.Options())
			if err != nil {
				return result, err
			}
			result.Costs = aiken.NetMaterials(result.Costs, aiken.ApplyDiscountRate(oldCost.Materials, catalog.Points[int(index)].DiscountRate))
		}
		slots, err := aiken.SlotsFromOptions(options)
		if err != nil {
			return result, err
		}
		result.Record = aiken.Record{Record0: byte(index), Record1: byte(len(items)), Slots: slots, Record18: byte(grade)}
		return result, nil
	}
	out.Plan = func(items []repo.ItemStack, options []byte) (aiken.SetPlan, error) { return plan(items, options, nil) }
	out.Replan = plan
	return out, nil
}
