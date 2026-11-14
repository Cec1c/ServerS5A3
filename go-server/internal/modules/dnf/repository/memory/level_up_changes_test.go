package memory

import (
	"context"
	"errors"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestMemoryLevelUpChangeFollowsSuccessfulTransaction(t *testing.T) {
	for _, scope := range []string{"assets", "progression"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			g := NewMemoryGroup()
			seedCharacterProgression(t, ctx, g)
			notices := make(chan string, 4)
			unsubscribe := g.LevelUpChanges.Subscribe(func(id string) { notices <- id })
			defer unsubscribe()
			run := func(fail bool) error {
				apply := func(chars r.CharacterRepository) error {
					ch, _, err := chars.Load(ctx, "77")
					if err != nil {
						return err
					}
					ch.Level++
					if err := chars.Save(ctx, ch); err != nil {
						return err
					}
					if len(notices) != 0 {
						t.Fatal("notification before commit")
					}
					if fail {
						return errors.New("rollback")
					}
					return nil
				}
				if scope == "assets" {
					return g.CharacterAssets.WithinCharacterAssets(ctx, "77", func(c r.CharacterRepository, _ r.InventoryRepository, _ r.EquipmentRepository) error { return apply(c) })
				}
				return g.CharacterProgression.WithinCharacterProgression(ctx, "77", func(c r.CharacterRepository, _ r.SkillRepository) error { return apply(c) })
			}
			if err := run(true); err == nil || len(notices) != 0 {
				t.Fatal("rollback published", err)
			}
			ch, _, _ := g.Character.Load(ctx, "77")
			if ch.Level != 2 {
				t.Fatal("rollback lost", ch.Level)
			}
			if err := run(false); err != nil {
				t.Fatal(err)
			}
			if len(notices) != 1 || <-notices != "77" {
				t.Fatal("missing commit notification")
			}
			ch, _, _ = g.Character.Load(ctx, "77")
			if ch.Level != 3 {
				t.Fatal("notification without committed state", ch.Level)
			}
		})
	}
}
