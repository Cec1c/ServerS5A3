package cargo

import (
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestAccountCargoPVFLateTiersAndNoUnpricedDirectUpgrade(t *testing.T) {
	for previous := int64(64); previous < 120; previous += 8 {
		next, ok := NextAccountCargoTier(previous)
		if !ok || next != previous+8 {
			t.Fatalf("tier %d: next=%d ok=%t", previous, next, ok)
		}
		character := dnfrepo.CharacterRecord{Stats: map[string]int64{"gold": 999999999}}
		account := dnfrepo.AccountRecord{Metadata: map[string]string{"account_cera": "999999999"}}
		if _, _, err := applyCargoUpgradeCost(&character, &account, nil, previous); !errors.Is(err, ErrCargoCostMissing) {
			t.Fatalf("unpriced tier %d cost error=%v", previous, err)
		}
		if character.Stats["gold"] != 999999999 || account.Metadata["account_cera"] != "999999999" {
			t.Fatal("rejected direct upgrade changed balances")
		}
	}
	if _, ok := NextAccountCargoTier(120); ok {
		t.Fatal("120 is PVF maximum")
	}
}
