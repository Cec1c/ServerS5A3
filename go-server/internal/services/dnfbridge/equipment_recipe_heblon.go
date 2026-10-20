package dnfbridge

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// The matched v150 maps need-material to SRC+64 and uses it for NPC purchase
// pricing. Its owned-drawing op25 path takes explicit input/output from CEC/D00.
// Current resources sell box10304511 for10159142x950, then select one of these
// three drawings. Keep this evidence-bound exception out of generic crafting.
func currentHeblonOwnedUpgradeShopPrice(entry equipmentRecipeEntry, document *dnfpvf.Document) (bool, error) {
	tokens, present, err := currentEquipmentRecipeCompoundQualificationTokens(document, "need material")
	if !present && err == nil && len(entry.ExtraMaterials) == 0 {
		return false, nil
	}
	reject := func() (bool, error) {
		return false, fmt.Errorf("%w: need_material_semantics_unproven", errCurrentEquipmentRecipeCompoundUnsupported)
	}
	if err != nil || !present || len(tokens) != 2 || len(entry.ExtraMaterials) != 1 ||
		tokens[0].Kind != dnfpvf.TokenInt || tokens[1].Kind != dnfpvf.TokenInt ||
		tokens[0].Int != 10159142 || tokens[1].Int != 950 ||
		entry.ExtraMaterials[0] != (equipmentRecipeItemStack{ItemID: 10159142, Count: 950}) {
		return reject()
	}
	for _, section := range document.Sections {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(section.Name), "/"), "usable datetime") {
			// Native SRC+CE8 is a use-date gate, separate from the purchase
			// vector. This family has no proven dated-use contract.
			return reject()
		}
	}
	var inputID, outputID int64
	switch entry.ItemID {
	case 10304471:
		inputID, outputID = 100344513, 100344527
	case 10304472:
		inputID, outputID = 100352817, 100352839
	case 10304473:
		inputID, outputID = 100390005, 100390011
	default:
		return reject()
	}
	if entry.Kind != equipmentRecipeKindMaterialOutput || entry.MaintainAbility != "normal" ||
		!entry.MaintainSocketStat || entry.MinimumItemmakingFound || entry.GoldCostFound || entry.GoldCost != 0 ||
		len(entry.ZeroCountPairs) != 0 || len(entry.Materials) != 5 || len(entry.Outputs) != 1 {
		return reject()
	}
	wantInput := []equipmentRecipeItemStack{{inputID, 1}, {10159623, 80}, {10158964, 15}, {3262, 1000}, {10000224, 1}}
	for i, want := range wantInput {
		if entry.Materials[i] != want {
			return reject()
		}
	}
	if entry.Outputs[0] != (equipmentRecipeItemStack{ItemID: outputID, Count: 1}) {
		return reject()
	}
	// Check raw pairs too: duplicate, orphan, missing, symbolic or extended
	// sections must not become admissible through a catalog projection.
	for _, field := range []struct {
		name string
		want []equipmentRecipeItemStack
	}{{"need material", entry.ExtraMaterials}, {"input item", wantInput}, {"output item", entry.Outputs}} {
		values, found, err := currentEquipmentRecipeCompoundQualificationTokens(document, field.name)
		if err != nil || !found || len(values) != len(field.want)*2 {
			return reject()
		}
		closed := false
		for _, section := range document.Sections {
			if strings.EqualFold(strings.TrimSpace(section.Name), "/"+field.name) {
				closed = true
			}
		}
		if !closed {
			return reject()
		}
		for i, want := range field.want {
			if values[2*i].Kind != dnfpvf.TokenInt || values[2*i+1].Kind != dnfpvf.TokenInt ||
				values[2*i].Int != want.ItemID || values[2*i+1].Int != want.Count {
				return reject()
			}
		}
	}
	return true, nil
}
