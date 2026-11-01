package dungeon

import (
	"context"
	"errors"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestEpicEntryFatigue030CallbackTransaction(t *testing.T) {
	for _, name := range []string{"success_and_replay", "callback_failure", "commit_failure", "insufficient_ticket", "zero_fatigue"} {
		t.Run(name, func(t *testing.T) {
			owner, g, cmd := epicOwnerFixture(t)
			ctx := context.Background()
			calls := 0
			failure := errors.New("activity write rejected")
			cmd.OnFatigueDebit = func(ch *repo.CharacterRecord, n int64, at time.Time) error {
				calls++
				if n != 1 || !at.Equal(cmd.UpdatedAt) || ch.CharacterID != cmd.CharacterID {
					t.Fatal("incorrect debit identity/amount/time")
				}
				ch.Stats["activity_fatigue_probe"] += n
				if name == "callback_failure" {
					return failure
				}
				return nil
			}
			if name == "commit_failure" {
				g.CharacterAssets = epicRollbackAssets{g.CharacterAssets, failure}
				owner, _ = NewOwner(g)
			}
			if name == "insufficient_ticket" {
				_ = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{}})
			}
			if name == "zero_fatigue" {
				ch, _, _ := g.Character.Load(ctx, "31")
				ch.Stats["fatigue"] = 0
				_ = g.Character.Save(ctx, ch)
			}
			before, _, _ := g.Character.Load(ctx, "31")
			bag, _, _ := g.Inventory.Load(ctx, "31")
			result, err := owner.ConsumeEpicEntry(ctx, cmd)
			after, _, _ := g.Character.Load(ctx, "31")
			afterBag, _, _ := g.Inventory.Load(ctx, "31")
			if name == "success_and_replay" {
				if err != nil || result.Replay || calls != 1 || after.Stats["activity_fatigue_probe"] != 1 {
					t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
				}
				result, err = owner.ConsumeEpicEntry(ctx, cmd)
				if err != nil || !result.Replay || calls != 1 {
					t.Fatal("replay callback", calls, err)
				}
			} else {
				if err == nil || after.Stats["activity_fatigue_probe"] != 0 || after.Stats["fatigue"] != before.Stats["fatigue"] || after.Stats[EpicEntrySequenceKey] != before.Stats[EpicEntrySequenceKey] || afterBag.Slots["0:1"].Count != bag.Slots["0:1"].Count {
					t.Fatal("rejected entry persisted side effects", err)
				}
				if (name == "insufficient_ticket" || name == "zero_fatigue") && calls != 0 {
					t.Fatal("callback before debit")
				}
			}
		})
	}
}
