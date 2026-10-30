package quest

import (
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
)

// Seeking int-data uses item ID zero for a required gold amount. It is not
// an inventory item or a reward marker. Payment joins the existing settlement
// transaction, before rewards, material persistence and slot unlocking.
func consumeFinishRequiredGold(character *dnfrepo.CharacterRecord, definition Definition) ([]FinishCommittedCurrency, error) {
	switch normalizeQuestTag(definition.Type) {
	case "seeking", "seek n meet npc":
	default:
		return nil, nil
	}
	var cost int64
	for i := 0; i+1 < len(definition.IntData); i += 2 {
		if definition.IntData[i] != 0 {
			continue
		}
		amount := definition.IntData[i+1]
		if amount < 0 || amount > math.MaxInt32-cost {
			return nil, fmt.Errorf("%w: invalid required gold", ErrQuestRewardMalformed)
		}
		cost += amount
	}
	if cost == 0 {
		return nil, nil
	}
	if character == nil || character.Stats["gold"] < cost {
		return nil, ErrFinishRequiredGoldMissing
	}
	character.Stats["gold"] -= cost
	return []FinishCommittedCurrency{{Name: "gold", Delta: -cost, PostValue: character.Stats["gold"]}}, nil
}
