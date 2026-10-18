package signupbenefits_test

import (
	"context"
	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/signupbenefits"
	"strconv"
	"testing"
)

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestWelcomeCeraOnlyOnceAndCharacterFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	creator := characterdata.NewCreator(g)
	require(t, g.Account.Save(ctx, repository.AccountRecord{AccountID: "new", Metadata: map[string]string{"account_cera": "25", "other": "keep"}}))
	create := func(id string, slot int, amount int64) error {
		return creator.Create(ctx, characterdata.Creation{
			Account: &repository.AccountRecord{AccountID: "new"}, WelcomeCera: amount,
			Character: repository.CharacterRecord{CharacterID: id, AccountID: "new", Slot: slot, Name: "test" + id, Stats: map[string]int64{"gold": 678}},
		})
	}
	// Collision must roll back the credit and receipt along with the character.
	require(t, g.Character.Save(ctx, repository.CharacterRecord{CharacterID: "1", AccountID: "other"}))
	if err := create("1", 0, 200); err == nil {
		t.Fatal("expected collision")
	}
	a, _, err := g.Account.Load(ctx, "new")
	require(t, err)
	if a.Metadata["account_cera"] != "25" || a.Metadata[signupbenefits.WelcomeReceipt] != "" {
		t.Fatal(a)
	}
	require(t, create("2", 0, 200))
	require(t, create("3", 1, 200))
	a, _, err = g.Account.Load(ctx, "new")
	require(t, err)
	if a.Metadata["account_cera"] != "225" || a.Metadata["other"] != "keep" {
		t.Fatal(a)
	}
	for _, id := range []string{"2", "3"} {
		c, _, err := g.Character.Load(ctx, id)
		require(t, err)
		if c.Stats["gold"] != 678 {
			t.Fatal(c)
		}
	}
	// Zero default has no credit or receipt; invalid/overflow fails closed.
	old := repository.AccountRecord{}
	require(t, signupbenefits.WelcomeCera(&old, 0))
	if len(old.Metadata) != 0 {
		t.Fatal(old)
	}
	old.Metadata = map[string]string{"account_cera": strconv.FormatInt(signupbenefits.MaxAmount, 10)}
	if err := signupbenefits.WelcomeCera(&old, 1); err == nil {
		t.Fatal("overflow accepted")
	}
}
