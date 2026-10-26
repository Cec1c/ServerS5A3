package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/booster"
	"longheng.io/server/internal/modules/dnf/itemusage"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestItemUsageMySQLAtomicAndConcurrent(t *testing.T) {
	for _, kind := range []string{"inventory", "booster"} {
		t.Run(kind, func(t *testing.T) {
			db, name, newGroup := itemTransactionMySQLFixture(t)
			ctx := context.Background()
			seed, cmd, definition := itemUsageOwnerFixture(t)
			character, _, _ := seed.Character.Load(ctx, "77")
			inventory, _, _ := seed.Inventory.Load(ctx, "77")
			g := newGroup()
			for _, err := range []error{g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}), g.Character.Save(ctx, character), g.Inventory.Save(ctx, inventory)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			run := func(g repo.Group) error {
				if kind == "inventory" {
					o, err := NewOwner(g)
					if err != nil {
						return err
					}
					_, err = o.UseStackable(ctx, cmd, nil)
					return err
				}
				o, err := booster.NewOwner(g)
				if err != nil {
					return err
				}
				return o.Open(ctx, booster.Command{AccountID: "a", CharacterID: "77", UpdatedAt: cmd.ItemUsageNow, ItemUsage: &definition, Project: func(a *booster.Assets) (booster.Changes, error) {
					s := a.Inventory.Slots["0:130"]
					if s.ItemID != 7440 || s.Count <= 0 {
						return booster.Changes{}, errors.New("source missing")
					}
					s.Count--
					a.Inventory.Slots["0:130"] = s
					reward := a.Inventory.Slots["0:131"]
					reward.ItemID = 600
					reward.Count++
					a.Inventory.Slots["0:131"] = reward
					return booster.Changes{Inventory: true}, nil
				}})
			}
			beforeC, _, _ := newGroup().Character.Load(ctx, "77")
			beforeI, _, _ := newGroup().Inventory.Load(ctx, "77")
			// Ordinary use records stats before the source save; booster saves the
			// source/reward before stats. Fail the later SQL write in each owner.
			table := "dnf_inventory_items"
			if kind == "booster" {
				table = "dnf_character_stats"
			}
			if _, err := db.Exec("CREATE TRIGGER `" + name + "`.reject_usage BEFORE INSERT ON `" + name + "`.`" + table + "` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected usage failure'"); err != nil {
				t.Fatal(err)
			}
			if err := run(newGroup()); err == nil {
				t.Fatal("SQL failure accepted")
			}
			afterC, _, _ := newGroup().Character.Load(ctx, "77")
			afterI, _, _ := newGroup().Inventory.Load(ctx, "77")
			if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatal("SQL failure left count, debit or reward")
			}
			if _, err := db.Exec("DROP TRIGGER `" + name + "`.reject_usage"); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			done := make(chan error, 8)
			for range 8 {
				g := newGroup()
				go func() { <-start; done <- run(g) }()
			}
			close(start)
			success := 0
			for range 8 {
				err := <-done
				if err == nil {
					success++
				} else if !errors.Is(err, itemusage.ErrLimit) {
					t.Fatal(err)
				}
			}
			afterC, _, _ = newGroup().Character.Load(ctx, "77")
			afterI, _, _ = newGroup().Inventory.Load(ctx, "77")
			used, _ := itemusage.Counts(afterC, definition, cmd.ItemUsageNow)
			if success != 3 || used != 3 || afterI.Slots["0:130"].Count != 7 {
				t.Fatalf("concurrent persisted counts: success=%d used=%d stock=%d", success, used, afterI.Slots["0:130"].Count)
			}
			if kind == "booster" && afterI.Slots["0:131"].Count != 3 {
				t.Fatal("SQL duplicate/lost reward")
			}
		})
	}
}
