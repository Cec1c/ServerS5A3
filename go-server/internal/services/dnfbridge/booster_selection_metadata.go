package dnfbridge

import dnfpvf "longheng.io/server/internal/modules/dnf/pvf"

// Native selection rows carry equipment attributes separately from item/count.
// The existing grant projection uses the resolved item's actual kind, not the
// reward section label. Equipment attributes have no representation on a
// stackable ticket. Do not generalize this compatibility to real equipment,
// missing templates, mixed pools, malformed metadata, or avatar options.
func parseCurrentBoosterStackableSelectionMetadata(catalog *pvfDungeonDropCatalog, document *dnfpvf.Document) ([]currentBoosterSelectionCandidate, map[uint16][]currentBoosterSelectionCandidate, bool) {
	if catalog == nil || document == nil {
		return nil, nil, false
	}
	copyDocument := *document
	copyDocument.Sections = make([]dnfpvf.Section, 0, len(document.Sections))
	found := false
	for _, section := range document.Sections {
		limit := int64(-1)
		switch normalizeMagicBoxSectionName(section.Name) {
		case "booster equipment grade":
			limit = 4
		case "booster equipment upgrade":
			limit = 255
		case "booster equipment separate":
			limit = 8
		}
		if limit < 0 {
			copyDocument.Sections = append(copyDocument.Sections, section)
			continue
		}
		values := magicBoxSectionInts(document, section)
		if section.End-section.Start != 1 || len(values) != 1 || document.Tokens[section.Start].Kind != dnfpvf.TokenInt || values[0] < 0 || values[0] > limit {
			return nil, nil, false
		}
		found = true
	}
	if !found {
		return nil, nil, false
	}
	rows, categories, err := parseCurrentBoosterSelectionCatalog(&copyDocument)
	if err != nil || len(rows) == 0 {
		return nil, nil, false
	}
	for _, row := range rows {
		item, err := catalog.ResolveItem(row.ItemID)
		if err != nil || item.Kind != dungeonDropItemStackable || row.CategoryKind != 0 || row.Option != 0 {
			return nil, nil, false
		}
	}
	return rows, categories, true
}
