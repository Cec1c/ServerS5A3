package epicwish

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfmemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestDurableSlotsClearAndOwnership(t *testing.T) {
	ctx := context.Background()
	repos := dnfmemory.NewMemoryGroup()
	for _, id := range []string{"1", "2"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "a", Stats: map[string]int64{"gold": 123, "hp": 456}}); err != nil {
			t.Fatal(err)
		}
	}
	want := Slots{101, 0, 103, -1, 105}
	allowed := func(id int32) bool { return id == 101 || id == 103 || id == 105 }
	if err := Save(ctx, repos, "a", "1", want, allowed); err != nil {
		t.Fatal(err)
	}
	want[3] = 0
	r, _, err := repos.Character.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if Read(r) != want || r.Stats["gold"] != 123 || r.Stats["hp"] != 456 {
		t.Fatalf("after relog=%+v", r)
	}
	r2, _, _ := repos.Character.Load(ctx, "2")
	if Read(r2) != (Slots{}) {
		t.Fatal("another character inherited wishes")
	}
	for _, test := range []struct {
		account string
		slots   Slots
		err     error
	}{
		{"wrong", want, ErrCharacter}, {"a", Slots{101, 101}, ErrInvalid}, {"a", Slots{999}, ErrInvalid}, {"a", Slots{-2}, ErrInvalid},
	} {
		if err := Save(ctx, repos, test.account, "1", test.slots, allowed); !errors.Is(err, test.err) {
			t.Fatalf("error=%v", err)
		}
		again, _, _ := repos.Character.Load(ctx, "1")
		if !reflect.DeepEqual(again, r) {
			t.Fatal("rejection modified character")
		}
	}
	if err := Save(ctx, repos, "a", "1", Slots{}, allowed); err != nil {
		t.Fatal(err)
	}
	r, _, _ = repos.Character.Load(ctx, "1")
	if Read(r) != (Slots{}) || r.Stats["gold"] != 123 {
		t.Fatal("clear did not persist")
	}
}
