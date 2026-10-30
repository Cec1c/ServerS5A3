package inventory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/itemusage"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func megaphoneAssets(t *testing.T) (r.Group, MegaphoneCommand) {
	t.Helper()
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "sample-account", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:68": {ItemID: 36, Count: 3}}}); err != nil {
		t.Fatal(err)
	}
	return repos, MegaphoneCommand{AccountID: "sample-account", CharacterID: 19, Mode: 11, Slot: 68, Session: strings.Repeat("01", 32), Sequence: 100,
		Now: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), Definition: MegaphoneDefinition{ItemID: 36, PVFPath: "stackable/cash/megaphone_heartbeat.stk", StackableType: "[waste]", MinimumLevel: 50,
			Slots: itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 104, AllowQuickSlots: true}}}
}

func TestMegaphoneConsumeReplayAndNewMessage(t *testing.T) {
	repos, cmd := megaphoneAssets(t)
	owner, _ := NewOwner(repos)
	ctx := context.Background()
	first, err := owner.ConsumeMegaphone(ctx, cmd)
	if err != nil || first.Remaining != 2 {
		t.Fatalf("first=%+v %v", first, err)
	}
	for _, seq := range []uint64{100, 99} {
		cmd.Sequence = seq
		if _, err := owner.ConsumeMegaphone(ctx, cmd); !errors.Is(err, ErrMegaphoneReplay) {
			t.Fatalf("replay=%v", err)
		}
	}
	cmd.Sequence = 101
	if got, err := owner.ConsumeMegaphone(ctx, cmd); err != nil || got.Remaining != 1 {
		t.Fatalf("legitimate next request=%+v %v", got, err)
	}
}

func TestMegaphoneInvalidSourceAndEligibilityRollback(t *testing.T) {
	for _, kind := range []string{"wrong_owner", "low_level", "account_level", "missing", "wrong_item", "expired", "metadata_expired", "locked", "unopened", "wrong_path", "wrong_type", "no_transaction", "usage_limit", "chat_ban"} {
		t.Run(kind, func(t *testing.T) {
			repos, cmd := megaphoneAssets(t)
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "19")
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			stack := inv.Slots["0:68"]
			stack.Extra = map[string]string{}
			switch kind {
			case "wrong_owner":
				cmd.AccountID = "other"
			case "low_level":
				c.Level = 49
			case "account_level":
				c.Level = 85
			case "missing":
				cmd.Slot = 58
			case "wrong_item":
				stack.ItemID = 37
			case "expired":
				stack.ExpireAt = cmd.Now
			case "metadata_expired":
				stack.Extra["expire_unix"] = "1"
			case "locked":
				stack.Extra["equipment_lock_state"] = "1"
			case "unopened":
				cmd.Slot = 115
				cmd.Definition.Slots.End = 120
				inv.Slots["0:115"] = stack
			case "wrong_path":
				cmd.Definition.PVFPath = "stackable/fake.stk"
			case "wrong_type":
				cmd.Definition.StackableType = "[etc]"
			case "no_transaction":
				repos.CharacterAssets = nil
			case "usage_limit":
				cmd.Definition.Usage = &itemusage.Definition{ItemID: 36, GroupID: -1, Daily: true, DailyLimit: 0}
			case "chat_ban":
				c.Stats = map[string]int64{"chat_ban_end_time": cmd.Now.Add(time.Hour).Unix()}
			}
			inv.Slots["0:68"] = stack
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			beforeC, _, _ := repos.Character.Load(ctx, "19")
			beforeI, _, _ := repos.Inventory.Load(ctx, "19")
			owner, _ := NewOwner(repos)
			got, err := owner.ConsumeMegaphone(ctx, cmd)
			if err == nil || got.Character.CharacterID != "" {
				t.Fatalf("invalid use committed: %+v %v", got, err)
			}
			afterC, _, _ := repos.Character.Load(ctx, "19")
			afterI, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatal("failed request changed assets or receipt")
			}
		})
	}
}

func TestMegaphoneSaveAndCommitFailuresRollbackReceipt(t *testing.T) {
	for _, late := range []bool{false, true} {
		repos, cmd := megaphoneAssets(t)
		ctx := context.Background()
		c, _, _ := repos.Character.Load(ctx, "19")
		inv, _, _ := repos.Inventory.Load(ctx, "19")
		repos.CharacterAssets = reviveCoinUseFailUOW{base: repos.CharacterAssets, late: late}
		owner, _ := NewOwner(repos)
		got, err := owner.ConsumeMegaphone(ctx, cmd)
		if err == nil || got.Character.CharacterID != "" {
			t.Fatal("failed transaction returned committed result")
		}
		afterC, _, _ := repos.Character.Load(ctx, "19")
		afterI, _, _ := repos.Inventory.Load(ctx, "19")
		if !reflect.DeepEqual(c, afterC) || !reflect.DeepEqual(inv, afterI) {
			t.Fatal("failed commit left debit or receipt")
		}
	}
}

func TestMegaphoneConcurrentSameRequestConsumesOnce(t *testing.T) {
	repos, cmd := megaphoneAssets(t)
	owner, _ := NewOwner(repos)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := owner.ConsumeMegaphone(context.Background(), cmd)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrMegaphoneReplay) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("commits=%d", successes.Load())
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "19")
	if inv.Slots["0:68"].Count != 2 {
		t.Fatal("duplicate debit")
	}
}

func TestMegaphoneLateIdentityChangeRollsBack(t *testing.T) {
	repos, cmd := megaphoneAssets(t)
	ctx := context.Background()
	c, _, _ := repos.Character.Load(ctx, "19")
	inv, _, _ := repos.Inventory.Load(ctx, "19")
	checks := 0
	cmd.Revalidate = func() bool { checks++; return checks == 1 }
	owner, _ := NewOwner(repos)
	got, err := owner.ConsumeMegaphone(ctx, cmd)
	if err == nil || got.Character.CharacterID != "" {
		t.Fatal("late stale identity accepted")
	}
	afterC, _, _ := repos.Character.Load(ctx, "19")
	afterI, _, _ := repos.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(c, afterC) || !reflect.DeepEqual(inv, afterI) {
		t.Fatal("late rejection left receipt or debit")
	}
}

func TestMegaphoneLowActorWithQualifiedAccountAndLastStack(t *testing.T) {
	repos, cmd := megaphoneAssets(t)
	ctx := context.Background()
	c, _, _ := repos.Character.Load(ctx, "19")
	c.Level = 50
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.CharacterID, c.Slot, c.Level = "20", 1, 86
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "19")
	stack := inv.Slots["0:68"]
	stack.Count = 1
	inv.Slots["0:68"] = stack
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	got, err := owner.ConsumeMegaphone(ctx, cmd)
	if err != nil || got.Remaining != 0 {
		t.Fatal("qualified account denied", err)
	}
	inv, _, _ = repos.Inventory.Load(ctx, "19")
	if _, ok := inv.Slots["0:68"]; ok {
		t.Fatal("empty stack remains")
	}
	cmd.Sequence++
	if _, err := owner.ConsumeMegaphone(ctx, cmd); err == nil {
		t.Fatal("empty stack reused")
	}
}
