package dnfbridge

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// The current PVF lists the Anton exchange price in [need material] for the
// nine Infinite Devourer drawings. The op25 making payload is the separate
// [input item] vector. The client's op25 affordability helper consumes the
// input/output domain; the drawing's own purchase price was already paid when
// it entered the inventory. Match the complete, current document shape so the
// generic unproven-need-material gate remains intact for every other drawing.
func currentInfiniteDevourerOwnedUpgradeShopPrice(entry equipmentRecipeEntry, document *dnfpvf.Document) (bool, error) {
	tokens, present, err := currentEquipmentRecipeCompoundQualificationTokens(document, "need material")
	if !present && err == nil && len(entry.ExtraMaterials) == 0 {
		return false, nil
	}
	reject := func() (bool, error) {
		return false, fmt.Errorf("%w: need_material_semantics_unproven", errCurrentEquipmentRecipeCompoundUnsupported)
	}
	rejectShape := func(detail string) (bool, error) {
		return false, fmt.Errorf("%w: infinite_devourer_shape_invalid:%s", errCurrentEquipmentRecipeCompoundUnsupported, detail)
	}
	if err != nil || !present || len(tokens) != 2 || len(entry.ExtraMaterials) != 1 ||
		tokens[0].Kind != dnfpvf.TokenInt || tokens[1].Kind != dnfpvf.TokenInt ||
		tokens[0].Int != 10093974 || tokens[1].Int != 30 ||
		entry.ExtraMaterials[0] != (equipmentRecipeItemStack{ItemID: 10093974, Count: 30}) {
		return reject()
	}
	for _, section := range document.Sections {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(section.Name), "/"), "usable datetime") {
			return reject()
		}
	}
	shape, found := currentInfiniteDevourerUpgradeShapes[entry.ItemID]
	if !found {
		return reject()
	}
	wantMaterials := []equipmentRecipeItemStack{
		{ItemID: shape.input, Count: 1},
		{ItemID: 10093972, Count: 100},
		{ItemID: 3262, Count: 1000},
		{ItemID: 10095702, Count: shape.soulFragmentCount},
	}
	if shape.furnaceCoupon {
		wantMaterials = append(wantMaterials, equipmentRecipeItemStack{ItemID: 10000224, Count: 1})
	}
	if entry.Kind != equipmentRecipeKindMaterialOutput || entry.MaintainAbility != "normal" ||
		!entry.MinimumItemmakingFound || entry.MinimumItemmakingLevel != 8 ||
		entry.GoldCostFound || entry.GoldCost != 0 || len(entry.ZeroCountPairs) != 0 ||
		len(entry.Materials) != len(wantMaterials) || len(entry.Outputs) != 1 {
		return rejectShape("entry")
	}
	if !equalEquipmentRecipeStacks(entry.Materials, wantMaterials) {
		return rejectShape("materials")
	}
	if entry.Outputs[0] != (equipmentRecipeItemStack{ItemID: shape.output, Count: 1}) {
		return rejectShape("output")
	}
	// Recheck raw pairs and closing tags so duplicate, orphan or extended
	// sections cannot become admissible through the catalog projection.
	for _, field := range []struct {
		name string
		want []equipmentRecipeItemStack
	}{
		{"need material", entry.ExtraMaterials},
		{"input item", wantMaterials},
		{"output item", entry.Outputs},
	} {
		values, found, err := currentEquipmentRecipeCompoundQualificationTokens(document, field.name)
		if err != nil || !found || len(values) != len(field.want)*2 {
			return rejectShape("tokens:" + field.name)
		}
		closed := false
		for _, section := range document.Sections {
			if strings.EqualFold(strings.TrimSpace(section.Name), "/"+field.name) {
				closed = true
			}
		}
		if !closed {
			return rejectShape("close:" + field.name)
		}
		for i, want := range field.want {
			if values[2*i].Kind != dnfpvf.TokenInt || values[2*i+1].Kind != dnfpvf.TokenInt ||
				values[2*i].Int != want.ItemID || values[2*i+1].Int != want.Count {
				return rejectShape("pair:" + field.name)
			}
		}
	}
	return true, nil
}

type currentInfiniteDevourerUpgradeShape struct {
	input             int64
	output            int64
	soulFragmentCount int64
	furnaceCoupon     bool
}

var currentInfiniteDevourerUpgradeShapes = map[int64]currentInfiniteDevourerUpgradeShape{
	10304468: {100312099, 100312095, 4, false},
	10304469: {100300216, 100300212, 4, false},
	10304470: {100321958, 100321954, 4, false},
	10096047: {100300212, 100300221, 5, true},
	10096048: {100312095, 100312103, 5, true},
	10096049: {100321954, 100321962, 5, true},
	10096050: {100343175, 100343190, 5, true},
	10096051: {100352106, 100352113, 5, true},
	10096052: {100352106, 100352115, 5, true},
}

func equalEquipmentRecipeStacks(left, right []equipmentRecipeItemStack) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
