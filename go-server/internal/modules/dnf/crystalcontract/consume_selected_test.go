package crystalcontract

import (
	"context"
	"errors"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestConsumeSelectedResolvesEveryCubeFromDurableSelection(t *testing.T) {
	ids := [6]int64{3033, 3034, 3035, 3036, 3037, 3262}
	for i, id := range ids {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			selection := int8(i)
			owner, repos, now := newOwnerTestRuntime(t, true, map[string]dnfrepo.ItemStack{testCrystalSlot(selection): {ItemID: id, Count: 2}})
			if _, err := owner.Select(context.Background(), "account-1", "19", selection, now); err != nil {
				t.Fatal(err)
			}
			result, err := owner.ConsumeSelected(context.Background(), "account-1", "19", now)
			if err != nil || result.ItemID != id || result.SlotIndex != uint16(354+i) || result.Remaining != 1 || result.Consumed != 1 || result.SelectionAfter != selection {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, err := owner.Select(context.Background(), "account-1", "19", SelectionNone, now); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ConsumeSelected(context.Background(), "account-1", "19", now); !errors.Is(err, ErrCubeRequestMismatch) {
				t.Fatalf("cancel consume error=%v", err)
			}
			inv, _, err := repos.AccountInventory.Load(context.Background(), "account-1")
			if err != nil || inv.Slots[testCrystalSlot(selection)].Count != 1 {
				t.Fatalf("cancel inventory=%+v err=%v", inv, err)
			}
		})
	}
}
