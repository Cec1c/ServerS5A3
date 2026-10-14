package booster

import (
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOwnerGoldCostValidation(t *testing.T) {
	for _, cost := range []int64{-1, 1} {
		repos := newOwnerTestRepositories(t, "a")
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		err = owner.Open(context.Background(), Command{AccountID: "a", CharacterID: "77", GoldCost: cost, Project: func(*Assets) (Changes, error) {
			t.Fatal("invalid price or absent wallet reached mutation")
			return Changes{}, nil
		}})
		want := ErrGoldInsufficient
		if cost < 0 {
			want = ErrGoldCostInvalid
		}
		if !errors.Is(err, want) {
			t.Fatalf("cost=%d err=%v want=%v", cost, err, want)
		}
	}
}

func TestOwnerPersistsGoldEvenWithoutProjectorCharacterChange(t *testing.T) {
	ctx := context.Background()
	repos := newOwnerTestRepositories(t, "a")
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "a", Stats: map[string]int64{"gold": 1000000, "fatigue": 17}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	if err := owner.Open(ctx, Command{AccountID: "a", CharacterID: "77", GoldCost: 1000000, Project: func(*Assets) (Changes, error) { return Changes{}, nil }}); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 0 || character.Stats["fatigue"] != 17 {
		t.Fatalf("stats=%v", character.Stats)
	}
}
