package dnfbridge

import (
	"fmt"
	"math"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// currentLotteryOpeningGold reads the opening price, separate from NPC
// acquisition materials. Do not turn an unreadable price into a free open.
func currentLotteryOpeningGold(document *dnfpvf.Document) (int64, bool, error) {
	if !currentLotterySupportedType(normalizeDungeonDropStackableType(magicBoxDocumentText(document, "stackable type"))) {
		return 0, false, nil
	}
	var cost int64
	found := false
	for _, section := range document.Sections {
		if normalizeMagicBoxSectionName(section.Name) != "lottery use cost" {
			continue
		}
		if found || section.Start < 0 || section.End > len(document.Tokens) || section.End-section.Start != 1 {
			return 0, true, fmt.Errorf("%w: invalid lottery opening price", errCurrentBoosterPVFInvalid)
		}
		token := document.Tokens[section.Start]
		if token.Kind != dnfpvf.TokenInt || token.Int < 0 || token.Int > math.MaxInt32 {
			return 0, true, fmt.Errorf("%w: invalid lottery opening price", errCurrentBoosterPVFInvalid)
		}
		cost, found = token.Int, true
	}
	return cost, found, nil
}
