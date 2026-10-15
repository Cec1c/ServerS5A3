package booster

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"testing"
)

func TestCreditGoldPreservesWalletOnLevelOrOverflowFailure(t *testing.T) {
	for _, row := range []struct {
		level  int
		gold   int64
		amount uint32
	}{{29, 100, 1}, {90, math.MaxInt32, 1}, {90, -1, 1}, {90, 100, 0}} {
		c := repo.CharacterRecord{Level: row.level, Stats: map[string]int64{"gold": row.gold, "grow_type": 2}}
		if _, err := CreditGold(&c, row.amount, 30); err == nil || c.Stats["gold"] != row.gold || c.Stats["grow_type"] != 2 {
			t.Fatalf("%+v %v", c, err)
		}
	}
	c := repo.CharacterRecord{Level: 90, Stats: map[string]int64{"gold": 100, "grow_type": 2}}
	after, err := CreditGold(&c, 120000, 30)
	if err != nil || after != 120100 || c.Stats["grow_type"] != 2 {
		t.Fatalf("%+v %v", c, err)
	}
}
