package wedding

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

func TestWeddingEngageRejectsEitherTombstonedCharacter(t *testing.T) {
	for _, id := range []string{"1", "2"} {
		for _, mark := range []string{"flag", "slot"} {
			t.Run(id+"/"+mark, func(t *testing.T) {
				o, now := ownerFixture(t)
				ctx := context.Background()
				c, _, _ := o.Repositories.Character.Load(ctx, id)
				if mark == "flag" {
					c.Stats["delete_flag"] = 1
				} else {
					c.Slot = -1
				}
				if err := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldBase, r.CharacterFieldStats); err != nil {
					t.Fatal(err)
				}
				beforeA, _, _ := o.Repositories.Character.Load(ctx, "1")
				beforeB, _, _ := o.Repositories.Character.Load(ctx, "2")
				if _, err := o.Engage(ctx, pairRequest(now)); !errors.Is(err, ErrOwnership) {
					t.Fatalf("tombstone accepted: %v", err)
				}
				afterA, _, _ := o.Repositories.Character.Load(ctx, "1")
				afterB, _, _ := o.Repositories.Character.Load(ctx, "2")
				if !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeB, afterB) {
					t.Fatal("rejected engagement mutated pair")
				}
			})
		}
	}
}
