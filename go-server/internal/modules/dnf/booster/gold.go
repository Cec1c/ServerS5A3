package booster

import (
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
)

// CreditGold is used inside Open's asset transaction. Currency never creates
// an inventory item; reject an unrepresentable wallet rather than losing value.
func CreditGold(character *repo.CharacterRecord, amount uint32, minimumLevel int64) (uint32, error) {
	if character == nil || amount == 0 || minimumLevel < 0 || minimumLevel > math.MaxUint8 || int64(character.Level) < minimumLevel {
		return 0, fmt.Errorf("invalid gold reward or character level")
	}
	gold := character.Stats["gold"]
	if gold < 0 || gold > math.MaxInt32-int64(amount) {
		return 0, fmt.Errorf("gold reward exceeds client wallet limit")
	}
	gold += int64(amount)
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats["gold"] = gold
	return uint32(gold), nil
}
