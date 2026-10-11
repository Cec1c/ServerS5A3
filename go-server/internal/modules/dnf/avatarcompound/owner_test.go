package avatarcompound

import (
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerCompoundAvatarsCommitsConsumptionAndGrantTogether(t *testing.T) {
	ctx := context.Background()
	repositories := newAvatarCompoundOwnerTestRepositories(t, "dnf:1")
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	err = owner.CompoundAvatars(ctx, Command{
		AccountID:   "dnf:1",
		CharacterID: "19",
		Project: func(assets *Assets) (Changes, error) {
			delete(assets.Inventory.Slots, "1:5")
			delete(assets.Inventory.Slots, "1:6")
			assets.Inventory.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 21, Count: 1}
			assets.Inventory.Slots["1:5"] = dnfrepo.ItemStack{ItemID: 41003, Count: 1}
			return Changes{Inventory: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	if _, exists := inventory.Slots["1:6"]; exists {
		t.Fatalf("second material was not consumed: %+v", inventory.Slots)
	}
	if inventory.Slots["0:9"].Count != 1 {
		t.Fatalf("compound material count = %+v", inventory.Slots["0:9"])
	}
	if inventory.Slots["1:5"].ItemID != 41003 {
		t.Fatalf("granted result = %+v", inventory.Slots["1:5"])
	}
}

func TestOwnerCompoundAvatarsRollsBackOnProjectorError(t *testing.T) {
	ctx := context.Background()
	repositories := newAvatarCompoundOwnerTestRepositories(t, "dnf:1")
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("projection failed")
	err = owner.CompoundAvatars(ctx, Command{
		AccountID:   "dnf:1",
		CharacterID: "19",
		Project: func(assets *Assets) (Changes, error) {
			delete(assets.Inventory.Slots, "1:5")
			delete(assets.Inventory.Slots, "1:6")
			return Changes{Inventory: true}, wantErr
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("CompoundAvatars() error=%v want=%v", err, wantErr)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	if inventory.Slots["1:5"].ItemID != 41001 || inventory.Slots["1:6"].ItemID != 41002 {
		t.Fatalf("failed projection leaked consumption: %+v", inventory.Slots)
	}
}

func TestOwnerCompoundAvatarsRejectsCrossAccountCharacter(t *testing.T) {
	ctx := context.Background()
	repositories := newAvatarCompoundOwnerTestRepositories(t, "dnf:2")
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	err = owner.CompoundAvatars(ctx, Command{
		AccountID:   "dnf:1",
		CharacterID: "19",
		Project: func(*Assets) (Changes, error) {
			return Changes{}, nil
		},
	})
	if !errors.Is(err, ErrAccountMismatch) {
		t.Fatalf("CompoundAvatars() error=%v want=%v", err, ErrAccountMismatch)
	}
}

func TestOwnerCompoundAvatarsRequiresAccountCharacterAndProjector(t *testing.T) {
	ctx := context.Background()
	repositories := newAvatarCompoundOwnerTestRepositories(t, "dnf:1")
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		command Command
		want    error
	}{
		"no account":   {command: Command{CharacterID: "19", Project: func(*Assets) (Changes, error) { return Changes{}, nil }}, want: ErrAccountRequired},
		"no character": {command: Command{AccountID: "dnf:1", Project: func(*Assets) (Changes, error) { return Changes{}, nil }}, want: ErrCharacterRequired},
		"no projector": {command: Command{AccountID: "dnf:1", CharacterID: "19"}, want: ErrProjectorRequired},
	}
	for name, testCase := range cases {
		if err := owner.CompoundAvatars(ctx, testCase.command); !errors.Is(err, testCase.want) {
			t.Fatalf("%s error=%v want=%v", name, err, testCase.want)
		}
	}
}

func TestNewOwnerRejectsMissingAssetUnitOfWork(t *testing.T) {
	if _, err := NewOwner(dnfrepo.Group{}); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("NewOwner() error=%v want=%v", err, ErrOwnerUnavailable)
	}
}

func newAvatarCompoundOwnerTestRepositories(t *testing.T, characterAccountID string) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: characterAccountID}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"1:5": {ItemID: 41001, Count: 1},
			"1:6": {ItemID: 41002, Count: 1},
			"0:9": {ItemID: 21, Count: 2},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}
